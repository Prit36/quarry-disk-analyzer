// Package scan builds an index on a background goroutine and transfers ownership
// only when complete. UI code never reads a mutating snapshot.
package scan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"quarry/internal/index"
)

type Options struct {
	Workers   int
	ForceWalk bool
	// AllowFallback must be explicit: an NTFS request must never silently turn
	// into a directory traversal that appears to be the fast MFT engine.
	AllowFallback bool
	// Exclude lists scan exclusion patterns (see Exclusions).
	Exclude []string
}

type MFTError struct{ Cause error }

func (e *MFTError) Error() string { return "MFT scan unavailable: " + e.Cause.Error() }
func (e *MFTError) Unwrap() error { return e.Cause }

type Progress struct {
	Files        atomic.Uint64
	Folders      atomic.Uint64
	Bytes        atomic.Uint64
	Records      atomic.Uint64
	TotalRecords atomic.Uint64
	Errors       atomic.Uint64
	Phase        atomic.Value // string
}

func (p *Progress) Status() string {
	if s := p.Phase.Load(); s != nil {
		return s.(string)
	}
	return "Preparing"
}
func Run(ctx context.Context, root string, opts Options, p *Progress) (*index.Snapshot, error) {
	if p == nil {
		p = &Progress{}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("choose a folder or drive")
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("choose the real folder, rather than a symbolic link")
	}
	if opts.Workers <= 0 {
		// Directory queries are bound by kernel and filter-driver work per
		// folder, which scales to about eight concurrent queries on SSDs.
		opts.Workers = min(8, max(2, runtime.NumCPU()))
	}
	opts.Workers = min(64, opts.Workers)
	var fallback string
	if !opts.ForceWalk && eligibleMFT(root) {
		p.Phase.Store("Reading NTFS metadata")
		s, err := scanMFT(ctx, root, p)
		if err == nil {
			excl := NewExclusions(opts.Exclude)
			s.Exclusions = excl.Patterns()
			if !excl.Empty() {
				p.Phase.Store("Applying exclusions")
				if err := excl.apply(ctx, s); err != nil {
					return nil, err
				}
			}
			return s, nil
		}
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if !opts.AllowFallback {
			return nil, &MFTError{Cause: err}
		}
		fallback = err.Error()
		p.Files.Store(0)
		p.Folders.Store(0)
		p.Bytes.Store(0)
		p.Records.Store(0)
		p.TotalRecords.Store(0)
		p.Errors.Store(0)
	}
	p.Phase.Store("Scanning folders")
	s, err := walk(ctx, root, opts, p)
	if s != nil {
		s.Fallback = fallback
	}
	return s, err
}

// entry is one directory record; its name lives in the batch's arena.
type entry struct {
	size, allocated uint64
	modified        int64
	fileID          uint64
	nameOff         uint32
	nameLen         uint16
	flags           uint16
}

// task is a folder to enumerate. While parent is set, the folder is opened
// by its name relative to the parent's open handle: the kernel then resolves
// one path component instead of every ancestor again, which is most of the
// cost of opening a folder deep in a drive.
type task struct {
	id      index.ID
	path    string
	parent  *dirHandle // the parent folder, held open for this task; nil for the root
	nameLen int        // the folder's name is the last nameLen bytes of path; 0: open by path
}

func (t task) name() string { return t.path[len(t.path)-t.nameLen:] }

// dirHandle is an open folder shared by the tasks of its subfolders. refs
// counts its holders: the coordinator until the folder's done result, and
// each queued subfolder task until that task has opened its folder.
type dirHandle struct {
	handle uintptr
	refs   atomic.Int32
}

func (d *dirHandle) retain() { d.refs.Add(1) }

func (d *dirHandle) release() {
	if d != nil && d.refs.Add(-1) == 0 {
		closeDirectory(d.handle)
	}
}

// releaseLater is release for the coordinator: closing a handle takes the
// kernel longer than indexing a folder's entries, so the last reference
// closes it on another goroutine.
func (d *dirHandle) releaseLater(closer chan<- uintptr) {
	if d != nil && d.refs.Add(-1) == 0 {
		closer <- d.handle
	}
}

type result struct {
	task  task
	batch *dirBatch
	dir   *dirHandle // with done: the folder's handle, nil if it did not open
	err   error
	done  bool
}

// dirBatch carries up to batchSize entries of one folder, with their names
// decoded into one arena, from a worker to the coordinator. Batches are
// pooled, so a scan allocates per batch, not per name.
type dirBatch struct {
	entries []entry
	names   []byte
	dir     *dirHandle // the enumerated folder, open
}

const batchSize = 256

var batches = sync.Pool{New: func() any { return &dirBatch{entries: make([]entry, 0, batchSize), names: make([]byte, 0, 8<<10)} }}

func acquireBatch() *dirBatch { return batches.Get().(*dirBatch) }
func releaseBatch(b *dirBatch) {
	b.entries, b.names, b.dir = b.entries[:0], b.names[:0], nil
	batches.Put(b)
}

func (b *dirBatch) add(e entry, name string) {
	e.nameOff = uint32(len(b.names))
	b.names = append(b.names, name...)
	e.nameLen = uint16(len(name))
	b.entries = append(b.entries, e)
}

// name borrows the batch's arena; it is valid until the batch is released.
func (b *dirBatch) name(e *entry) string {
	if e.nameLen == 0 {
		return ""
	}
	return unsafe.String(&b.names[e.nameOff], int(e.nameLen))
}

// A single coordinator owns the index. Workers only enumerate directories.
// At most two buffered batches per worker, with 256 records in each batch.
// Folders are taken depth first (last queued, first scanned): the queue,
// and the parent handles it holds open, stay proportional to the depth
// of the tree instead of to its widest level.
func walk(ctx context.Context, root string, opts Options, p *Progress) (*index.Snapshot, error) {
	b := index.NewBuilder(root)
	b.Engine = "Directory scan"
	excl := NewExclusions(opts.Exclude)
	b.Exclusions = excl.Patterns()
	jobs := make(chan task)
	results := make(chan result, opts.Workers*2)
	workerCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for t := range jobs {
				dir, err := enumerate(workerCtx, t, func(batch *dirBatch) error {
					select {
					case results <- result{task: t, batch: batch}:
						return nil
					case <-workerCtx.Done():
						releaseBatch(batch)
						return workerCtx.Err()
					}
				})
				select {
				case results <- result{task: t, dir: dir, err: err, done: true}:
				case <-workerCtx.Done():
					dir.release()
					return
				}
			}
		}()
	}
	pending := []task{{id: 0, path: root}}
	closer := make(chan uintptr, 256)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for h := range closer {
			closeDirectory(h)
		}
	}()
	defer func() {
		cancel()
		close(jobs)
		workers.Wait()
		// Close what an interrupted scan still holds open.
		for {
			select {
			case r := <-results:
				if r.done {
					r.dir.release()
				} else {
					releaseBatch(r.batch)
				}
				continue
			default:
			}
			break
		}
		for _, t := range pending {
			t.parent.release()
		}
		close(closer)
		<-closed
	}()
	active := 0
	identities := make([]identity, 0, 1<<16)
	for len(pending) > 0 || active > 0 {
		var send chan task
		var next task
		if len(pending) > 0 {
			send = jobs
			next = pending[len(pending)-1]
		}
		select {
		case <-ctx.Done():
			b.Cancelled = true
			p.Phase.Store("Finalizing partial scan")
			return finish(b, identities), nil
		case send <- next:
			pending[len(pending)-1] = task{}
			pending = pending[:len(pending)-1]
			active++
		case r := <-results:
			t := r.task
			if r.done {
				active--
				r.dir.releaseLater(closer)
				if r.err != nil && !errors.Is(r.err, context.Canceled) {
					b.Node(t.id).Flags |= index.Incomplete
					b.AddIssue(t.path, r.err)
					p.Errors.Add(1)
				}
				continue
			}
			batch := r.batch
			var files, folders, bytes uint64
			for i := range batch.entries {
				e := &batch.entries[i]
				name := batch.name(e)
				dir := e.flags&index.Directory != 0
				if !excl.Empty() {
					full := ""
					if dir && excl.NeedsPaths() {
						full = filepath.Join(t.path, name)
					}
					if excl.Match(name, full, dir) {
						b.Excluded++
						continue
					}
				}
				if e.flags&index.Incomplete != 0 {
					b.AddIssue(filepath.Join(t.path, name), fmt.Errorf("allocation metadata unavailable"))
					p.Errors.Add(1)
				}
				id := b.Add(t.id, name, e.size, e.allocated, e.modified, e.flags)
				if dir {
					folders++
					if e.flags&index.Reparse != 0 {
						b.SkippedLinks++
						continue
					}
					child := task{id: id, path: filepath.Join(t.path, name)}
					if batch.dir != nil && len(name) > 0 && strings.HasSuffix(child.path, name) {
						batch.dir.retain()
						child.parent, child.nameLen = batch.dir, len(name)
					}
					pending = append(pending, child)
				} else {
					if e.fileID != 0 {
						identities = append(identities, identity{e.fileID, id})
					}
					files++
					bytes += e.size
				}
			}
			p.Files.Add(files)
			p.Folders.Add(folders)
			p.Bytes.Add(bytes)
			releaseBatch(batch)
		}
	}
	p.Phase.Store("Aggregating sizes")
	return finish(b, identities), nil
}

// finish builds the index, marks hard links and aggregates sizes. The
// identities are sorted while the index is assembled.
func finish(b *index.Builder, identities []identity) *index.Snapshot {
	sorted := make(chan []identity, 1)
	go func() { sorted <- sortIdentities(identities) }()
	s := b.Build()
	markHardLinks(s, <-sorted)
	s.Finalize()
	return s
}

// identity pairs a file's volume file ID with its entry.
type identity struct {
	file uint64
	id   index.ID
}

// markHardLinks finds files listed under several names (hard links) and
// counts each file once: every name after the first seen keeps its entry,
// marked Shared, without bytes. Sorting a flat list after the scan needs a
// fraction of the memory of a hash set of every file ID held during it.
// sorted is ids after sortIdentities.
func markHardLinks(s *index.Snapshot, sorted []identity) {
	for i := 1; i < len(sorted); i++ {
		if sorted[i].file == sorted[i-1].file {
			n := &s.Nodes[sorted[i].id]
			n.Flags |= index.Shared
			n.Size, n.Allocated = 0, 0
			s.HardLinks++
		}
	}
}

// sortIdentities groups identities of the same file together, each group
// in entry order (the first name seen first). One counting pass scatters
// them into 64Ki buckets by a hash of the file ID; the buckets, a few
// entries each, are then sorted by (file, entry) in parallel. The order
// between files is arbitrary.
func sortIdentities(ids []identity) []identity {
	if len(ids) < 2 {
		return ids
	}
	const bits = 16
	bucket := func(file uint64) int { return int(file * 0x9e3779b97f4a7c15 >> (64 - bits)) }
	starts := make([]int, 1<<bits+1)
	for _, v := range ids {
		starts[bucket(v.file)+1]++
	}
	for i := 1; i < len(starts); i++ {
		starts[i] += starts[i-1]
	}
	out := make([]identity, len(ids))
	next := append([]int(nil), starts[:1<<bits]...)
	for _, v := range ids {
		d := bucket(v.file)
		out[next[d]] = v
		next[d]++
	}
	byFile := func(a, b identity) int {
		if c := cmp.Compare(a.file, b.file); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	}
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/32768))
	var group sync.WaitGroup
	for w := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			// Buckets [lo, hi) hold about an equal share of the entries.
			lo, _ := slices.BinarySearch(starts, len(ids)*w/workers)
			hi, _ := slices.BinarySearch(starts, len(ids)*(w+1)/workers)
			if w == workers-1 {
				hi = 1 << bits
			}
			for d := lo; d < hi; d++ {
				if part := out[starts[d]:starts[d+1]]; len(part) > 1 {
					slices.SortFunc(part, byFile)
				}
			}
		}()
	}
	group.Wait()
	return out
}

func unixTime(windowsTime uint64) int64 { return int64(windowsTime/10000000) - 11644473600 }
