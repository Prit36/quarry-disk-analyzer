package index

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"slices"
	"sync/atomic"
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
type DuplicateProgress struct{ Files, Bytes atomic.Uint64 }
type hashedFile struct {
	id   ID
	info os.FileInfo
}

func (s *Snapshot) FindDuplicates(ctx context.Context, root ID, progress *DuplicateProgress) (DuplicateResult, error) {
	result := DuplicateResult{}
	candidates := make(map[uint64][]ID)
	err := s.Visit(ctx, root, func(id ID) error {
		node := s.Nodes[id]
		if !node.IsDir() && node.Size > 0 && node.Flags&(Reparse|Shared|Unplaced|Incomplete) == 0 {
			candidates[node.Size] = append(candidates[node.Size], id)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	buffer := make([]byte, 64<<10)
	for _, ids := range candidates {
		if len(ids) < 2 {
			continue
		}
		groups := make(map[[32]byte][]hashedFile)
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			path := s.Path(id)
			before, err := os.Lstat(path)
			if err != nil || !before.Mode().IsRegular() {
				result.Skipped++
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				result.Skipped++
				if len(result.Issues) < 100 {
					result.Issues = append(result.Issues, Issue{Path: path, Message: err.Error()})
				}
				continue
			}
			opened, err := file.Stat()
			node := s.Nodes[id]
			if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() < 0 || uint64(opened.Size()) != node.Size || (node.Modified != 0 && opened.ModTime().Unix() != node.Modified) {
				file.Close()
				result.Skipped++
				continue
			}
			hash := sha256.New()
			var readBytes uint64
			for {
				if err = ctx.Err(); err != nil {
					break
				}
				var count int
				count, err = file.Read(buffer)
				if count > 0 {
					_, _ = hash.Write(buffer[:count])
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
				return result, ctx.Err()
			}
			if err != nil || statErr != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || readBytes != node.Size {
				result.Skipped++
				continue
			}
			var digest [32]byte
			copy(digest[:], hash.Sum(nil))
			alias := false
			for _, existing := range groups[digest] {
				if os.SameFile(existing.info, opened) {
					alias = true
					break
				}
			}
			if !alias {
				groups[digest] = append(groups[digest], hashedFile{id: id, info: opened})
			}
			result.Checked++
			if progress != nil {
				progress.Files.Add(1)
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
