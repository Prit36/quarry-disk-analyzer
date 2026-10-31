package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func addDuplicateFile(t *testing.T, s *Snapshot, parent ID, name string, data []byte) ID {
	t.Helper()
	path := filepath.Join(s.Path(parent), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return s.Add(parent, name, uint64(info.Size()), uint64(info.Size()), info.ModTime().Unix(), 0)
}

// Compare the optimized, parallel prefix search with an independent full-read
// reference. Include boundary sizes, renamed copies and several content groups
// of the same size, so missing results and false prefix matches both fail.
func TestDuplicatesMatchFullContentReference(t *testing.T) {
	s := New(t.TempDir())
	if err := os.Mkdir(filepath.Join(s.Root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	scope := s.Add(0, "nested", 0, 0, 0, Directory)
	var all []ID
	for _, size := range []int{0, 1, duplicatePrefix - 1, duplicatePrefix, duplicatePrefix + 1, 128 << 10} {
		for group := range 3 {
			data := bytes.Repeat([]byte{byte('a' + group)}, size)
			if size > duplicatePrefix {
				copy(data, bytes.Repeat([]byte("p"), duplicatePrefix))
			}
			for copy := range 3 {
				parent := ID(0)
				if copy != 0 {
					parent = scope
				}
				all = append(all, addDuplicateFile(t, s, parent, fmt.Sprintf("renamed-日本-%d-%d-%d.bin", size, group, copy), data))
			}
		}
	}
	all = append(all, addDuplicateFile(t, s, 0, "unique", []byte("unique length")))
	s.Finalize()
	for _, root := range []ID{0, scope} {
		for _, minimum := range []uint64{0, 1, duplicatePrefix, duplicatePrefix + 1, 1 << 30} {
			reference := make(map[[32]byte][]ID)
			for _, id := range all {
				if !s.Contains(root, id) || s.Nodes[id].Size < minimum {
					continue
				}
				data, err := os.ReadFile(s.Path(id))
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(data)
				reference[digest] = append(reference[digest], id)
			}
			var want []DuplicateGroup
			for _, ids := range reference {
				if len(ids) > 1 {
					want = append(want, DuplicateGroup{IDs: ids, Size: s.Nodes[ids[0]].Size})
				}
			}
			slices.SortFunc(want, func(a, b DuplicateGroup) int {
				if a.Size > b.Size {
					return -1
				}
				if a.Size < b.Size {
					return 1
				}
				return int(a.IDs[0]) - int(b.IDs[0])
			})
			progress := &DuplicateProgress{}
			progress.Bytes.Store(123) // reused progress must reset
			got, err := s.FindDuplicatesOver(context.Background(), root, minimum, progress)
			if err != nil || !reflect.DeepEqual(got.Groups, want) || got.Skipped != 0 {
				t.Fatalf("root=%d minimum=%d: got %+v, want %+v, err=%v", root, minimum, got, want, err)
			}
			if progress.Bytes.Load() != progress.TotalBytes.Load() || progress.Files.Load() != got.Checked {
				t.Fatalf("incomplete progress: %d/%d bytes", progress.Bytes.Load(), progress.TotalBytes.Load())
			}
		}
	}
}

func TestDuplicatesReportSkippedFilesAndCompleteProgress(t *testing.T) {
	for _, size := range []int{0, 12, duplicatePrefix + 20, duplicateFullRead + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := New(t.TempDir())
			data := bytes.Repeat([]byte("x"), size)
			first := addDuplicateFile(t, s, 0, "first", data)
			second := addDuplicateFile(t, s, 0, "second", data)
			changed := addDuplicateFile(t, s, 0, "changed", data)
			missing := addDuplicateFile(t, s, 0, "missing", data)
			s.Finalize()
			if err := os.Remove(s.Path(missing)); err != nil {
				t.Fatal(err)
			}
			future := time.Unix(s.Nodes[changed].ModTime()+10, 0)
			if err := os.Chtimes(s.Path(changed), future, future); err != nil {
				t.Fatal(err)
			}
			progress := &DuplicateProgress{}
			result, err := s.FindDuplicates(context.Background(), 0, progress)
			if err != nil || len(result.Groups) != 1 || !slices.Equal(result.Groups[0].IDs, []ID{first, second}) || result.Checked != 2 || result.Skipped != 2 || len(result.Issues) != 2 {
				t.Fatalf("skipped files lost or included: %+v, %v", result, err)
			}
			if progress.Bytes.Load() != uint64(4*size) || progress.TotalBytes.Load() != uint64(4*size) {
				t.Fatalf("skipped files stalled progress: %d/%d", progress.Bytes.Load(), progress.TotalBytes.Load())
			}
			for _, issue := range result.Issues {
				if issue.Path == "" || issue.Message == "" {
					t.Fatal("missing skip details", issue)
				}
			}
		})
	}
}

func TestDuplicateNameCandidatesUseSameEligibility(t *testing.T) {
	s := New(t.TempDir())
	for _, name := range []string{"left", "right"} {
		if err := os.Mkdir(filepath.Join(s.Root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	left := s.Add(0, "left", 0, 0, 0, Directory)
	right := s.Add(0, "right", 0, 0, 0, Directory)
	first := addDuplicateFile(t, s, left, "same.bin", nil)
	second := addDuplicateFile(t, s, right, "SAME.BIN", nil)
	bad := s.Add(right, "same.bin", 0, 0, 0, Incomplete)
	s.Finalize()
	result, err := s.DuplicateCandidatesOver(context.Background(), 0, 0)
	if err != nil || len(result.Groups) != 1 || !slices.Equal(result.Groups[0].IDs, []ID{first, second}) || slices.Contains(result.Groups[0].IDs, bad) {
		t.Fatalf("candidate eligibility or empty files: %+v, %v", result, err)
	}
	s.Nodes[left].Flags |= Unplaced
	if candidates, err := s.duplicateCandidates(context.Background(), first, 0); err != nil || len(candidates) != 0 {
		t.Fatal("file scope ignored an unresolved ancestor", candidates, err)
	}
	for _, verify := range []bool{false, true} {
		if verify {
			result, err = s.FindDuplicates(context.Background(), 0, nil)
		} else {
			result, err = s.DuplicateCandidatesOver(context.Background(), 0, 0)
		}
		if err != nil || len(result.Groups) != 0 {
			t.Fatal("unresolved ancestor was searched", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.DuplicateCandidatesOver(ctx, 0, 0); err != context.Canceled {
		t.Fatal("candidate cancellation ignored", err)
	}
	if _, err := s.DuplicateCandidatesOver(context.Background(), None, 0); err == nil {
		t.Fatal("invalid candidate scope accepted")
	}
}

func TestDuplicateHardLinksHaveDeterministicRepresentative(t *testing.T) {
	s := New(t.TempDir())
	first := addDuplicateFile(t, s, 0, "original", bytes.Repeat([]byte("x"), duplicatePrefix+1))
	if err := os.Link(s.Path(first), filepath.Join(s.Root, "alias")); err != nil {
		t.Skip(err)
	}
	s.Add(0, "alias", s.Nodes[first].Size, s.Nodes[first].Size, s.Nodes[first].ModTime(), 0)
	copyID := addDuplicateFile(t, s, 0, "independent", bytes.Repeat([]byte("x"), duplicatePrefix+1))
	s.Finalize()
	for range 12 {
		result, err := s.FindDuplicates(context.Background(), 0, nil)
		if err != nil || len(result.Groups) != 1 || !slices.Equal(result.Groups[0].IDs, []ID{first, copyID}) {
			t.Fatalf("unstable alias: %+v, %v", result, err)
		}
	}
}

func TestDuplicateClampedModificationTimes(t *testing.T) {
	s := New(t.TempDir())
	for _, name := range []string{"old-a", "old-b"} {
		id := addDuplicateFile(t, s, 0, name, []byte("same"))
		old := time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(s.Path(id), old, old); err != nil {
			t.Skip(err)
		}
		info, err := os.Stat(s.Path(id))
		if err != nil {
			t.Fatal(err)
		}
		s.Nodes[id].Modified = unixSeconds(info.ModTime().Unix())
	}
	s.Finalize()
	result, err := s.FindDuplicates(context.Background(), 0, nil)
	if err != nil || len(result.Groups) != 1 || result.Skipped != 0 {
		t.Fatal("clamped timestamp skipped unchanged files", result, err)
	}
}
