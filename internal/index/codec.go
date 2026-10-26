package index

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
)

// A saved node is its fields in order, little-endian, without padding: the
// layout encoding/binary gives Node. Encoding them by hand avoids
// reflection per node, which dominated saving and loading.
const (
	nodeRecord = 52
	nodeBatch  = 8192
)

func appendNode(b []byte, n *Node) []byte {
	le := binary.LittleEndian
	b = le.AppendUint64(b, n.Size)
	b = le.AppendUint64(b, n.Allocated)
	b = le.AppendUint64(b, uint64(n.ModTime())) // saved as int64: the format predates 32-bit times
	b = le.AppendUint32(b, uint32(n.Parent))
	b = le.AppendUint32(b, uint32(n.FirstChild))
	b = le.AppendUint32(b, uint32(n.NextSibling))
	b = le.AppendUint32(b, n.NameOffset)
	b = le.AppendUint16(b, n.NameLength)
	b = le.AppendUint16(b, n.Flags)
	b = le.AppendUint32(b, n.Files)
	return le.AppendUint32(b, n.Dirs)
}

func decodeNode(b []byte) Node {
	le := binary.LittleEndian
	_ = b[nodeRecord-1]
	return Node{
		Size: le.Uint64(b), Allocated: le.Uint64(b[8:]), Modified: unixSeconds(int64(le.Uint64(b[16:]))),
		Parent: ID(le.Uint32(b[24:])), FirstChild: ID(le.Uint32(b[28:])), NextSibling: ID(le.Uint32(b[32:])),
		NameOffset: le.Uint32(b[36:]), NameLength: le.Uint16(b[40:]), Flags: le.Uint16(b[42:]),
		Files: le.Uint32(b[44:]), Dirs: le.Uint32(b[48:]),
	}
}

// parallelGzip compresses fixed-size blocks on several cores and writes
// them in order, each as a gzip member. Concatenated members form one
// valid gzip stream (RFC 1952), which gzip.Reader, and so every earlier
// release, reads as a whole.
type parallelGzip struct {
	w       io.Writer
	block   *gzipBlock
	free    chan *gzipBlock // written blocks, reused: memory stays bounded
	jobs    chan *gzipBlock
	order   chan *gzipBlock
	done    chan error
	workers sync.WaitGroup
}

type gzipBlock struct {
	data  []byte
	out   bytes.Buffer
	err   error
	ready chan struct{}
}

const gzipBlockSize = 1 << 20

func newParallelGzip(w io.Writer) *parallelGzip {
	n := max(1, min(4, runtime.NumCPU()))
	z := &parallelGzip{w: w, free: make(chan *gzipBlock, 2*n+1), jobs: make(chan *gzipBlock, n), order: make(chan *gzipBlock, n), done: make(chan error, 1)}
	z.block = z.newBlock()
	for range n {
		z.workers.Add(1)
		go func() {
			defer z.workers.Done()
			var zw *gzip.Writer
			for b := range z.jobs {
				if zw == nil {
					zw, _ = gzip.NewWriterLevel(&b.out, gzip.BestSpeed)
				} else {
					zw.Reset(&b.out)
				}
				_, b.err = zw.Write(b.data)
				if err := zw.Close(); b.err == nil {
					b.err = err
				}
				close(b.ready)
			}
		}()
	}
	go func() {
		var err error
		for b := range z.order {
			<-b.ready
			if err == nil {
				err = b.err
			}
			if err == nil {
				_, err = z.w.Write(b.out.Bytes())
			}
			select {
			case z.free <- b:
			default:
			}
		}
		z.done <- err
	}()
	return z
}

func (z *parallelGzip) newBlock() *gzipBlock {
	select {
	case b := <-z.free:
		b.data, b.err, b.ready = b.data[:0], nil, make(chan struct{})
		b.out.Reset()
		return b
	default:
		b := &gzipBlock{data: make([]byte, 0, gzipBlockSize), ready: make(chan struct{})}
		b.out.Grow(gzipBlockSize / 2)
		return b
	}
}

func (z *parallelGzip) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		b := z.block
		k := min(len(p), gzipBlockSize-len(b.data))
		b.data = append(b.data, p[:k]...)
		p = p[k:]
		if len(b.data) == gzipBlockSize {
			z.flush()
		}
	}
	return n, nil
}

func (z *parallelGzip) flush() {
	if len(z.block.data) == 0 {
		return
	}
	b := z.block
	z.order <- b
	z.jobs <- b
	z.block = z.newBlock()
}

// Close compresses what remains and returns the first error.
func (z *parallelGzip) Close() error {
	z.flush()
	close(z.jobs)
	close(z.order)
	err := <-z.done
	z.workers.Wait()
	return err
}

// errSnapshotSegment marks a failure while decoding members in parallel;
// the caller then reads the file serially, which reports the real problem.
var errSnapshotSegment = errors.New("snapshot member")

// gzipMemberStarts finds where the parallel writer's gzip members begin:
// a member header with no name, comment or extra field and zero modification
// time, right after a previous member whose trailer records exactly one
// block of uncompressed data. The result is a guide, not a proof: a wrong
// start fails to decode and the caller falls back to a serial read.
func gzipMemberStarts(data []byte) []int {
	const header = "\x1f\x8b\x08\x00\x00\x00\x00\x00"
	var isize [4]byte
	binary.LittleEndian.PutUint32(isize[:], gzipBlockSize)
	starts := []int{0}
	for offset := 1; offset < len(data); {
		i := bytes.Index(data[offset:], []byte(header))
		if i < 0 {
			break
		}
		p := offset + i
		if p >= 4 && string(data[p-4:p]) == string(isize[:]) {
			starts = append(starts, p)
		}
		offset = p + 1
	}
	return starts
}

// segmentStream decodes consecutive gzip segments on several cores and
// yields their output in order. At most GOMAXPROCS+2 slots (capped at 14)
// are decoded or waiting, so memory stays bounded without idle-core buffers.
type segmentStream struct {
	results chan *segmentResult
	free    chan *segmentResult
	stop    chan struct{}
	current *segmentResult
	offset  int
	err     error
	workers sync.WaitGroup
	closed  sync.Once
}

type segmentResult struct {
	data  []byte
	err   error
	ready chan struct{}
	input bytes.Reader
	zr    *gzip.Reader
}

func newSegmentStream(data []byte, starts []int) *segmentStream {
	queued := max(1, min(12, runtime.GOMAXPROCS(0)))
	slots := min(len(starts), queued+2)
	s := &segmentStream{results: make(chan *segmentResult, queued), free: make(chan *segmentResult, slots), stop: make(chan struct{})}
	for range slots {
		s.free <- &segmentResult{ready: make(chan struct{}, 1)}
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(s.results)
		for i, start := range starts {
			var r *segmentResult
			select {
			case r = <-s.free:
			case <-s.stop:
				return
			}
			end := len(data)
			if i+1 < len(starts) {
				end = starts[i+1]
			}
			s.workers.Add(1)
			go func(segment []byte) {
				defer s.workers.Done()
				r.data, r.err = r.decode(segment)
				r.ready <- struct{}{}
			}(data[start:end])
			select {
			case s.results <- r:
			case <-s.stop:
				return
			}
		}
	}()
	return s
}

// A reusable result slot owns its inflater as well as its output buffer. Reset
// retains the DEFLATE window and tables instead of allocating them per member.
func (r *segmentResult) decode(segment []byte) ([]byte, error) {
	r.input.Reset(segment)
	var err error
	if r.zr == nil {
		r.zr, err = gzip.NewReader(&r.input)
	} else {
		err = r.zr.Reset(&r.input)
	}
	if err != nil {
		return nil, err
	}
	defer r.zr.Close()
	out := bytes.NewBuffer(r.data[:0])
	out.Grow(gzipBlockSize + bytes.MinRead)
	// A false boundary can span multiple members. Fall back to the serial
	// reader before decoding more than one block into a retained buffer.
	_, err = out.ReadFrom(io.LimitReader(r.zr, gzipBlockSize+1))
	if out.Len() > gzipBlockSize && err == nil {
		err = fmt.Errorf("gzip member exceeds %d bytes", gzipBlockSize)
	}
	return out.Bytes(), err
}

func (s *segmentStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for s.current == nil || s.offset == len(s.current.data) {
		if s.current != nil {
			s.free <- s.current
			s.current, s.offset = nil, 0
		}
		if s.err != nil {
			return 0, s.err
		}
		r, ok := <-s.results
		if !ok {
			return 0, io.EOF
		}
		<-r.ready
		if r.err != nil {
			s.err = fmt.Errorf("%w: %v", errSnapshotSegment, r.err)
			return 0, s.err
		}
		s.current = r
	}
	n := copy(p, s.current.data[s.offset:])
	s.offset += n
	return n, nil
}

// Close stops scheduling and joins every decoder before releasing its buffers.
// A canceled load cannot leave workers retaining the compressed scan.
func (s *segmentStream) Close() {
	s.closed.Do(func() {
		close(s.stop)
		s.workers.Wait()
		s.current, s.results, s.free = nil, nil, nil
		s.err = io.ErrClosedPipe
	})
}
