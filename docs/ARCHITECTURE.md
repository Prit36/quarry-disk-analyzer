# Architecture

The latest export, saving, loading and duplicate-view measurements are in
[PERFORMANCE-EXPORT-SAVE.md](PERFORMANCE-EXPORT-SAVE.md).

The latest sorting, filtering and search measurements against commit
`38f9eeb` are in [PERFORMANCE-SORTS-FILTERS.md](PERFORMANCE-SORTS-FILTERS.md).
The preceding passes are in [PERFORMANCE-INTERACTIONS.md](PERFORMANCE-INTERACTIONS.md)
and [PERFORMANCE-THROUGHPUT.md](PERFORMANCE-THROUGHPUT.md).

```
main.go                 command line, or desktop.Run
internal/scan           MFT reader (ntfs) and directory enumerator; exclusions
internal/ntfs           MFT record parsing
internal/index          flat scan index: queries, search syntax, analysis,
                        copy-on-write mutation, snapshots, exports, duplicates
internal/treemap        bounded squarified nested layout
internal/platform       Windows services: drives, elevation, shell (Explorer,
                        Properties, Recycle Bin, rename, type names, verbs)
internal/desktop        the native UI
```

## Index

A scan is a `Snapshot`: 48-byte pointer-free `Node`s (sizes, modified time as 32-bit Unix seconds, parent/first-child/next-sibling IDs, counts) and one UTF-8 name arena. Paths are rebuilt on demand (`Path` measures, then fills one allocation; exports build paths incrementally while walking). Folders own no bytes; their totals are their children's. Because the index holds no pointers, the garbage collector never scans it; the GUI therefore runs with `GOGC=25`.

A directory scan grows its index in fixed-size chunks (`Builder`) and assembles exactly sized arrays once; the MFT reader sizes them from the MFT's own `$BITMAP`. Neither copies arrays as they grow.

Snapshots are shared with background goroutines and never mutated in place. A delete, rename, exclusion or analysis filter produces a copy with the same IDs (`Clone`, `Remove`, `Restore`, `Rename`, `Prune`, `Filtered`, `Reaggregate`), so selections, expansion and history survive. `Compact` renumbers a copy for saving.

`SortContext` radix-sorts numeric orders in stable 11-bit passes. Values and ID ties that fit together use two packed-word buffers (16 scratch bytes per entry); full-width values use separate key/ID buffers (20 bytes). Name sorting reads folded eight-byte chunks, inspecting later chunks only in groups sharing a prefix, with 20 scratch bytes per entry. Dense folder/extension ranks use a counting partition followed by sorting within groups. All paths preserve `Compare` ordering and ascending ID ties. Changing the direction of an existing numeric/name order reverses groups in parallel without building new key buffers. Numeric keys are read and packed on all cores. Name comparison, folding and contains-matching handle ASCII eight bytes at a time (`fold.go`), and name lists of 12 or more entries below the radix threshold sort by a folded eight-byte key first, falling back to the full comparison only for equal keys.


`ParseFilter` compiles the search syntax (wildcards, quoted phrases, negation, `ext:`, `size:`, `modified:`, `path:`, `type:`, `regex:` with Go's linear-time RE2) into a `Filter`; `Search` walks a scope without recursion and evaluates `path:` terms incrementally per folder rather than by building paths. Prefix, suffix and contains wildcards match ASCII names without backtracking. Scopes of 32,768 entries or more are divided into subtrees searched on all cores and concatenated in index order. Small adjacent sibling subtrees share tasks, keeping wide folders from allocating a descriptor and result slice for every file.

Saved scans keep the original format: 52-byte little-endian records, gzip. They are written as 1 MiB gzip members compressed in parallel, which any gzip reader (and every earlier release) reads as one stream. Loading finds those member boundaries and decodes the members in parallel, falling back to one serial stream for older files or when a member does not decode. The compressed file is read into memory outside the Go heap and released as soon as decoding finishes. `Compact` renumbers through a dense ID array rather than a map.

CSV and JSON exports format each row straight into one output buffer (`exportfmt.go`), with the bytes `encoding/csv` and `encoding/json` produced: quoting is decided by an eight-byte scan, times are formatted without `time.Time`, and the rare strings needing HTML or control-character escapes are passed to `encoding/json` itself. Row exports build each path in one reused buffer.

`Filtered` combines matching, child-list rebuilding and aggregation in one
depth-first folder walk. Empty subtrees are isolated as their parent's totals
finish, preserving the former removal flags and callback order. Scratch storage
tracks depth rather than every node. Unusual imported trees with children under
files retain the general filtering path. `FilteredParallel` produces the same
copy with concurrent, deterministic callbacks: the heaviest folders are split
into pieces rebuilt on workers, and the split folders are finished afterwards
in the original order. The Show/Hide Filter decides its files beforehand, the
include query as a parallel search and name exclusions in parallel ranges.

`SortByRankGroups` returns four-byte group lengths from the counting pass's
existing boundaries. File View retains only these lengths to reverse Folder or
Extension groups while preserving the name/size order inside each group. The
cache belongs to one immutable result order and is discarded on snapshot or
column changes; it retains no paths or per-file rank table.

Full-path terms use a worker-owned path buffer and ancestor stack, with the
existing normalization fallback for unusual paths and unresolved metadata.
Plain (non-wildcard) terms are matched incrementally: a term in a folder's path
holds below it, and otherwise only the folder path's last `len(term)-1` bytes
and the rest are searched while the folder's path is ASCII. Folders are decided
only when an entry below them reaches the path test. File View's Folder sort
builds parent paths with `PathCursor`, which keeps the previous path's
ancestors; its Extension sort interns extensions on all cores and passes ranks
by position (`SortByRankValues`).
Unrestricted searches give workers disjoint, capacity-clipped ranges of a
single result array. Incorrect saved counts can grow individual ranges safely;
the coordinator then copies or compacts them without changing traversal order.

### Analysis on demand

`age.go`, `categories.go`, `diff.go`, `graft.go` and `history.go` add analyses that read the immutable index and never change it:

- `AgeProfiles` sums files into five age ranges (the same bounds as the `age:` search terms, so a range's count equals its query's) for several minimum sizes in one parallel piece walk, as `SummarizeScope` splits work. Insights and the report make three walks in all: `Analyze` (extension totals and largest files), the age ranges, and the duplicate candidates. `SummarizeCategories` folds extension totals into fixed categories (`cat:` terms; `cat:other` is everything else). The online-only cloud flag (`CloudOnly`, from the recall/offline attributes) is stored in the node flags and saved with them.
- `Comparison` relates two snapshots of the same root by path. Folder totals already exist in both, so `Children` only matches one folder's children by folded name; `LargestChanges` walks the rest, skipping folders whose totals and counts are unchanged: the heaviest folders are opened until there are pieces for every worker, each worker keeps its own bounded heap, and a total order (change, size, IDs) makes the merged result independent of the split. Folded names live in a per-folder arena, so matching allocates nothing per entry.
- `Graft` replaces one folder's contents with a fresh scan of it: the old children are detached (as `Remove` does), the new nodes appended with new IDs, and ancestors adjusted by the difference, so every other ID and selection stays valid. The node table is copied once, sized for both.
- `RecheckDuplicates` re-hashes a keeper and the copies to be deleted in full and fails on any change of size, time or contents.
- History summaries (`SummarizeHistory`) hold totals and the folders near the root; they are small JSON files listed without loading any index, and `CompareSummaries` compares two of them.

## Scanning

- **MFT** (`mft_windows.go`, administrator): two readers with their own unbuffered volume handles read the MFT in 2 MiB chunks, skipping regions the `$BITMAP` marks unused, and parse records into per-chunk arenas; the coordinator applies chunks in order, so IDs are deterministic. Names found in extension records (large folders such as WinSxS keep theirs there) replace or supply the base record's.
- **Directory enumeration** (`scan.go`, `enumerate_*.go`): up to eight workers query folders with `FILE_ID_BOTH_DIR_INFO`, decode names straight into pooled batch arenas and open folders through a pooled UTF-16 path buffer. Hard links are found after the scan by radix-sorting (file ID, entry) pairs. Folders are opened by path, not by file ID: opening by ID bypasses namespace-virtualizing filter drivers and lists different contents.

## Desktop

- `app.go` - `App`: scanning, the base snapshot and the filtered view snapshot, the single navigation history (location, back, forward, up, reveal). Hidden NTFS metadata is detached from the base snapshot itself (`Remove`, `Restore` to show it), not kept as a second copy.
- `commands.go` - every user action once: label, shortcut, availability, check state, action. The native menu bar, row and treemap context menus and the keyboard dispatcher read the same table. Menu labels show shortcuts without registering native accelerators, so text fields keep Ctrl+C, Delete, etc.
- `tree.go` - Tree View model: flattened rows of the expanded tree below the location, spliced on expand/collapse, keyed by index ID; bounded subtree expansion.
- `files.go`, `extensions.go` - File View and Extensions: queries and sorts on goroutines with generation counters; results are applied on the UI thread, and a query already running for the shown snapshot is not restarted by later frames. Sort changes reuse matching immutable query results and totals. Typing cancels obsolete work before debounce expires; model resets reject already queued completions. Snapshot changes drop obsolete cache identities in hidden views and cancel their old workers, so lazy refresh does not retain a former copy-on-write node table. Explorer type names are resolved in parallel after the Extensions table is shown.
- `treemap.go` - hierarchy built off-thread (6,000 tiles, 7 levels, "Smaller items" aggregates), dominant extension per aggregated block, geometry cached per size, tile colors and labels cached per layout and setting.
- `selection.go` - selected entries in row order and facts about them (all on disk, total size), cached between frames and invalidated directly on table input changes (`Table.Changed()`). A complete file selection borrows the immutable result list and validates its cardinality/generation without hashing every ID. Partial selections retain fingerprint checks. Replacing a bulk selection with one entry releases its large map without clearing every bucket.

- `query.go` - `showQuery` (every summary leads to its files in File View), quick filter presets, the Filters menu and operator suggestions under the search box (an overlay whose rows keep the focus in the box).
- `insights.go`, `compare.go`, `basket.go`, `history.go`, `report.go`, `refresh.go` - Storage Insights/Folder Details, the scan comparison, the cleanup basket, opt-in scan history, the HTML report and folder refresh. Each is a modal or menu command over the existing views; their work runs on goroutines with generation counters, like File View's, and nothing is kept after the window closes except opt-in history.
- `mapmatch.go` - which treemap tiles hold a File View match: bit sets of matched entries and their ancestors built off-thread per result set, then one lookup per tile.
- `ops.go` - file operations through `internal/platform`, each re-validating the on-disk entry first, then updating the index copy-on-write.
- `view.go`, `tables.go`, `dialogs.go` - layout, grids and modal dialogs.
- `prefs.go` - persisted settings (atomic write on close).

Rule of thumb: the UI thread only reads snapshots and small models; anything proportional to the number of files runs on a goroutine and posts its result back with `App.post`.

## Safety

Deletion goes through `SHFileOperationW` (Recycle Bin with `FOF_ALLOWUNDO`, or permanent after an explicit warning), rename through `MoveFileExW` without replacement. NTFS metadata, unresolved MFT records, the scan root and Windows system locations are blocked; saved scans are read-only. The index is updated only for entries confirmed gone afterwards.

## Tests

`go test ./...` covers the scanners, NTFS parsing, the index (search syntax, mutation, snapshots, duplicates) and the desktop models, plus headless view tests with `ui.NewTester`. Interactive checks of the built executable are listed in the 0.1.0 changelog entry.

Performance is measured with `go run scripts/bench.go -scan C:\ [-walk] [-ops]` (scan time, phases, peak working set and private bytes, allocations, then every index operation on the result), `TestPerfReport` in `internal/desktop` (headless frame times and view latencies on a saved scan; set `QUARRY_PERF_SNAPSHOT`) and, as administrator, `TestMFTDiagnostics` in `internal/scan` (MFT read settings on a real volume; set `QUARRY_MFT_DIAG=C:\`). Run directory-scan benchmarks outside sandboxes: filter drivers change their cost.

`QUARRY_PERF_SYNTHETIC=1` runs the headless UI report on a deterministic 500,051-node fixture without disk scanning. See [PERFORMANCE-LATENCY.md](PERFORMANCE-LATENCY.md) for the latest common-path latency and memory measurements, [PERFORMANCE-FOLLOWUP.md](PERFORMANCE-FOLLOWUP.md) for the preceding pass, and [PERFORMANCE.md](PERFORMANCE.md) for the earlier scan/UI audit.
