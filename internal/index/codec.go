package index

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
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
