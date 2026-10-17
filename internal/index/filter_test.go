package index

import (
	"context"
	"testing"
	"time"
)

func TestAdvancedFilterRangesAndScope(t *testing.T) {
	snapshot := New("fixture")
	folder := snapshot.Add(0, "folder", 0, 0, 0, Directory)
	date, _ := time.ParseInLocation("2006-01-02", "2026-10-17", time.Local)
	snapshot.Add(folder, "large.zip", 2<<30, 2<<30, date.Unix(), 0)
	snapshot.Add(folder, "small.zip", 30, 30, date.AddDate(0, 0, -1).Unix(), 0)
	snapshot.Add(folder, "missing-date.zip", 2<<30, 2<<30, 0, 0)
	snapshot.Add(0, "outside.zip", 2<<30, 2<<30, date.Unix(), 0)
	snapshot.Finalize()
	result, err := snapshot.QueryRows(context.Background(), Query{Root: folder, Text: "type:file ext:zip min:1GiB max:3GiB after:2026-10-17 before:2026-10-17"})
	if err != nil || len(result.Rows) != 1 || snapshot.NameView(result.Rows[0].ID) != "large.zip" {
		t.Fatal("advanced scope/range mismatch", result, err)
	}
	result, err = snapshot.QueryRows(context.Background(), Query{Root: folder, Text: "before:2026-10-17 min:1GiB"})
	if err != nil || len(result.Rows) != 1 {
		t.Fatal("missing dates matched a date range", result, err)
	}
	for _, query := range []string{"min:NaN", "max:Inf", "min:3GiB max:1GiB", "after:invalid", "after:2026-10-18 before:2026-10-17", "type:file type:folder"} {
		if _, err := snapshot.QueryRows(context.Background(), Query{Text: query}); err == nil {
			t.Fatal("invalid filter accepted", query)
		}
	}
}
