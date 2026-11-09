package desktop

import (
	"math/rand/v2"
	"slices"
	"testing"

	"quarry/internal/index"
)

// referenceGroupDuplicates is the former map-based implementation.
func referenceGroupDuplicates(ids []index.ID, groups map[index.ID]int32) ([]index.ID, []uint32) {
	rank := map[int32]int{}
	var lens []uint32
	for _, id := range ids {
		g := groups[id]
		r, ok := rank[g]
		if !ok {
			r = len(lens)
			rank[g] = r
			lens = append(lens, 0)
		}
		lens[r]++
	}
	var buckets [][]index.ID = make([][]index.ID, len(lens))
	for _, id := range ids {
		r := rank[groups[id]]
		buckets[r] = append(buckets[r], id)
	}
	var order []index.ID
	for _, b := range buckets {
		order = append(order, b...)
	}
	return order, lens
}

func TestGroupDuplicatesMatchesReference(t *testing.T) {
	random := rand.New(rand.NewPCG(11, 12))
	for trial := range 400 {
		n := random.IntN(300)
		groupCount := 1 + random.IntN(40)
		groups := map[index.ID]int32{}
		ids := make([]index.ID, n)
		for i := range ids {
			ids[i] = index.ID(i*3 + 1)
			if random.IntN(10) > 0 { // some files carry no group (0)
				groups[ids[i]] = int32(1 + random.IntN(groupCount))
			}
		}
		if trial%3 == 0 {
			// Already grouped: sorted by group, stable by position.
			slices.SortStableFunc(ids, func(x, y index.ID) int { return int(groups[x] - groups[y]) })
		} else {
			random.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		}
		input := slices.Clone(ids)
		gotOrder, gotLens := groupDuplicates(ids, groups)
		wantOrder, wantLens := referenceGroupDuplicates(ids, groups)
		if !slices.Equal(gotLens, wantLens) || !slices.Equal(gotOrder, wantOrder) && !(len(gotOrder) == 0 && len(wantOrder) == 0) {
			t.Fatalf("trial %d: got %v %v, want %v %v", trial, gotOrder, gotLens, wantOrder, wantLens)
		}
		if !slices.Equal(ids, input) {
			t.Fatalf("trial %d: input changed", trial)
		}
	}
}
