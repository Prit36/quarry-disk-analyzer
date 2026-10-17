package index

import (
	"container/heap"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type Analysis struct {
	Root              ID
	Types             []FileType
	TopFiles, Folders []ID
}

func (s *Snapshot) Visit(ctx context.Context, root ID, visit func(ID) error) error {
	return s.walk(ctx, root, func(id ID) (bool, error) { return true, visit(id) })
}

func (s *Snapshot) walk(ctx context.Context, root ID, visit func(ID) (bool, error)) error {
	if root == None || int(root) >= len(s.Nodes) {
		return fmt.Errorf("invalid scope")
	}
	current, count := root, 0
	for {
		if count&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		count++
		descend, err := visit(current)
		if err != nil {
			return err
		}
		if child := s.Nodes[current].FirstChild; descend && child != None {
			current = child
			continue
		}
		for current != root && s.Nodes[current].NextSibling == None {
			current = s.Nodes[current].Parent
		}
		if current == root {
			return ctx.Err()
		}
		current = s.Nodes[current].NextSibling
	}
}

type largestHeap struct {
	ids       []ID
	snapshot  *Snapshot
	allocated bool
}

func (h largestHeap) Len() int { return len(h.ids) }
func (h largestHeap) Less(first, second int) bool {
	return h.snapshot.Compare(h.ids[first], h.ids[second], Sort{Allocated: h.allocated}) < 0
}
func (h largestHeap) Swap(first, second int) {
	h.ids[first], h.ids[second] = h.ids[second], h.ids[first]
}
func (h *largestHeap) Push(value any) { h.ids = append(h.ids, value.(ID)) }
func (h *largestHeap) Pop() any {
	last := len(h.ids) - 1
	id := h.ids[last]
	h.ids = h.ids[:last]
	return id
}

func (s *Snapshot) Analyze(ctx context.Context, root ID, allocated bool, limit int, showMetadata ...bool) (Analysis, error) {
	result := Analysis{Root: root}
	groups := make(map[string]*FileType)
	top := &largestHeap{snapshot: s, allocated: allocated}
	folders := &largestHeap{snapshot: s, allocated: allocated}
	err := s.walk(ctx, root, func(id ID) (bool, error) {
		node := s.Nodes[id]
		if len(showMetadata) > 0 && !showMetadata[0] && root == 0 && node.Parent == 0 && strings.Contains(s.Engine, "MFT") && strings.HasPrefix(s.NameView(id), "$") {
			return false, nil
		}
		if node.IsDir() {
			if node.Parent == root {
				if folders.Len() < 10 {
					heap.Push(folders, id)
				} else if s.Compare(id, folders.ids[0], Sort{Allocated: allocated}) > 0 {
					folders.ids[0] = id
					heap.Fix(folders, 0)
				}
			}
			return true, nil
		}
		extension := strings.ToLower(filepath.Ext(s.NameView(id)))
		if extension == "" {
			extension = "(no extension)"
		}
		group := groups[extension]
		if group == nil {
			group = &FileType{Extension: extension}
			groups[extension] = group
		}
		group.Bytes += node.Size
		group.Allocated += node.Allocated
		group.Count++
		if limit > 0 {
			if top.Len() < limit {
				heap.Push(top, id)
			} else if s.Compare(id, top.ids[0], Sort{Allocated: allocated}) > 0 {
				top.ids[0] = id
				heap.Fix(top, 0)
			}
		}
		return true, nil
	})
	if err != nil {
		return Analysis{}, err
	}
	for _, group := range groups {
		result.Types = append(result.Types, *group)
	}
	result.TopFiles = top.ids
	result.Folders = folders.ids
	s.Sort(result.TopFiles, Sort{Allocated: allocated, Desc: true})
	s.Sort(result.Folders, Sort{Allocated: allocated, Desc: true})
	if len(result.Folders) > 10 {
		result.Folders = result.Folders[:10]
	}
	slices.SortFunc(result.Types, func(first, second FileType) int {
		firstValue, secondValue := first.Bytes, second.Bytes
		if allocated {
			firstValue, secondValue = first.Allocated, second.Allocated
		}
		if firstValue > secondValue {
			return -1
		}
		if firstValue < secondValue {
			return 1
		}
		return strings.Compare(first.Extension, second.Extension)
	})
	return result, nil
}
