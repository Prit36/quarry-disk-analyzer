package index

import (
	"context"
	"strings"
)

// searchFilePiece collects an unrestricted file list in the same depth-first
// order as Search. Metadata ancestry follows the walk, requiring neither a
// per-node bitset nor atomic operations between disjoint pieces.
func (s *Snapshot) searchFilePiece(ctx context.Context, first, last, skip ID, ids []ID, tally *searchTally) ([]ID, error) {
	nodes := s.Nodes
	inherited := s.IsMetadata(nodes[first].Parent)
	if first == skip {
		inherited = s.IsMetadata(skip)
	}
	mft := strings.Contains(s.Engine, "MFT")
	boundary := None
	top, current, count := first, first, 0
	for {
		if count&4095 == 0 && ctx.Err() != nil {
			return ids, ctx.Err()
		}
		count++
		n := &nodes[current]
		if current != skip {
			if !tally.metadata && !inherited && boundary == None &&
				(n.Flags&Unplaced != 0 || mft && n.Parent == 0 && isMetadataName(s.NameView(current))) {
				boundary = current
			}
			if !n.IsDir() {
				ids = append(ids, current)
				if tally.allocated {
					tally.total += n.Allocated
				} else {
					tally.total += n.Size
				}
				tally.metadata = tally.metadata || inherited || boundary != None
			}
		}
		if child := n.FirstChild; child != None {
			current = child
			continue
		}
		for {
			if current == boundary {
				boundary = None
			}
			if current == top {
				if top == last {
					return ids, ctx.Err()
				}
				top = nodes[top].NextSibling
				current = top
				break
			}
			if next := nodes[current].NextSibling; next != None {
				current = next
				break
			}
			current = nodes[current].Parent
		}
	}
}
