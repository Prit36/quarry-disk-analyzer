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
		opts.Workers = min(4, max(1, runtime.NumCPU()/2))
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

type entry struct {
	name            string
	size, allocated uint64
	modified        int64
	fileID          uint64
	flags           uint16
}
type task struct {
	id   index.ID
	path string
}
type batch struct {
	parent  index.ID
	path    string
	entries []entry
	err     error
	done    bool
}

var entryBatches = sync.Pool{New: func() any { return make([]entry, 0, 256) }}

func acquireEntries() []entry        { return entryBatches.Get().([]entry)[:0] }
func releaseEntries(entries []entry) { clear(entries); entryBatches.Put(entries[:0]) }

// A single coordinator owns the index. Workers only enumerate directories.
// At most two buffered batches per worker, with 256 records in each batch.
func walk(ctx context.Context, root string, opts Options, p *Progress) (*index.Snapshot, error) {
	s := index.New(root)
	s.Engine = "Directory scan"
	excl := NewExclusions(opts.Exclude)
	s.Exclusions = excl.Patterns()
	jobs := make(chan task)
	results := make(chan batch, opts.Workers*2)
	workerCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for t := range jobs {
				err := enumerate(workerCtx, t.path, func(entries []entry) error {
					select {
					case results <- batch{parent: t.id, path: t.path, entries: entries}:
						return nil
					case <-workerCtx.Done():
						releaseEntries(entries)
						return workerCtx.Err()
					}
				})
				select {
				case results <- batch{parent: t.id, path: t.path, err: err, done: true}:
				case <-workerCtx.Done():
					return
				}
			}
		}()
	}
	defer func() { cancel(); close(jobs); workers.Wait() }()
	pending := []task{{0, root}}
	head, active := 0, 0
	seen := make(map[uint64]index.ID)
	for head < len(pending) || active > 0 {
		var send chan task
		var next task
		if head < len(pending) {
			send = jobs
			next = pending[head]
		}
		select {
		case <-ctx.Done():
			s.Cancelled = true
			p.Phase.Store("Finalizing partial scan")
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
		case b := <-results:
			if b.done {
				active--
				if b.err != nil && !errors.Is(b.err, context.Canceled) {
					s.Nodes[b.parent].Flags |= index.Incomplete
					s.AddIssue(b.path, b.err)
					p.Errors.Add(1)
				}
				continue
			}
			for _, e := range b.entries {
				if !excl.Empty() {
					full := ""
					if e.flags&index.Directory != 0 {
						full = filepath.Join(b.path, e.name)
					}
					if excl.Match(e.name, full, e.flags&index.Directory != 0) {
						s.Excluded++
						continue
					}
				}
				if e.flags&index.Incomplete != 0 {
					s.AddIssue(filepath.Join(b.path, e.name), fmt.Errorf("allocation metadata unavailable"))
					p.Errors.Add(1)
				}
				if e.flags&index.Directory == 0 && e.fileID != 0 {
					if _, ok := seen[e.fileID]; ok {
						e.flags |= index.Shared
						e.size = 0
						e.allocated = 0
						s.HardLinks++
					}
				}
				id := s.Add(b.parent, e.name, e.size, e.allocated, e.modified, e.flags)
				if e.flags&index.Directory != 0 {
					p.Folders.Add(1)
					if e.flags&index.Reparse != 0 {
						s.SkippedLinks++
					} else {
						pending = append(pending, task{id, filepath.Join(b.path, e.name)})
					}
				} else {
					if e.fileID != 0 && e.flags&index.Shared == 0 {
						seen[e.fileID] = id
					}
					p.Files.Add(1)
					p.Bytes.Add(e.size)
				}
			}
			releaseEntries(b.entries)
		}
	}
	p.Phase.Store("Aggregating sizes")
	s.Finalize()
	return s, nil
}

func unixTime(windowsTime uint64) int64 { return int64(windowsTime/10000000) - 11644473600 }
