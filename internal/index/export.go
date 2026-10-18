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
	if _, err := fmt.Fprintf(writer, "{\"scan\":%s,\"nodes\":[", metadata); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	walker := s.newPathWalker()
	first := true
	// One record value is reused for every node: encoding a pointer to it
	// does not box a fresh copy per node, and the JSON is the same.
	var record struct {
		Path, Name      string
		Size, Allocated uint64
		Files, Folders  uint32
		Modified        int64
		Flags           uint16
	}
	err = s.Visit(ctx, root, func(id ID) error {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		first = false
		node := s.Nodes[id]
		record.Path, record.Name = walker.path(id), s.NameView(id)
		record.Size, record.Allocated, record.Files, record.Folders = node.Size, node.Allocated, node.Files, node.Dirs
		record.Modified, record.Flags = node.ModTime(), node.Flags
		return encoder.Encode(&record)
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, "]}\n")
	return err
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
