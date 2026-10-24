package desktop

import (
	"context"

	"quarry/internal/index"
)

// initialTree keeps large root-folder sorts and row allocation off the UI
// thread when a scan or saved snapshot first arrives. It is consumed only if
// the snapshot and sort still match; analysis filters can replace the snapshot.
type initialTree struct {
	snap  *index.Snapshot
	order index.Sort
	kids  []index.ID
	rows  []treeRow
}

func prepareInitialTree(s *index.Snapshot, order index.Sort) *initialTree {
	kids := s.Children(0)
	_ = s.SortContext(context.Background(), kids, order)
	rows := make([]treeRow, len(kids)+1)
	rows[0] = treeRow{id: 0, kids: s.Nodes[0].FirstChild != index.None}
	for i, id := range kids {
		n := &s.Nodes[id]
		rows[i+1] = treeRow{id: id, depth: 1, kids: n.IsDir() && n.FirstChild != index.None}
	}
	return &initialTree{snap: s, order: order, kids: kids, rows: rows}
}
