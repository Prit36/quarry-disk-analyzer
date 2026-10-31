package index

import (
	"context"
	"hash/maphash"
	"runtime"
	"slices"
	"strings"
)

type duplicateKey struct {
	hash uint32
	id   ID
}

// DuplicateCandidatesOver is a metadata-only name/size search. It uses the
// same scope and eligibility rules as content verification, without sorting
// unrelated files or opening any files.
func (s *Snapshot) DuplicateCandidatesOver(ctx context.Context, root ID, minSize uint64) (DuplicateResult, error) {
	ids, err := s.unsortedDuplicateCandidates(ctx, root, minSize)
	if err != nil {
		return DuplicateResult{}, err
	}
	if len(ids) < 2 {
		return DuplicateResult{}, ctx.Err()
	}
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<15)))
	const partitionBits, partitions = 6, 1 << 6
	seed := maphash.MakeSeed()
	keys := make([]duplicateKey, len(ids))
	counts := make([][partitions]int, workers)
	runSortWorkers(len(ids), workers, func(w, start, end int) {
		var scratch []byte
		for i := start; i < end; i++ {
			if i&4095 == 0 && ctx.Err() != nil {
				return
			}
			id := ids[i]
			name := s.NameView(id)
			var ascii bool
			scratch, ascii = AppendFoldedASCII(scratch[:0], name)
			var hash uint64
			if ascii {
				hash = maphash.Bytes(seed, scratch)
			} else {
				hash = maphash.String(seed, strings.ToLower(name))
			}
			// The fingerprint only routes candidates. Exact name/size checks
			// below resolve collisions, including full fingerprint collisions.
			hash ^= s.Nodes[id].Size * 0x9e3779b97f4a7c15
			fingerprint := uint32(hash ^ (hash >> 32))
			keys[i] = duplicateKey{fingerprint, id}
			counts[w][fingerprint&(partitions-1)]++
		}
	})
	if err := ctx.Err(); err != nil {
		return DuplicateResult{}, err
	}
	if workers == 1 {
		groups, err := s.groupDuplicateKeys(ctx, keys, 0, ids)
		sortDuplicateGroups(groups)
		return DuplicateResult{Groups: groups}, err
	}
	// Route equal keys to the same small table. Prefix sums give each worker
	// disjoint destinations; no locks or atomics are needed per file. Tables
	// fit in cache instead of probing a million-entry map through node memory.
	var offsets [partitions + 1]int
	for p := range partitions {
		position := offsets[p]
		for w := range workers {
			n := counts[w][p]
			counts[w][p] = position
			position += n
		}
		offsets[p+1] = position
	}
	partitioned := make([]duplicateKey, len(keys))
	runSortWorkers(len(keys), workers, func(w, start, end int) {
		positions := &counts[w]
		for i := start; i < end; i++ {
			if i&4095 == 0 && ctx.Err() != nil {
				return
			}
			key := keys[i]
			p := key.hash & (partitions - 1)
			partitioned[positions[p]] = key
			positions[p]++
		}
	})
	if err := ctx.Err(); err != nil {
		return DuplicateResult{}, err
	}
	var grouped [partitions][]DuplicateGroup
	runSortWorkers(partitions, workers, func(_, start, end int) {
		for p := start; p < end; p++ {
			if ctx.Err() != nil {
				return
			}
			grouped[p], _ = s.groupDuplicateKeys(ctx, partitioned[offsets[p]:offsets[p+1]], partitionBits, ids[offsets[p]:offsets[p+1]])
		}
	})
	if err := ctx.Err(); err != nil {
		return DuplicateResult{}, err
	}
	n := 0
	for _, groups := range grouped {
		n += len(groups)
	}
	result := DuplicateResult{}
	if n > 0 {
		result.Groups = make([]DuplicateGroup, 0, n)
		for _, groups := range grouped {
			result.Groups = append(result.Groups, groups...)
		}
		sortDuplicateGroups(result.Groups)
	}
	return result, ctx.Err()
}

// Slots hold one-based indices into keys, with equal keys chained through
// links. Singletons need no strings or individual slices. Only duplicate
// groups receive output IDs, backed by one arena per partition.
func (s *Snapshot) groupDuplicateKeys(ctx context.Context, keys []duplicateKey, partitionBits uint, links []ID) ([]DuplicateGroup, error) {
	if len(keys) < 2 {
		return nil, ctx.Err()
	}
	capacity := 16
	for capacity < len(keys)+len(keys)/2 {
		capacity *= 2
	}
	slots := make([]uint32, capacity)
	// Collection IDs are no longer needed after partitioning. Reuse their
	// storage for chains; workers own disjoint ranges of that storage.
	clear(links)
	var duplicateSlots []uint32
	matched := 0
	mask := uint32(capacity - 1)
	for i, key := range keys {
		if i&4095 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for slot := (key.hash >> partitionBits) & mask; ; slot = (slot + 1) & mask {
			previous := slots[slot]
			if previous == 0 {
				slots[slot] = uint32(i + 1)
				break
			}
			other := keys[previous-1]
			if other.hash != key.hash || s.Nodes[other.id].Size != s.Nodes[key.id].Size || compareFold(s.NameView(other.id), s.NameView(key.id)) != 0 {
				continue
			}
			if links[previous-1] == 0 {
				duplicateSlots = append(duplicateSlots, slot)
				matched++ // the original singleton now belongs to a group
			}
			matched++
			links[i] = ID(previous)
			slots[slot] = uint32(i + 1)
			break
		}
	}
	if len(duplicateSlots) == 0 {
		return nil, ctx.Err()
	}
	groups := make([]DuplicateGroup, len(duplicateSlots))
	arena := make([]ID, matched)
	start := 0
	for g, slot := range duplicateSlots {
		end := start
		for current := slots[slot]; current != 0; current = uint32(links[current-1]) {
			if end&4095 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			arena[end] = keys[current-1].id
			end++
		}
		matches := arena[start:end:end]
		slices.Sort(matches)
		groups[g] = DuplicateGroup{IDs: matches, Size: s.Nodes[matches[0]].Size}
		start = end
	}
	return groups, ctx.Err()
}
