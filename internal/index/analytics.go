package index

import (
	"container/heap"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func fastExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

type largestHeap struct {
	ids       []ID
	snapshot  *Snapshot
	allocated bool
}

func (h largestHeap) Len() int { return len(h.ids) }
func (h largestHeap) Less(first, second int) bool {
	a, b := h.ids[first], h.ids[second]
	na, nb := &h.snapshot.Nodes[a], &h.snapshot.Nodes[b]
	if h.allocated {
		if na.Allocated != nb.Allocated {
			return na.Allocated < nb.Allocated
		}
	} else {
		if na.Size != nb.Size {
			return na.Size < nb.Size
		}
	}
	return a < b
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
	if root == None || int(root) >= len(s.Nodes) {
		return Analysis{}, fmt.Errorf("invalid scope")
	}
	result := Analysis{Root: root}
	hasMetadataExclusion := len(showMetadata) > 0 && !showMetadata[0] && root == 0 && strings.Contains(s.Engine, "MFT")

	// Collect direct child folders of root directly from FirstChild/NextSibling
	for c := s.Nodes[root].FirstChild; c != None; c = s.Nodes[c].NextSibling {
		if s.Nodes[c].IsDir() {
			if hasMetadataExclusion && strings.HasPrefix(s.NameView(c), "$") {
				continue
			}
			result.Folders = append(result.Folders, c)
		}
	}
	s.Sort(result.Folders, Sort{Allocated: allocated, Desc: true})
	if len(result.Folders) > 10 {
		result.Folders = result.Folders[:10]
	}

	n := &s.Nodes[root]
	workers := min(max(1, runtime.GOMAXPROCS(0)/2), max(1, int((uint64(n.Files)+uint64(n.Dirs))/parallelSearchEntries)))
	if workers > 1 && n.IsDir() {
		target := max(uint64(1024), (uint64(n.Files)+uint64(n.Dirs))/uint64(4*workers))
		pieces, err := s.searchPieces(ctx, root, target)
		if err != nil {
			return Analysis{}, err
		}
		for len(pieces) < 4*workers {
			best := -1
			for i, p := range pieces {
				if p.id == p.last && s.Nodes[p.id].IsDir() && s.Nodes[p.id].FirstChild != None && (best < 0 || p.weight > pieces[best].weight) {
					best = i
				}
			}
			if best < 0 {
				break
			}
			children, err := s.searchPieces(ctx, pieces[best].id, target)
			if err != nil {
				return Analysis{}, err
			}
			pieces = slices.Replace(pieces, best, best+1, children...)
		}
		if len(pieces) > 1 {
			workers = min(workers, len(pieces))
			partials := make([]map[string]*FileType, workers)
			partialTops := make([]*largestHeap, workers)
			errs := make([]error, workers)
			var next atomic.Int64
			var wg sync.WaitGroup
			for w := range workers {
				wg.Add(1)
				go func(workerIdx int) {
					defer wg.Done()
					groups := make(map[string]*FileType)
					byExt := make(map[string]*FileType)
					var last *FileType
					var lastExt string
					var top *largestHeap
					if limit > 0 {
						top = &largestHeap{snapshot: s, allocated: allocated}
					}
					count := 0
					for {
						i := int(next.Add(1) - 1)
						if i >= len(pieces) {
							break
						}
						p := pieces[i]
						for sib := p.id; ; sib = s.Nodes[sib].NextSibling {
							if count&4095 == 0 {
								if err := ctx.Err(); err != nil {
									errs[workerIdx] = err
									return
								}
							}
							count++
							if hasMetadataExclusion && s.Nodes[sib].Parent == 0 && strings.HasPrefix(s.NameView(sib), "$") {
								if sib == p.last {
									break
								}
								continue
							}
							current := sib
							for {
								if count&4095 == 0 {
									if err := ctx.Err(); err != nil {
										errs[workerIdx] = err
										return
									}
								}
								count++
								node := &s.Nodes[current]
								descend := true
								if hasMetadataExclusion && current != 0 && node.Parent == 0 && strings.HasPrefix(s.NameView(current), "$") {
									descend = false
								} else if !node.IsDir() {
									ext := fastExt(s.NameView(current))
									group := last
									if group == nil || ext != lastExt {
										group = byExt[ext]
										if group == nil {
											extension := strings.ToLower(ext)
											if extension == "" {
												extension = "(no extension)"
											}
											group = groups[extension]
											if group == nil {
												group = &FileType{Extension: extension}
												groups[extension] = group
											}
											byExt[ext] = group
										}
										last, lastExt = group, ext
									}
									group.Bytes += node.Size
									group.Allocated += node.Allocated
									group.Count++
									if limit > 0 {
										val := node.Size
										if allocated {
											val = node.Allocated
										}
										if top.Len() < limit {
											heap.Push(top, current)
										} else {
											minID := top.ids[0]
											minNode := &s.Nodes[minID]
											minVal := minNode.Size
											if allocated {
												minVal = minNode.Allocated
											}
											if val > minVal || (val == minVal && current > minID) {
												top.ids[0] = current
												heap.Fix(top, 0)
											}
										}
									}
								}
								if child := node.FirstChild; descend && child != None {
									current = child
									continue
								}
								for current != sib && s.Nodes[current].NextSibling == None {
									current = s.Nodes[current].Parent
								}
								if current == sib {
									break
								}
								current = s.Nodes[current].NextSibling
							}
							if sib == p.last {
								break
							}
						}
					}
					partials[workerIdx] = groups
					if limit > 0 {
						partialTops[workerIdx] = top
					}
				}(w)
			}
			wg.Wait()
			for _, err := range errs {
				if err != nil {
					return Analysis{}, err
				}
			}
			merged := make(map[string]*FileType)
			for _, partial := range partials {
				for ext, pt := range partial {
					g := merged[ext]
					if g == nil {
						g = &FileType{Extension: ext}
						merged[ext] = g
					}
					g.Bytes += pt.Bytes
					g.Allocated += pt.Allocated
					g.Count += pt.Count
				}
			}
			for _, g := range merged {
				result.Types = append(result.Types, *g)
			}
			if limit > 0 {
				top := &largestHeap{snapshot: s, allocated: allocated}
				for _, pTop := range partialTops {
					if pTop == nil {
						continue
					}
					for _, id := range pTop.ids {
						val := s.Nodes[id].Size
						if allocated {
							val = s.Nodes[id].Allocated
						}
						if top.Len() < limit {
							heap.Push(top, id)
						} else {
							minID := top.ids[0]
							minNode := &s.Nodes[minID]
							minVal := minNode.Size
							if allocated {
								minVal = minNode.Allocated
							}
							if val > minVal || (val == minVal && id > minID) {
								top.ids[0] = id
								heap.Fix(top, 0)
							}
						}
					}
				}
				result.TopFiles = top.ids
				s.Sort(result.TopFiles, Sort{Allocated: allocated, Desc: true})
			}
			s.sortAnalysisTypes(result.Types, allocated)
			return result, nil
		}
	}

	groups := make(map[string]*FileType)
	byExt := make(map[string]*FileType)
	var last *FileType
	var lastExt string
	top := &largestHeap{snapshot: s, allocated: allocated}

	current, count := root, 0
	for {
		if count&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return Analysis{}, err
			}
		}
		count++
		node := &s.Nodes[current]
		descend := true
		if hasMetadataExclusion && current != 0 && node.Parent == 0 && strings.HasPrefix(s.NameView(current), "$") {
			descend = false
		} else if !node.IsDir() {
			ext := fastExt(s.NameView(current))
			group := last
			if group == nil || ext != lastExt {
				group = byExt[ext]
				if group == nil {
					extension := strings.ToLower(ext)
					if extension == "" {
						extension = "(no extension)"
					}
					group = groups[extension]
					if group == nil {
						group = &FileType{Extension: extension}
						groups[extension] = group
					}
					byExt[ext] = group
				}
				last, lastExt = group, ext
			}
			group.Bytes += node.Size
			group.Allocated += node.Allocated
			group.Count++
			if limit > 0 {
				val := node.Size
				if allocated {
					val = node.Allocated
				}
				if top.Len() < limit {
					heap.Push(top, current)
				} else {
					minID := top.ids[0]
					minNode := &s.Nodes[minID]
					minVal := minNode.Size
					if allocated {
						minVal = minNode.Allocated
					}
					if val > minVal || (val == minVal && current > minID) {
						top.ids[0] = current
						heap.Fix(top, 0)
					}
				}
			}
		}

		if child := node.FirstChild; descend && child != None {
			current = child
			continue
		}
		for current != root && s.Nodes[current].NextSibling == None {
			current = s.Nodes[current].Parent
		}
		if current == root {
			break
		}
		current = s.Nodes[current].NextSibling
	}

	for _, group := range groups {
		result.Types = append(result.Types, *group)
	}
	result.TopFiles = top.ids
	s.Sort(result.TopFiles, Sort{Allocated: allocated, Desc: true})
	s.sortAnalysisTypes(result.Types, allocated)
	return result, nil
}

func (s *Snapshot) sortAnalysisTypes(types []FileType, allocated bool) {
	slices.SortFunc(types, func(first, second FileType) int {
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
}
