package index

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func duplicateRegressionSnapshot(kind string) *Snapshot {
	s := New(`C:\duplicate-regression`)
	var folder ID
	for i := range 150000 {
		if i%256 == 0 {
			folder = s.Add(0, fmt.Sprintf("folder-%d", i/256), 0, 0, 0, Directory)
		}
		// Copies are 75,000 entries apart, spanning different worker ranges.
		key := i % 75000
		if kind == "Repeated" {
			key %= 31
		}
		size := uint64(key%127)<<32 | uint64(key%4096)
		if kind == "SameSize" {
			size = 65536
		}
		name := fmt.Sprintf("Item-%d.LOG", key)
		if kind == "Unicode" {
			name = fmt.Sprintf("日本-Ärger-%d.LOG", key)
		}
		if i >= 75000 {
			name = strings.ToLower(name)
		}
		s.Add(folder, name, size, size, 0, 0)
	}
	s.Finalize()
	return s
}

func duplicateMetadataReference(s *Snapshot, root ID, minimum uint64) []DuplicateGroup {
	type key struct {
		name string
		size uint64
	}
	byKey := map[key][]ID{}
	s.Visit(context.Background(), root, func(id ID) error {
		n := s.Nodes[id]
		if !n.IsDir() && n.Size >= minimum {
			k := key{strings.ToLower(s.NameView(id)), n.Size}
			byKey[k] = append(byKey[k], id)
		}
		return nil
	})
	var groups []DuplicateGroup
	for key, ids := range byKey {
		if len(ids) > 1 {
			slices.Sort(ids)
			groups = append(groups, DuplicateGroup{IDs: ids, Size: key.size})
		}
	}
	sortDuplicateGroups(groups)
	return groups
}

func TestDuplicateMetadataParallelMatchesReference(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previous)
	for _, kind := range []string{"Mixed", "SameSize", "Repeated", "Unicode"} {
		s := duplicateRegressionSnapshot(kind)
		for _, name := range []string{"ÄRGER.LOG", "ärger.log", "İ.log", "i.LOG", "K.LOG", "k.log", "Σ.log", "σ.LOG"} {
			s.Add(0, name, 1<<48, 1<<48, 0, 0)
		}
		s.Reaggregate()
		for _, scope := range []ID{0, 1} {
			for _, minimum := range []uint64{0, 1 << 20, 1 << 48} {
				want := duplicateMetadataReference(s, scope, minimum)
				for _, procs := range []int{1, 4} {
					runtime.GOMAXPROCS(procs)
					got, err := s.DuplicateCandidatesOver(context.Background(), scope, minimum)
					if err != nil || !reflect.DeepEqual(got.Groups, want) {
						t.Fatalf("%s scope=%d min=%d procs=%d: groups=%d want=%d err=%v", kind, scope, minimum, procs, len(got.Groups), len(want), err)
					}
				}
			}
		}
	}
}

func TestDuplicateMetadataResolvesFingerprintCollisions(t *testing.T) {
	s := New(`C:\not-opened`)
	var keys []duplicateKey
	for i := range 100 {
		name := fmt.Sprintf("item-%d.LOG", i%17)
		if i%2 == 0 {
			name = strings.ToLower(name)
		}
		id := s.Add(0, name, uint64(i%3)<<40, 0, 0, 0)
		keys = append(keys, duplicateKey{hash: 7, id: id})
	}
	// Deliberately collide every fingerprint, including different names and
	// file sizes. This also forces long linear-probe chains.
	got, err := s.groupDuplicateKeys(context.Background(), keys, 0, make([]ID, len(keys)))
	sortDuplicateGroups(got)
	want := duplicateMetadataReference(s, 0, 0)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("fingerprint collisions changed exact matches", err)
	}
}

type duplicateCancelContext struct {
	context.Context
	checks atomic.Int64
	after  int64
}

func (c *duplicateCancelContext) Err() error {
	if c.checks.Add(1) >= c.after {
		return context.Canceled
	}
	return nil
}

func TestDuplicateMetadataCancelsDuringParallelWork(t *testing.T) {
	s := duplicateRegressionSnapshot("Mixed")
	// Collection checks the context about 40 times. Cancel during the
	// hashing/partition passes, after collection has successfully finished.
	ctx := &duplicateCancelContext{Context: context.Background(), after: 50}
	if _, err := s.DuplicateCandidatesOver(ctx, 0, 0); err != context.Canceled {
		t.Fatalf("parallel grouping ignored cancellation: %v", err)
	}
}

func TestDuplicateSamplesOnlyRejectAndFullHashesConfirm(t *testing.T) {
	for _, size := range []int{duplicateFullRead - 1, duplicateFullRead, duplicateFullRead + 1, 4 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := New(t.TempDir())
			base := bytes.Repeat([]byte{'a'}, size)
			first := addDuplicateFile(t, s, 0, "first", base)
			second := addDuplicateFile(t, s, 0, "renamed", base)
			// The quarter-position difference is outside all three samples.
			// Matching samples must still receive full SHA-256 verification.
			for i, offset := range []int{size / 4, size - 1, 0} {
				data := bytes.Clone(base)
				data[offset] = 'b'
				addDuplicateFile(t, s, 0, fmt.Sprintf("different-%d", i), data)
			}
			s.Finalize()
			progress := &DuplicateProgress{}
			result, err := s.FindDuplicates(context.Background(), 0, progress)
			if err != nil || result.Skipped != 0 || result.Checked != 5 || len(result.Groups) != 1 || !slices.Equal(result.Groups[0].IDs, []ID{first, second}) {
				t.Fatal("sampling lost or falsely verified matches", result, err)
			}
			if progress.Bytes.Load() != progress.TotalBytes.Load() {
				t.Fatalf("incomplete progress: %d/%d", progress.Bytes.Load(), progress.TotalBytes.Load())
			}
		})
	}
}

func TestDuplicateBatchesKeepEntireSizeGroups(t *testing.T) {
	s := New(t.TempDir())
	for i := range 4094 {
		s.Add(0, fmt.Sprintf("missing-%d", i), uint64(i/2+1), 0, 0, 0)
	}
	for i := range 8 {
		size := 8192
		if i >= 6 {
			size *= 2
		}
		addDuplicateFile(t, s, 0, fmt.Sprintf("real-%d", i), bytes.Repeat([]byte{'a'}, size))
	}
	s.Finalize()
	p := &DuplicateProgress{}
	got, err := s.FindDuplicates(context.Background(), 0, p)
	if err != nil || len(got.Groups) != 2 || len(got.Groups[0].IDs) != 2 || len(got.Groups[1].IDs) != 6 || got.Checked != 8 || got.Skipped != 4094 || len(got.Issues) != 100 || p.Bytes.Load() != p.TotalBytes.Load() {
		t.Fatalf("size group split or incomplete progress: %+v, %v", got, err)
	}
}
