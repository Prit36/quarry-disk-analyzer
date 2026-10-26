package index

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

// duplicatePrefix is how much of each same-size candidate is hashed before
// the rest: files that differ near their start are never read in full.
const duplicatePrefix = 4 << 10

type DuplicateGroup struct {
	IDs  []ID
	Size uint64
}
type DuplicateResult struct {
	Groups           []DuplicateGroup
	Issues           []Issue
	Checked, Skipped uint64
}
type DuplicateProgress struct{ Files, Bytes, TotalBytes atomic.Uint64 }
type hashedFile struct {
	id   ID
	info os.FileInfo
}

// hashOutcome is the result of hashing one candidate. ok is false when the
// file was skipped; issue is set when it could not be opened.
type hashOutcome struct {
	digest [32]byte
	info   os.FileInfo
	ok     bool
	issue  *Issue
	err    error
}

func (s *Snapshot) FindDuplicates(ctx context.Context, root ID, progress *DuplicateProgress) (DuplicateResult, error) {
	return s.FindDuplicatesOver(ctx, root, 1, progress)
}

// FindDuplicatesOver verifies duplicates among files of at least minSize
// bytes: same-size candidates are confirmed by a full SHA-256 of the content,
// and only those that also agree on their first 4 KiB are read in full.
// Hashing runs on a few workers; equal-size candidates are ordered by ID.
func (s *Snapshot) FindDuplicatesOver(ctx context.Context, root ID, minSize uint64, progress *DuplicateProgress) (DuplicateResult, error) {
	result := DuplicateResult{}
	candidates, err := s.duplicateCandidates(ctx, root, max(minSize, 1))
	if err != nil {
		return result, err
	}
	if progress != nil {
		var total uint64
		for i, id := range candidates {
			size := s.Nodes[id].Size
			if i > 0 && s.Nodes[candidates[i-1]].Size == size || i+1 < len(candidates) && s.Nodes[candidates[i+1]].Size == size {
				total += size
			}
		}
		progress.TotalBytes.Store(total)
	}
	// Scratch belongs to this verification, rather than each size/prefix
	// group. Thousands of small groups must not each allocate 64 KiB per
	// worker; prefix reads need only 4 KiB and empty groups need none.
	buffers := make([][]byte, min(8, runtime.NumCPU()))
	for start := 0; start < len(candidates); {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		size := s.Nodes[candidates[start]].Size
		end := start + 1
		for end < len(candidates) && s.Nodes[candidates[end]].Size == size {
			if end&4095 == 0 && ctx.Err() != nil {
				return result, ctx.Err()
			}
			end++
		}
		ids := candidates[start:end]
		start = end
		if len(ids) < 2 {
			continue
		}
		groups := make(map[[32]byte][]hashedFile)
		count := func() {
			result.Checked++
			if progress != nil {
				progress.Files.Add(1)
			}
		}
		add := func(id ID, outcome hashOutcome) {
			alias := false
			for _, existing := range groups[outcome.digest] {
				if os.SameFile(existing.info, outcome.info) {
					alias = true
					break
				}
			}
			if !alias {
				groups[outcome.digest] = append(groups[outcome.digest], hashedFile{id: id, info: outcome.info})
			}
			count()
		}
		// settle counts outcomes in order and reports verified ones to
		// verified; the first cancellation error stops it.
		settle := func(outcomes []hashOutcome, verified func(int)) error {
			for i, outcome := range outcomes {
				if outcome.err != nil {
					return outcome.err
				}
				if outcome.issue != nil && len(result.Issues) < 100 {
					result.Issues = append(result.Issues, *outcome.issue)
				}
				if !outcome.ok {
					result.Skipped++
					continue
				}
				verified(i)
			}
			return nil
		}
		if size <= duplicatePrefix {
			outcomes := s.hashCandidates(ctx, ids, size, progress, buffers)
			if err := settle(outcomes, func(i int) { add(ids[i], outcomes[i]) }); err != nil {
				return result, err
			}
		} else {
			// A file whose prefix no other candidate shares cannot have a
			// duplicate, so it is never read further.
			// Prefix reads are not progress: the bytes counted are the
			// candidates' full sizes, read in full or ruled out.
			prefixOutcomes := s.hashCandidates(ctx, ids, duplicatePrefix, nil, buffers)
			prefixes := make(map[[32]byte][]ID)
			if err := settle(prefixOutcomes, func(i int) {
				prefixes[prefixOutcomes[i].digest] = append(prefixes[prefixOutcomes[i].digest], ids[i])
			}); err != nil {
				return result, err
			}
			var pending []ID
			for _, same := range prefixes {
				if len(same) == 1 {
					count()
					if progress != nil {
						progress.Bytes.Add(size)
					}
					continue
				}
				pending = append(pending, same...)
			}
			outcomes := s.hashCandidates(ctx, pending, size, progress, buffers)
			if err := settle(outcomes, func(i int) { add(pending[i], outcomes[i]) }); err != nil {
				return result, err
			}
		}
		for _, matches := range groups {
			if len(matches) < 2 {
				continue
			}
			group := DuplicateGroup{Size: s.Nodes[matches[0].id].Size}
			for _, match := range matches {
				group.IDs = append(group.IDs, match.id)
			}
			result.Groups = append(result.Groups, group)
		}
	}
	slices.SortFunc(result.Groups, func(first, second DuplicateGroup) int {
		if first.Size > second.Size {
			return -1
		}
		if first.Size < second.Size {
			return 1
		}
		return 0
	})
	return result, nil
}

// Keep one compact ID array instead of a map and a separately allocated slice
// for every distinct size. The existing cancellable radix sorter groups equal
// sizes and gives aliases a deterministic ID order before content verification.
func (s *Snapshot) duplicateCandidates(ctx context.Context, root ID, minSize uint64) ([]ID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == None || int(root) >= len(s.Nodes) {
		return nil, fmt.Errorf("invalid scope")
	}
	ids := make([]ID, 0, int(min(uint64(s.Nodes[root].Files), uint64(len(s.Nodes)))))
	err := s.Visit(ctx, root, func(id ID) error {
		node := &s.Nodes[id]
		if !node.IsDir() && node.Size >= minSize && node.Flags&(Reparse|Shared|Unplaced|Incomplete) == 0 {
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.SortContext(ctx, ids, Sort{Column: "Size"}); err != nil {
		return nil, err
	}
	return ids, nil
}

// hashCandidates hashes the first limit bytes of each candidate, or all of
// it when limit is its size, on up to eight workers. Calls sharing buffers
// are sequential; each worker owns one scratch slot for the duration of a call.
func (s *Snapshot) hashCandidates(ctx context.Context, ids []ID, limit uint64, progress *DuplicateProgress, buffers [][]byte) []hashOutcome {
	if len(ids) == 0 {
		return nil
	}
	outcomes := make([]hashOutcome, len(ids))
	var next atomic.Int64
	var wg sync.WaitGroup
	work := func(w int) {
		needed := int(min(limit, 64<<10))
		buffer := buffers[w]
		if cap(buffer) < needed {
			buffer = make([]byte, needed)
		} else {
			buffer = buffer[:needed]
		}
		buffers[w] = buffer
		for i := int(next.Add(1)) - 1; i < len(ids); i = int(next.Add(1)) - 1 {
			if err := ctx.Err(); err != nil {
				outcomes[i].err = err
				return
			}
			outcomes[i] = s.hashCandidate(ctx, ids[i], limit, buffer, progress)
		}
	}
	for w := 1; w < min(len(ids), len(buffers)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			work(w)
		}()
	}
	work(0)
	wg.Wait()
	return outcomes
}

// hashCandidate hashes the first limit bytes of a candidate, or all of it
// when limit is its size, after checking that the file on disk is still the
// indexed one.
func (s *Snapshot) hashCandidate(ctx context.Context, id ID, limit uint64, buffer []byte, progress *DuplicateProgress) hashOutcome {
	if err := ctx.Err(); err != nil {
		return hashOutcome{err: err}
	}
	path := s.Path(id)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return hashOutcome{}
	}
	file, err := os.Open(path)
	if err != nil {
		return hashOutcome{issue: &Issue{Path: path, Message: err.Error()}}
	}
	opened, err := file.Stat()
	node := s.Nodes[id]
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() < 0 || uint64(opened.Size()) != node.Size || (node.Modified != 0 && opened.ModTime().Unix() != node.ModTime()) {
		file.Close()
		return hashOutcome{}
	}
	hash := sha256.New()
	var readBytes uint64
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		chunk := buffer
		if limit < node.Size {
			if readBytes == limit {
				break
			}
			chunk = buffer[:min(uint64(len(buffer)), limit-readBytes)]
		}
		var count int
		count, err = file.Read(chunk)
		if count > 0 {
			_, _ = hash.Write(chunk[:count])
			readBytes += uint64(count)
			if progress != nil {
				progress.Bytes.Add(uint64(count))
			}
		}
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			break
		}
		if readBytes > node.Size {
			err = fmt.Errorf("file grew during duplicate verification")
			break
		}
	}
	after, statErr := file.Stat()
	file.Close()
	if ctx.Err() != nil {
		return hashOutcome{err: ctx.Err()}
	}
	if err != nil || statErr != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || readBytes != limit {
		return hashOutcome{}
	}
	var digest [32]byte
	hash.Sum(digest[:0])
	return hashOutcome{digest: digest, info: opened, ok: true}
}
