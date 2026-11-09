package index

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"quarry/internal/platform"
)

func (s *Snapshot) ExportJSON(ctx context.Context, writer io.Writer, root ID) error {
	if root == None || int(root) >= len(s.Nodes) {
		return fmt.Errorf("invalid export scope")
	}
	metadata, err := json.Marshal(struct {
		Schema             int
		ScanRoot, Scope    string
		Partial            bool
		Logical, Allocated uint64
	}{1, s.Root, s.Path(root), s.Cancelled, s.Nodes[root].Size, s.Nodes[root].Allocated})
	if err != nil {
		return err
	}
	out := newExportBuffer(writer)
	out.buf = fmt.Appendf(out.buf, "{\"scan\":%s,\"nodes\":[", metadata)
	walker := s.newPathWalker()
	first := true
	// Each record is the json.Encoder output for
	// {Path, Name string; Size, Allocated uint64; Files, Folders uint32;
	// Modified int64; Flags uint16}, newline included, formatted directly.
	err = s.Visit(ctx, root, func(id ID) error {
		if !first {
			out.buf = append(out.buf, ',')
		}
		first = false
		out.buf = appendJSONRecord(out.buf, walker.path(id), s.NameView(id), &s.Nodes[id])
		return out.done()
	})
	if err != nil {
		return err
	}
	out.buf = append(out.buf, "]}\n"...)
	return out.flush()
}

func (s *Snapshot) ExportReport(ctx context.Context, writer io.Writer, root ID) error {
	analysis, err := s.Analyze(ctx, root, false, 10)
	if err != nil {
		return err
	}
	node := s.Nodes[root]
	_, err = fmt.Fprintf(writer, "Quarry scan report\nScan root: %s\nAnalyzed scope: %s\nStarted: %s\nDuration: %s\nEngine: %s\nPartial: %t\nLogical bytes: %d\nAllocated bytes: %d\nFiles: %d\nFolders: %d\nScan issues: %d\n\nLargest files (logical bytes)\n", s.Root, s.Path(root), platform.FormatDateTime(s.Started), s.Elapsed, s.Engine, s.Cancelled, node.Size, node.Allocated, node.Files, node.Dirs, s.Errors)
	if err != nil {
		return err
	}
	for _, id := range analysis.TopFiles {
		if _, err := fmt.Fprintf(writer, "%d\t%s\n", s.Nodes[id].Size, s.Path(id)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(writer, "\nExtensions (logical bytes / allocated bytes / files)\n"); err != nil {
		return err
	}
	for _, group := range analysis.Types {
		if _, err := fmt.Fprintf(writer, "%s\t%d\t%d\t%d\n", group.Extension, group.Bytes, group.Allocated, group.Count); err != nil {
			return err
		}
	}
	_, err = io.WriteString(writer, "\nLive scan data. Allocated file totals exclude some filesystem overhead; they are not drive capacity or reclaimable space.\n")
	return err
}
