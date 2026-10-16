package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stratum/internal/index"
)

func TestDirectoryScanHardLinksAndEmptyFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, size := range map[string]int{"first.bin": 8192, "nested/second.txt": 20, "empty.txt": 0} {
		if err := os.WriteFile(filepath.Join(root, path), make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	linked := os.Link(filepath.Join(root, "first.bin"), filepath.Join(root, "alias.bin")) == nil
	s, err := Run(context.Background(), root, Options{ForceWalk: true, Workers: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := s.Nodes[0]
	wantFiles := uint32(3)
	if linked {
		wantFiles++
	}
	if n.Size != 8212 || n.Files != wantFiles || n.Dirs != 1 || s.Errors != 0 {
		t.Fatalf("bad totals %+v, issues %+v", n, s.Issues)
	}
	if linked && s.HardLinks != 1 {
		t.Fatal("hard link counted twice")
	}
	if n.Allocated < n.Size {
		t.Fatal("non-sparse allocation below logical bytes", n.Allocated)
	}
}
func TestCancellationAndInvalidRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := Run(ctx, t.TempDir(), Options{ForceWalk: true}, nil)
	if err != nil || !s.Cancelled {
		t.Fatal(s, err)
	}
	if _, err := Run(context.Background(), filepath.Join(t.TempDir(), "missing"), Options{}, nil); err == nil {
		t.Fatal("missing root accepted")
	}
}
func TestDirectoryLinksAreNotFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "outside.txt"), make([]byte, 100), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	s, err := Run(context.Background(), root, Options{ForceWalk: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Nodes[0].Size != 0 || s.SkippedLinks != 1 {
		t.Fatalf("followed directory link: %+v", s.Nodes[0])
	}
	for _, n := range s.Nodes {
		if n.Flags&index.Reparse != 0 {
			return
		}
	}
	t.Fatal("reparse flag missing")
}
func TestCancellationDoesNotLeaveWorkers(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() { Run(ctx, root, Options{ForceWalk: true, Workers: 4}, nil); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled scan blocked")
		}
	}
}
