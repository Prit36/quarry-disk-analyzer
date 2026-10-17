package index

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestExplicitSizeSortDoesNotFollowAllocationMetric(t *testing.T) {
	snapshot := New(`C:\fixture`)
	logical := snapshot.Add(0, "logical.bin", 1000, 1, 0, 0)
	allocated := snapshot.Add(0, "allocated.bin", 1, 1000, 0, 0)
	snapshot.Finalize()
	for _, metric := range []bool{false, true} {
		for _, column := range []string{"Size", "Allocated"} {
			result, err := snapshot.QueryRows(context.Background(), Query{Mode: "largest", Sort: Sort{Column: column, Desc: true, Allocated: metric}})
			if err != nil {
				t.Fatal(err)
			}
			want := logical
			if column == "Allocated" {
				want = allocated
			}
			if len(result.Rows) != 2 || result.Rows[0].ID != want {
				t.Fatalf("%s sort depends on visualization metric %v: %+v", column, metric, result.Rows)
			}
		}
	}
}

func TestSnapshotRoundTripCorruptionAndRetention(t *testing.T) {
	directory := t.TempDir()
	snapshot := New(`C:\fixture`)
	folder := snapshot.Add(0, "資料", 0, 0, 1760000000, Directory)
	snapshot.Add(folder, "large.bin", 1<<50, 1234, 1760000000, 0)
	snapshot.Engine = "NTFS MFT"
	snapshot.Cancelled = true
	snapshot.Finalize()
	for number := range 7 {
		snapshot.Started = time.Unix(int64(number+1), 0)
		path := filepath.Join(directory, "scan-"+time.Unix(int64(number+1), 0).Format("150405")+".quarry")
		if err := snapshot.Save(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadSnapshot(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(snapshot.Nodes, loaded.Nodes) || !bytes.Equal(snapshot.Names, loaded.Names) || !loaded.Cancelled || loaded.Root != snapshot.Root || !loaded.Started.Equal(snapshot.Started) {
			t.Fatal("snapshot changed data")
		}
	}
	if err := PruneSnapshots(directory); err != nil {
		t.Fatal(err)
	}
	saved, err := ListSnapshots(directory)
	if err != nil || len(saved) != 5 {
		t.Fatal("retention incorrect", len(saved), err)
	}
	data, err := os.ReadFile(saved[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-5] ^= 0xff
	if err := os.WriteFile(saved[0].Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(context.Background(), saved[0].Path); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshot.Save(ctx, filepath.Join(directory, "scan-canceled.quarry")); err != context.Canceled {
		t.Fatal("save cancellation ignored", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "scan-canceled.quarry")); !os.IsNotExist(err) {
		t.Fatal("canceled save published")
	}
	broken := New("root")
	broken.Nodes[0].FirstChild = ID(999)
	if err := broken.validate(context.Background()); err == nil {
		t.Fatal("invalid graph accepted")
	}
}

func TestSnapshotComparisonAndScopedExports(t *testing.T) {
	previous, current := New(`C:\fixture`), New(`C:\fixture`)
	for _, snapshot := range []*Snapshot{previous, current} {
		folder := snapshot.Add(0, "Users", 0, 0, 0, Directory)
		size := uint64(100)
		if snapshot == current {
			size = 150
		}
		snapshot.Add(folder, "data.bin", size, size, 0, 0)
		snapshot.Add(0, "outside.bin", 12, 12, 0, 0)
		snapshot.Finalize()
	}
	changes, err := CompareSnapshots(previous, current, false)
	if err != nil || len(changes) != 2 || changes[0].Difference != 50 || !changes[0].Increase {
		t.Fatal("comparison wrong", changes, err)
	}
	other := New(`D:\fixture`)
	other.Finalize()
	if _, err := CompareSnapshots(previous, other, false); err == nil {
		t.Fatal("mismatched roots compared")
	}
	var buffer bytes.Buffer
	if err := current.ExportJSON(context.Background(), &buffer, 1); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Nodes []struct {
			Path string
			Size uint64
		}
	}
	if err := json.Unmarshal(buffer.Bytes(), &document); err != nil || len(document.Nodes) != 2 || document.Nodes[1].Size != 150 {
		t.Fatal("JSON scope/data wrong", buffer.String(), err)
	}
	buffer.Reset()
	if err := current.ExportScope(context.Background(), &buffer, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buffer).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatal("CSV scope wrong", rows, err)
	}
	buffer.Reset()
	if err := current.ExportRows(context.Background(), &buffer, []Row{{ID: 2}}); err != nil {
		t.Fatal(err)
	}
	rows, err = csv.NewReader(&buffer).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][2] != "150" {
		t.Fatal("table results export wrong", rows, err)
	}
	buffer.Reset()
	if err := current.ExportReport(context.Background(), &buffer, 1); err != nil || !bytes.Contains(buffer.Bytes(), []byte("Logical bytes: 150")) || bytes.Contains(buffer.Bytes(), []byte("outside.bin")) {
		t.Fatal("report scope wrong", buffer.String(), err)
	}
}

func TestDuplicatesVerifyContentAndExcludeHardLinks(t *testing.T) {
	directory := t.TempDir()
	snapshot := New(directory)
	for name, content := range map[string]string{"first.bin": "same-content", "second.bin": "same-content", "different.bin": "other-bytes!"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Add(0, name, uint64(info.Size()), uint64(info.Size()), info.ModTime().Unix(), 0)
	}
	if err := os.Link(filepath.Join(directory, "first.bin"), filepath.Join(directory, "alias.bin")); err == nil {
		info, _ := os.Stat(filepath.Join(directory, "alias.bin"))
		snapshot.Add(0, "alias.bin", uint64(info.Size()), uint64(info.Size()), info.ModTime().Unix(), 0)
	}
	snapshot.Add(0, "missing.bin", 12, 12, 0, 0)
	snapshot.Finalize()
	result, err := snapshot.FindDuplicates(context.Background(), 0, &DuplicateProgress{})
	if err != nil || len(result.Groups) != 1 || len(result.Groups[0].IDs) != 2 || result.Skipped < 1 {
		t.Fatal("content verification or hard-link exclusion failed", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot.FindDuplicates(ctx, 0, nil); err != context.Canceled {
		t.Fatal("duplicate cancellation ignored", err)
	}
}
