package index

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const SnapshotLimit = 128 << 20

var snapshotMagic = [8]byte{'S', 'T', 'R', 'A', 'T', 'U', 'M', 1}

type SnapshotInfo struct {
	Path                                        string
	Root, Engine, Fallback                      string
	Started                                     time.Time
	Elapsed                                     time.Duration
	Cancelled                                   bool
	Errors, SkippedLinks, HardLinks, Unresolved uint64
	Issues                                      []Issue
	Exclusions                                  []string
	Excluded                                    uint64
	Total                                       Node
}
type snapshotHeader struct {
	Magic                    [8]byte
	Records, Names, Metadata uint32
}

func (s *Snapshot) Save(ctx context.Context, path string) error {
	if s.hasRemoved() {
		s = s.Compact()
	}
	if len(s.Nodes) == 0 || uint64(len(s.Nodes))*52+uint64(len(s.Names)) > SnapshotLimit {
		return fmt.Errorf("snapshot exceeds the 128 MiB uncompressed history limit")
	}
	info := SnapshotInfo{Root: s.Root, Engine: s.Engine, Fallback: s.Fallback, Started: s.Started, Elapsed: s.Elapsed, Cancelled: s.Cancelled, Errors: s.Errors, SkippedLinks: s.SkippedLinks, HardLinks: s.HardLinks, Unresolved: s.Unresolved, Issues: s.Issues, Exclusions: s.Exclusions, Excluded: s.Excluded, Total: s.Nodes[0]}
	metadata, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if len(metadata) > 1<<20 {
		return fmt.Errorf("snapshot metadata too large")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	compressed := newParallelGzip(file)
	header := snapshotHeader{Magic: snapshotMagic, Records: uint32(len(s.Nodes)), Names: uint32(len(s.Names)), Metadata: uint32(len(metadata))}
	err = binary.Write(compressed, binary.LittleEndian, header)
	if err == nil {
		_, err = compressed.Write(metadata)
	}
	encoded := make([]byte, 0, nodeBatch*nodeRecord)
	for start := 0; err == nil && start < len(s.Nodes); start += nodeBatch {
		if err = ctx.Err(); err != nil {
			break
		}
		encoded = encoded[:0]
		for i := range s.Nodes[start:min(start+nodeBatch, len(s.Nodes))] {
			encoded = appendNode(encoded, &s.Nodes[start+i])
		}
		_, err = compressed.Write(encoded)
	}
	if err == nil {
		_, err = compressed.Write(s.Names)
	}
	if closeErr := compressed.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readSnapshotHeader(reader io.Reader) (snapshotHeader, SnapshotInfo, error) {
	var header snapshotHeader
	var info SnapshotInfo
	if err := binary.Read(reader, binary.LittleEndian, &header); err != nil {
		return header, info, err
	}
	if header.Magic != snapshotMagic || header.Records == 0 || uint64(header.Records)*52+uint64(header.Names) > SnapshotLimit || header.Metadata > 1<<20 {
		return header, info, fmt.Errorf("unsupported or oversized Quarry snapshot")
	}
	metadata := make([]byte, header.Metadata)
	if _, err := io.ReadFull(reader, metadata); err != nil {
		return header, info, err
	}
	err := json.Unmarshal(metadata, &info)
	return header, info, err
}

func LoadSnapshot(ctx context.Context, path string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshot(ctx, data, true)
	if errors.Is(err, errSnapshotSegment) {
		// Not decodable member by member: read it serially for the real error.
		snapshot, err = loadSnapshot(ctx, data, false)
	}
	return snapshot, err
}

// loadSnapshot decodes a saved scan from its file contents. Files written by
// this version are decoded member by member in parallel when parallel is set.
func loadSnapshot(ctx context.Context, data []byte, parallel bool) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var compressed io.Reader
	if starts := gzipMemberStarts(data); parallel && len(starts) > 1 {
		segments := newSegmentStream(data, starts)
		defer segments.Close()
		compressed = segments
	} else {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		compressed = reader
	}
	header, info, err := readSnapshotHeader(compressed)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Root: info.Root, Engine: info.Engine, Fallback: info.Fallback, Started: info.Started, Elapsed: info.Elapsed, Cancelled: info.Cancelled, Errors: info.Errors, SkippedLinks: info.SkippedLinks, HardLinks: info.HardLinks, Unresolved: info.Unresolved, Issues: info.Issues, Exclusions: info.Exclusions, Excluded: info.Excluded, Nodes: make([]Node, header.Records), Names: make([]byte, header.Names)}
	encoded := make([]byte, nodeBatch*nodeRecord)
	for start := 0; start < len(snapshot.Nodes); start += nodeBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nodes := snapshot.Nodes[start:min(start+nodeBatch, len(snapshot.Nodes))]
		b := encoded[:len(nodes)*nodeRecord]
		if _, err := io.ReadFull(compressed, b); err != nil {
			return nil, err
		}
		for i := range nodes {
			nodes[i] = decodeNode(b[i*nodeRecord:])
		}
	}
	if _, err := io.ReadFull(compressed, snapshot.Names); err != nil {
		return nil, err
	}
	var tail [1]byte
	if count, err := compressed.Read(tail[:]); count != 0 || err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected trailing snapshot data")
	}
	if err := snapshot.validate(ctx); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Snapshot) validate(ctx context.Context) error {
	if len(s.Nodes) == 0 || !s.Nodes[0].IsDir() || s.Nodes[0].Parent != None || s.Nodes[0].NextSibling != None {
		return fmt.Errorf("invalid snapshot root")
	}
	// Validate while walking from the root, rather than scanning every child
	// list and then coloring every parent chain. Each edge must agree with the
	// parent we reached it from and each record may be visited only once. A
	// final count rejects disconnected records (including detached cycles).
	// All links used to ascend were checked on descent, so no stack is needed.
	seen := make([]bool, len(s.Nodes))
	current, parent := ID(0), None
	count, ascents := 0, 0
	for {
		if count&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if int(current) >= len(s.Nodes) || seen[current] {
			return fmt.Errorf("invalid snapshot child graph")
		}
		node := &s.Nodes[current]
		if node.Parent != parent {
			return fmt.Errorf("invalid snapshot parent")
		}
		if uint64(node.NameOffset)+uint64(node.NameLength) > uint64(len(s.Names)) {
			return fmt.Errorf("invalid snapshot name")
		}
		if !node.IsDir() && node.FirstChild != None {
			return fmt.Errorf("file has snapshot children")
		}
		seen[current] = true
		count++
		if node.FirstChild != None {
			parent, current = current, node.FirstChild
			continue
		}
		for current != 0 && s.Nodes[current].NextSibling == None {
			ascents++
			if ascents&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			current = s.Nodes[current].Parent
		}
		if current == 0 {
			break
		}
		parent, current = s.Nodes[current].Parent, s.Nodes[current].NextSibling
	}
	if count != len(s.Nodes) {
		return fmt.Errorf("unreachable snapshot record")
	}
	return ctx.Err()
}

func ListSnapshots(directory string) ([]SnapshotInfo, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []SnapshotInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "scan-") || !strings.HasSuffix(entry.Name(), ".quarry") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		compressed, err := gzip.NewReader(file)
		if err == nil {
			_, info, readErr := readSnapshotHeader(compressed)
			if readErr == nil {
				info.Path = path
				result = append(result, info)
			}
			compressed.Close()
		}
		file.Close()
	}
	slices.SortFunc(result, func(first, second SnapshotInfo) int { return second.Started.Compare(first.Started) })
	return result, nil
}

func PruneSnapshots(directory string) error {
	snapshots, err := ListSnapshots(directory)
	if err != nil {
		return err
	}
	var bytes int64
	for position, snapshot := range snapshots {
		info, err := os.Stat(snapshot.Path)
		if err != nil {
			continue
		}
		bytes += info.Size()
		if position >= 5 || (position > 0 && bytes > 256<<20) {
			if err := os.Remove(snapshot.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

type Change struct {
	Name              string
	Previous, Current uint64
	Increase          bool
	Difference        uint64
}

func CompareSnapshots(previous, current *Snapshot, allocated bool) ([]Change, error) {
	if !strings.EqualFold(filepath.Clean(previous.Root), filepath.Clean(current.Root)) {
		return nil, fmt.Errorf("comparison requires the same scan root")
	}
	groups := make(map[string]*Change)
	for position, snapshot := range []*Snapshot{previous, current} {
		for child := snapshot.Nodes[0].FirstChild; child != None; child = snapshot.Nodes[child].NextSibling {
			name := snapshot.NameView(child)
			key := strings.ToLower(name)
			change := groups[key]
			if change == nil {
				change = &Change{Name: name}
				groups[key] = change
			}
			value := snapshot.Nodes[child].Size
			if allocated {
				value = snapshot.Nodes[child].Allocated
			}
			if position == 0 {
				change.Previous += value
			} else {
				change.Current += value
			}
		}
	}
	result := make([]Change, 0, len(groups))
	for _, change := range groups {
		change.Increase = change.Current >= change.Previous
		if change.Increase {
			change.Difference = change.Current - change.Previous
		} else {
			change.Difference = change.Previous - change.Current
		}
		result = append(result, *change)
	}
	slices.SortFunc(result, func(first, second Change) int {
		if first.Difference > second.Difference {
			return -1
		}
		if first.Difference < second.Difference {
			return 1
		}
		return strings.Compare(first.Name, second.Name)
	})
	return result, nil
}
