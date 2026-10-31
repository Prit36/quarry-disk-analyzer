package index

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDuplicateCandidatesMatchSizeGroups(t *testing.T) {
	s := benchSnapshot(100000)
	scope := s.Add(0, "scope", 0, 0, 0, Directory)
	for _, flags := range []uint16{0, Reparse, Shared, Unplaced, Incomplete} {
		s.Add(scope, "candidate", 900, 900, 0, flags)
	}
	s.Add(scope, "empty", 0, 0, 0, 0)
	s.Finalize()
	s.Remove(s.Nodes[0].FirstChild)
	for _, root := range []ID{0, scope, ID(len(s.Nodes) - 1), 1} {
		for _, minimum := range []uint64{1, 900, 1 << 40} {
			var want []ID
			err := s.Visit(context.Background(), root, func(id ID) error {
				n := s.Nodes[id]
				if !n.IsDir() && n.Size >= minimum && n.Flags&(Reparse|Shared|Unplaced|Incomplete) == 0 {
					want = append(want, id)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s.Sort(want, Sort{Column: "Size"})
			// Saved totals are not trusted as slice lengths or traversal limits.
			for _, count := range []uint32{0, 1, ^uint32(0)} {
				s.Nodes[root].Files = count
				got, err := s.duplicateCandidates(context.Background(), root, minimum)
				if err != nil || !slices.Equal(got, want) {
					t.Fatalf("root %d min %d count %d: %v", root, minimum, count, err)
				}
			}
		}
	}
	if _, err := s.duplicateCandidates(context.Background(), None, 1); err == nil {
		t.Fatal("invalid scope accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if outcome := s.hashCandidate(ctx, None, 1, nil, nil); outcome.err != context.Canceled {
		t.Fatal("canceled hash accessed its candidate", outcome.err)
	}
}

func TestDuplicatePrefixGroupsAndProgress(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	contents := [][]byte{
		bytes.Repeat([]byte("a"), duplicatePrefix+17),
		bytes.Repeat([]byte("a"), duplicatePrefix+17),
		append(bytes.Repeat([]byte("a"), duplicatePrefix), bytes.Repeat([]byte("b"), 17)...),
		bytes.Repeat([]byte("c"), duplicatePrefix+17),
		[]byte("small"), []byte("small"), []byte("unique"),
	}
	for i, data := range contents {
		name := string(rune('a'+i)) + ".bin"
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		s.Add(0, name, uint64(len(data)), uint64(len(data)), 0, 0)
	}
	s.Finalize()
	progress := &DuplicateProgress{}
	result, err := s.FindDuplicates(context.Background(), 0, progress)
	wantBytes := uint64(4*(duplicatePrefix+17) + 10)
	if err != nil || len(result.Groups) != 2 || result.Checked != 6 || result.Skipped != 0 || progress.Files.Load() != 6 || progress.TotalBytes.Load() != wantBytes || progress.Bytes.Load() != wantBytes {
		t.Fatalf("groups/progress changed: %+v, bytes %d/%d, %v", result, progress.Bytes.Load(), progress.TotalBytes.Load(), err)
	}
	for i, group := range result.Groups {
		slices.Sort(group.IDs)
		want := []ID{1, 2}
		if i == 1 {
			want = []ID{5, 6}
		}
		if !slices.Equal(group.IDs, want) {
			t.Fatal("incorrect verified content group", group)
		}
	}
}

func TestSegmentStreamReusesDecodersAndRejectsCorruption(t *testing.T) {
	var compressed, want bytes.Buffer
	for i := range 40 {
		data := bytes.Repeat([]byte{byte(i)}, gzipBlockSize)
		if i == 39 {
			data = data[:37]
		}
		want.Write(data)
		z := gzip.NewWriter(&compressed)
		z.Write(data)
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data := compressed.Bytes()
	starts := gzipMemberStarts(data)
	if len(starts) != 40 {
		t.Fatal("fixture did not create reusable slots", len(starts))
	}
	for _, corrupt := range []bool{false, true} {
		input := slices.Clone(data)
		if corrupt {
			input[len(input)-8] ^= 1 // CRC of the final member, after slot reuse.
		}
		s := newSegmentStream(input, starts)
		got, err := io.ReadAll(s)
		s.Close()
		s.Close()
		if corrupt && err == nil || !corrupt && (err != nil || !bytes.Equal(got, want.Bytes())) {
			t.Fatalf("corrupt %v: decoded %d bytes: %v", corrupt, len(got), err)
		}
	}
	// Early cancellation joins all in-flight decoders without draining results.
	s := newSegmentStream(data, starts)
	s.Close()
}

// Use an independent breadth-first reference to check the constant-space
// traversal on reordered records and malformed child/parent graphs.
func validSnapshotReference(s *Snapshot) bool {
	if len(s.Nodes) == 0 || !s.Nodes[0].IsDir() || s.Nodes[0].Parent != None || s.Nodes[0].NextSibling != None {
		return false
	}
	seen := make([]bool, len(s.Nodes))
	seen[0] = true
	queue := []ID{0}
	for i := 0; i < len(queue); i++ {
		n := s.Nodes[queue[i]]
		if uint64(n.NameOffset)+uint64(n.NameLength) > uint64(len(s.Names)) || !n.IsDir() && n.FirstChild != None {
			return false
		}
		for c := n.FirstChild; c != None; c = s.Nodes[c].NextSibling {
			if int(c) >= len(s.Nodes) || seen[c] || s.Nodes[c].Parent != queue[i] {
				return false
			}
			seen[c] = true
			queue = append(queue, c)
		}
	}
	return len(queue) == len(s.Nodes)
}

func TestSnapshotValidationMatchesGraphReference(t *testing.T) {
	r := rand.New(rand.NewPCG(271, 319))
	base := randomTree(r, 1000)
	// Include non-topological record order, as with scans of NTFS metadata.
	perm := r.Perm(len(base.Nodes) - 1)
	remap := func(id ID) ID {
		if id == 0 || id == None {
			return id
		}
		return ID(perm[id-1] + 1)
	}
	nodes := make([]Node, len(base.Nodes))
	for i, n := range base.Nodes {
		n.Parent, n.FirstChild, n.NextSibling = remap(n.Parent), remap(n.FirstChild), remap(n.NextSibling)
		nodes[remap(ID(i))] = n
	}
	base.Nodes = nodes
	if err := base.validate(context.Background()); err != nil {
		t.Fatal("valid reordered graph rejected", err)
	}
	for range 2000 {
		s := base.Clone()
		n := &s.Nodes[r.IntN(len(s.Nodes))]
		value := ID(r.IntN(len(s.Nodes) + 2))
		switch r.IntN(6) {
		case 0:
			n.Parent = value
		case 1:
			n.FirstChild = value
		case 2:
			n.NextSibling = value
		case 3:
			n.Flags ^= Directory
		case 4:
			n.NameOffset = r.Uint32()
		case 5:
			n.NameLength = uint16(r.Uint32())
		}
		if got, want := s.validate(context.Background()) == nil, validSnapshotReference(s); got != want {
			t.Fatalf("graph validation differs: got %v want %v", got, want)
		}
	}
	if err := (&Snapshot{}).validate(context.Background()); err == nil {
		t.Fatal("empty graph accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := base.validate(ctx); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
	cycle := New(`C:\cycle`)
	a := cycle.Add(0, "a", 0, 0, 0, Directory)
	b := cycle.Add(a, "b", 0, 0, 0, Directory)
	cycle.Nodes[0].FirstChild = None
	cycle.Nodes[a].Parent = b
	cycle.Nodes[b].FirstChild = a
	if err := cycle.validate(context.Background()); err == nil {
		t.Fatal("disconnected parent/child cycle accepted")
	}
	deep := New(`C:\deep`)
	parent := ID(0)
	for range 100000 {
		parent = deep.Add(parent, "d", 0, 0, 0, Directory)
	}
	if err := deep.validate(context.Background()); err != nil {
		t.Fatal("deep valid graph rejected", err)
	}
}
