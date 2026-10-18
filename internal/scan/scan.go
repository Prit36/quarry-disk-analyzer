// Package scan builds an index on a background goroutine and transfers ownership
// only when complete. UI code never reads a mutating snapshot.
package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	opts.Workers = min(16, opts.Workers)
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

// task is a folder to enumerate.
type task struct {
	id   index.ID
	path string
}

type result struct {
	task  task
	batch *dirBatch
	err   error
	done  bool
}

// dirBatch carries up to batchSize entries of one folder, with their names
// decoded into one arena, from a worker to the coordinator. Batches are
// pooled, so a scan allocates per batch, not per name.
type dirBatch struct {
	entries []entry
	names   []byte
}

const batchSize = 256

var batches = sync.Pool{New: func() any { return &dirBatch{entries: make([]entry, 0, batchSize), names: make([]byte, 0, 8<<10)} }}

func acquireBatch() *dirBatch { return batches.Get().(*dirBatch) }
func releaseBatch(b *dirBatch) {
	b.entries, b.names = b.entries[:0], b.names[:0]
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
				err := enumerate(workerCtx, t.path, func(batch *dirBatch) error {
					select {
					case results <- result{task: t, batch: batch}:
						return nil
					case <-workerCtx.Done():
						releaseBatch(batch)
						return workerCtx.Err()
					}
				})
				select {
				case results <- result{task: t, err: err, done: true}:
				case <-workerCtx.Done():
					return
				}
			}
		}()
	}
	defer func() { cancel(); close(jobs); workers.Wait() }()
	pending := []task{{0, root}}
	head, active := 0, 0
	identities := make([]identity, 0, 1<<16)
	for head < len(pending) || active > 0 {
		var send chan task
		var next task
		if head < len(pending) {
			send = jobs
			next = pending[head]
		}
		select {
		case <-ctx.Done():
			b.Cancelled = true
			p.Phase.Store("Finalizing partial scan")
			s := b.Build()
			markHardLinks(s, identities)
			s.Finalize()
			return s, nil
		case send <- next:
			pending[head] = task{}
			head++
			active++
			if head > 4096 && head*2 >= len(pending) {
				copy(pending, pending[head:])
				pending = pending[:len(pending)-head]
				head = 0
			}
		case r := <-results:
			t := r.task
			if r.done {
				active--
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
					pending = append(pending, task{id, filepath.Join(t.path, name)})
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
	s := b.Build()
	markHardLinks(s, identities)
	s.Finalize()
	return s, nil
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
func markHardLinks(s *index.Snapshot, ids []identity) {
	radixSort(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i].file == ids[i-1].file {
			n := &s.Nodes[ids[i].id]
			n.Flags |= index.Shared
			n.Size, n.Allocated = 0, 0
			s.HardLinks++
		}
	}
}

// radixSort orders identities by file ID in four stable 16-bit passes, so
// names of one file stay in the order they were added (by entry ID).
func radixSort(ids []identity) {
	if len(ids) < 2 {
		return
	}
	tmp := make([]identity, len(ids))
	counts := make([]int, 1<<16)
	src, dst := ids, tmp
	for shift := uint(0); shift < 64; shift += 16 {
		clear(counts)
		for _, v := range src {
			counts[v.file>>shift&0xffff]++
		}
		if counts[src[0].file>>shift&0xffff] == len(src) {
			continue // every key shares this digit
		}
		sum := 0
		for i, c := range counts {
			counts[i] = sum
			sum += c
		}
		for _, v := range src {
			d := v.file >> shift & 0xffff
			dst[counts[d]] = v
			counts[d]++
		}
		src, dst = dst, src
	}
	if &src[0] != &ids[0] {
		copy(ids, src)
	}
}

func unixTime(windowsTime uint64) int64 { return int64(windowsTime/10000000) - 11644473600 }
