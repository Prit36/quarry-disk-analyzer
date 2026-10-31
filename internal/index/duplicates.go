package index

import (
	"cmp"
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

const (
	duplicatePrefix   = 4 << 10  // bytes per region of a large-file sample
	duplicateFullRead = 64 << 10 // small files cost less to read in one pass
)

type DuplicateGroup struct {
	IDs  []ID
	Size uint64
}
type DuplicateResult struct {
	Groups           []DuplicateGroup
	Issues           []Issue
	Checked, Skipped uint64
}
type DuplicateProgress struct {
	Files, Bytes, TotalBytes atomic.Uint64 // logical progress, including ruled-out candidates
}
type hashedFile struct {
	id   ID
	info os.FileInfo
}

type duplicateIdentity [3]uint32 // volume serial number and 64-bit file index

// hashOutcome is the result of hashing one candidate. ok is false when the
// file was skipped; issue explains why it could not be verified.
type hashOutcome struct {
	digest      [32]byte
	info        os.FileInfo
	ok          bool
	issue       *Issue
	err         error
	bytes       uint64 // bytes read during this hash
	identity    duplicateIdentity
	hasIdentity bool
}

func (s *Snapshot) FindDuplicates(ctx context.Context, root ID, progress *DuplicateProgress) (DuplicateResult, error) {
	return s.FindDuplicatesOver(ctx, root, 0, progress)
}

// FindDuplicatesOver confirms same-size files using full SHA-256. Files up to
// 64 KiB are read once. Larger files are sampled at the start, middle and end;
// only matching samples proceed to a full hash. Samples never prove equality.
// Batches span size groups, keeping workers busy even with many tiny pairs.
func (s *Snapshot) FindDuplicatesOver(ctx context.Context, root ID, minSize uint64, progress *DuplicateProgress) (DuplicateResult, error) {
	result := DuplicateResult{}
	if progress != nil {
		progress.Files.Store(0)
		progress.Bytes.Store(0)
		progress.TotalBytes.Store(0)
	}
	candidates, err := s.duplicateCandidates(ctx, root, minSize)
	if err != nil {
		return result, err
	}
	// Discard unique sizes without opening them. Compact in place and retain
	// ascending IDs within each size for deterministic hard-link selection.
	kept := candidates[:0]
	var total uint64
	for start := 0; start < len(candidates); {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		end := start + 1
		size := s.Nodes[candidates[start]].Size
		for end < len(candidates) && s.Nodes[candidates[end]].Size == size {
			if end&4095 == 0 && ctx.Err() != nil {
				return result, ctx.Err()
			}
			end++
		}
		if end-start > 1 {
			kept = append(kept, candidates[start:end]...)
			total += size * uint64(end-start)
		}
		start = end
	}
	candidates = kept
	if progress != nil {
		progress.TotalBytes.Store(total)
	}
	buffers := make([][]byte, min(8, runtime.GOMAXPROCS(0)))
	// Never split a size group across batches: all possible matches for a
	// given size must be compared together. Normal batches bound FileInfo
	// and digest scratch to a few thousand files instead of the whole scan.
	for start := 0; start < len(candidates); {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		end := min(start+4096, len(candidates))
		for end < len(candidates) && s.Nodes[candidates[end]].Size == s.Nodes[candidates[end-1]].Size {
			if end&4095 == 0 && ctx.Err() != nil {
				return result, ctx.Err()
			}
			end++
		}
		if err := s.verifyDuplicateBatch(ctx, candidates[start:end], progress, buffers, &result); err != nil {
			return result, err
		}
		start = end
	}
	sortDuplicateGroups(result.Groups)
	return result, ctx.Err()
}

type duplicateDigest struct {
	size   uint64
	digest [32]byte
}

func (s *Snapshot) verifyDuplicateBatch(ctx context.Context, ids []ID, progress *DuplicateProgress, buffers [][]byte, result *DuplicateResult) error {
	groups := make(map[duplicateDigest][]hashedFile)
	identities := make(map[duplicateIdentity]struct{})
	unknownIdentities := make(map[duplicateDigest][]os.FileInfo)
	count := func() {
		result.Checked++
		if progress != nil {
			progress.Files.Add(1)
		}
	}
	add := func(id ID, outcome hashOutcome) {
		key := duplicateDigest{s.Nodes[id].Size, outcome.digest}
		alias := false
		if outcome.hasIdentity {
			_, alias = identities[outcome.identity]
			if !alias {
				for _, info := range unknownIdentities[key] {
					if os.SameFile(info, outcome.info) {
						alias = true
						break
					}
				}
			}
		} else {
			for _, existing := range groups[key] {
				if os.SameFile(existing.info, outcome.info) {
					alias = true
					break
				}
			}
		}
		if !alias {
			groups[key] = append(groups[key], hashedFile{id: id, info: outcome.info})
			if outcome.hasIdentity {
				identities[outcome.identity] = struct{}{}
			} else {
				unknownIdentities[key] = append(unknownIdentities[key], outcome.info)
			}
		}
		count()
	}
	settle := func(batch []ID, outcomes []hashOutcome, sampled bool, verified func(int)) error {
		for i, outcome := range outcomes {
			if i&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			if outcome.err != nil {
				return outcome.err
			}
			if outcome.issue != nil && len(result.Issues) < 100 {
				result.Issues = append(result.Issues, *outcome.issue)
			}
			if !outcome.ok {
				result.Skipped++
				if progress != nil {
					size, read := s.Nodes[batch[i]].Size, outcome.bytes
					if sampled && size > duplicateFullRead {
						read = 0
					}
					progress.Bytes.Add(size - read)
				}
				continue
			}
			verified(i)
		}
		return nil
	}
	outcomes := s.hashCandidates(ctx, ids, true, progress, buffers)
	samples := make(map[duplicateDigest][]ID)
	if err := settle(ids, outcomes, true, func(i int) {
		id := ids[i]
		if size := s.Nodes[id].Size; size <= duplicateFullRead {
			add(id, outcomes[i])
		} else {
			key := duplicateDigest{size, outcomes[i].digest}
			samples[key] = append(samples[key], id)
		}
	}); err != nil {
		return err
	}
	var pending []ID
	for key, same := range samples {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(same) == 1 {
			count()
			if progress != nil {
				progress.Bytes.Add(key.size)
			}
		} else {
			pending = append(pending, same...)
		}
	}
	slices.Sort(pending)
	outcomes = s.hashCandidates(ctx, pending, false, progress, buffers)
	if err := settle(pending, outcomes, false, func(i int) { add(pending[i], outcomes[i]) }); err != nil {
		return err
	}
	for key, matches := range groups {
		if len(matches) < 2 {
			continue
		}
		group := DuplicateGroup{Size: key.size, IDs: make([]ID, len(matches))}
		for i, match := range matches {
			group.IDs[i] = match.id
		}
		slices.Sort(group.IDs)
		result.Groups = append(result.Groups, group)
	}
	return ctx.Err()
}

// A stable order makes group numbers and the representative hard-link path
// independent of map iteration and worker completion order.
func sortDuplicateGroups(groups []DuplicateGroup) {
	slices.SortFunc(groups, func(a, b DuplicateGroup) int {
		if c := cmp.Compare(b.Size, a.Size); c != 0 {
			return c
		}
		return cmp.Compare(a.IDs[0], b.IDs[0])
	})
}

// Keep one compact ID array instead of a map and a separately allocated slice
// for every distinct size. The existing cancellable radix sorter groups equal
// sizes and gives aliases a deterministic ID order before content verification.
func (s *Snapshot) duplicateCandidates(ctx context.Context, root ID, minSize uint64) ([]ID, error) {
	ids, err := s.unsortedDuplicateCandidates(ctx, root, minSize)
	if err == nil {
		err = s.SortContext(ctx, ids, Sort{Column: "Size"})
	}
	return ids, err
}

// Metadata grouping has no use for a size sort. Content verification keeps
// the sorted variant so unique sizes never reach the filesystem.
func (s *Snapshot) unsortedDuplicateCandidates(ctx context.Context, root ID, minSize uint64) ([]ID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == None || int(root) >= len(s.Nodes) {
		return nil, fmt.Errorf("invalid scope")
	}
	for parent := s.Nodes[root].Parent; parent != None; parent = s.Nodes[parent].Parent {
		if s.Nodes[parent].Flags&(Unplaced|Reparse) != 0 {
			return nil, nil
		}
	}
	ids := make([]ID, 0, int(min(uint64(s.Nodes[root].Files), uint64(len(s.Nodes)))))
	err := s.walk(ctx, root, func(id ID) (bool, error) {
		node := &s.Nodes[id]
		if node.IsDir() && node.Flags&(Reparse|Unplaced) != 0 {
			return false, nil // descendants have no reliable ordinary file path
		}
		if !node.IsDir() && node.Size >= minSize && node.Flags&(Reparse|Shared|Unplaced|Incomplete) == 0 {
			ids = append(ids, id)
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// Hash workers share a batch across sizes. Each owns reusable scratch;
// sampling uses 4 KiB, while a full hash grows scratch only up to 64 KiB.
func (s *Snapshot) hashCandidates(ctx context.Context, ids []ID, sample bool, progress *DuplicateProgress, buffers [][]byte) []hashOutcome {
	if len(ids) == 0 {
		return nil
	}
	outcomes := make([]hashOutcome, len(ids))
	var next atomic.Int64
	var wg sync.WaitGroup
	work := func(w int) {
		buffer := buffers[w]
		defer func() { buffers[w] = buffer }()
		for i := int(next.Add(1)) - 1; i < len(ids); i = int(next.Add(1)) - 1 {
			if err := ctx.Err(); err != nil {
				outcomes[i].err = err
				return
			}
			limit := s.Nodes[ids[i]].Size
			sampled := sample && limit > duplicateFullRead
			if sampled {
				limit = duplicatePrefix
			}
			needed := int(max(1, min(limit, 64<<10)))
			if cap(buffer) < needed {
				buffer = make([]byte, max(duplicatePrefix, needed))
			} else {
				buffer = buffer[:needed]
			}
			outcomes[i] = s.readDuplicateCandidate(ctx, ids[i], limit, buffer, progress, sampled)
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
	return s.readDuplicateCandidate(ctx, id, limit, buffer, progress, false)
}

func (s *Snapshot) readDuplicateCandidate(ctx context.Context, id ID, limit uint64, buffer []byte, progress *DuplicateProgress, sample bool) hashOutcome {
	if err := ctx.Err(); err != nil {
		return hashOutcome{err: err}
	}
	path := s.Path(id)
	skipped := func(message string, bytes uint64) hashOutcome {
		return hashOutcome{issue: &Issue{Path: path, Message: message}, bytes: bytes}
	}
	before, err := os.Lstat(path)
	if err != nil {
		return skipped(err.Error(), 0)
	}
	if !before.Mode().IsRegular() {
		return skipped("not a regular file", 0)
	}
	file, err := os.Open(path)
	if err != nil {
		return skipped(err.Error(), 0)
	}
	defer file.Close()
	opened, err := file.Stat()
	node := s.Nodes[id]
	if err != nil {
		return skipped(err.Error(), 0)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() < 0 || uint64(opened.Size()) != node.Size || (node.Modified != 0 && unixSeconds(opened.ModTime().Unix()) != node.Modified) {
		return skipped("file changed since the scan; rescan to include it", 0)
	}
	hash := sha256.New()
	type region struct{ offset, length uint64 }
	regions := [3]region{{length: limit}}
	expected := limit
	if sample {
		regions[1] = region{(node.Size - duplicatePrefix) / 2, duplicatePrefix}
		regions[2] = region{node.Size - duplicatePrefix, duplicatePrefix}
		expected += 2 * duplicatePrefix
	}
	var readBytes uint64
readRegions:
	for _, region := range regions {
		if region.length == 0 {
			continue
		}
		if region.offset > 0 {
			if _, err = file.Seek(int64(region.offset), io.SeekStart); err != nil {
				break
			}
		}
		for remaining := region.length; remaining > 0; {
			if err = ctx.Err(); err != nil {
				break readRegions
			}
			chunk := buffer[:min(uint64(len(buffer)), remaining)]
			var count int
			count, err = file.Read(chunk)
			if count > 0 {
				_, _ = hash.Write(chunk[:count])
				readBytes += uint64(count)
				remaining -= uint64(count)
				if progress != nil {
					if !sample {
						progress.Bytes.Add(uint64(count))
					}
				}
			}
			if err == io.EOF {
				err = nil
				break readRegions
			}
			if err != nil {
				break readRegions
			}
		}
	}
	after, statErr := file.Stat()
	if ctx.Err() != nil {
		return hashOutcome{err: ctx.Err()}
	}
	if err != nil {
		return skipped(err.Error(), readBytes)
	}
	if statErr != nil {
		return skipped(statErr.Error(), readBytes)
	}
	if after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || readBytes != expected {
		return skipped("file changed during duplicate verification", readBytes)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return skipped(err.Error(), readBytes)
	}
	if !os.SameFile(opened, current) || !current.Mode().IsRegular() || current.Size() != opened.Size() || !current.ModTime().Equal(opened.ModTime()) {
		return skipped("file was replaced during duplicate verification", readBytes)
	}
	var digest [32]byte
	hash.Sum(digest[:0])
	identity, hasIdentity := readDuplicateIdentity(file)
	return hashOutcome{digest: digest, info: opened, ok: true, bytes: readBytes, identity: identity, hasIdentity: hasIdentity}
}
