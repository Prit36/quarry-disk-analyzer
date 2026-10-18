# Sorting, filtering and search performance

Baseline: `38f9eeb`, the checkout before this pass. Both versions run the same
benchmarks on the same saved directory scan of `C:\`
(`build/useful-pass/baseline.quarry`): **1,452,146 nodes, 1,115,623 files and
336,522 folders**. Windows amd64, Go 1.27.2, Intel Core i5-12400F, 12 logical
processors.

The baseline test binary was compiled from `38f9eeb` before any change. Three
runs alternate before/after, after/before, before/after; each benchmark runs
for 700 ms. Tables report medians of the three runs. MB/op is decimal bytes
allocated per operation, not working set. Raw output is under
`build/max-pass/` (`bench-*.txt`, `summary.json`).

## Changes

- **Word-at-a-time name helpers** (`internal/index/fold.go`). `compareFold`
  compares ASCII names eight bytes at a time and hands any word with another
  byte to the existing byte loop, which still decides where Unicode folding
  takes over. Folding, the ASCII check, separator checks (formerly
  `strings.ContainsAny`, which builds a set per call) and contains-matching
  use the same technique. Randomized tests compare every helper with the byte
  loop it replaces, including Unicode at every position.
- **Small name sorts compare a folded eight-byte key first.** Groups of 12 or
  more names (the radix sort's tie groups, File View's per-folder groups,
  Tree View children) sort `(key, ID)` pairs. A key never orders two names
  opposite to `compareFold`, and equal keys fall back to the full comparison,
  so the order is unchanged. Numeric small sorts compare the column directly
  instead of going through `Compare`, without allocating.
- **Numeric sort passes run in parallel.** Reading keys, the summary of
  differing bits, packing and unpacking each ran on one core before the
  parallel radix passes. Packed keys reuse the bits computed while reading.
- **Extension ranks are computed in bulk.** Each worker interns the original
  spellings of its range, skipping repeats of the previous one, and records a
  slot per entry; the merged, folded and sorted extensions turn slots into
  ranks by array lookup. `SortByRankValues` takes those ranks by position, so
  no entry is hashed twice.
- **Folder sort paths reuse ancestors.** `PathCursor` keeps the previous
  path's ancestor chain; neighbouring parents rebuild only the part below
  their common ancestor. Paths are folded eight bytes at a time.
- **Plain full-path terms are matched incrementally.** A term found in a
  folder's path is in every descendant's. Otherwise, when the folder's path is
  ASCII, an occurrence must end after it, so only its last `len(term)-1`
  bytes and the rest are searched. A folder is decided only when an entry
  below it reaches the path test; non-ASCII paths are searched in full.
- **Search walks without callbacks.** The search loop is the same depth-first
  traversal as `Visit`, calling the matcher directly instead of through two
  closures per entry.
- **Show/Hide Filter on all cores.** The include query runs as a parallel
  search of the whole scan, which is exactly the set of files the filter
  keeps. Name exclusions are matched in parallel ranges, and the tree rebuild
  reads bits. `FilteredParallel` then rebuilds separate subtrees on workers.
  The heaviest folders are split until there are four pieces per core; a
  split folder is never one the exclusions drop. The coordinator finishes the
  split folders in the original order and attaches the finished pieces. The
  node table copy checks for imported trees with children under files while
  each block is in cache; those trees keep the sequential path. Exclusions of
  plain names compare lengths before folding.

## Measurements

| Operation | Before ms | After ms | Change | Before MB/op | After MB/op |
|---|---:|---:|---:|---:|---:|
| Filter: show `ext:dll` | 44.446 | 21.494 | -51.6% | 69.709 | 71.047 |
| Filter: show `path:system32 ext:dll` | 53.261 | 23.084 | -56.7% | 71.168 | 71.648 |
| Filter: hide `node_modules;*.tmp` | 44.243 | 24.222 | -45.3% | 69.710 | 69.907 |
| Filter: no matching files | 57.577 | 22.580 | -60.8% | 69.710 | 70.099 |
| Sort by Extension | 29.932 | 13.641 | -54.4% | 25.609 | 25.927 |
| Sort by Size | 13.710 | 8.009 | -41.6% | 17.965 | 17.967 |
| Sort by Modified | 13.809 | 7.882 | -42.9% | 17.965 | 17.967 |
| Sort by Folder | 57.502 | 40.470 | -29.6% | 59.309 | 59.336 |
| Sort by Name | 50.778 | 42.351 | -16.6% | 22.496 | 22.496 |
| Reverse Name heading | 9.653 | 8.099 | -16.1% | 4.466 | 4.466 |
| `path:C:\Windows\System32` | 56.976 | 21.589 | -62.1% | 1.401 | 1.411 |
| Show all files | 19.475 | 13.928 | -28.5% | 22.826 | 22.829 |
| `type:folder` | 9.795 | 7.796 | -20.4% | 7.184 | 7.185 |
| `type:file path:C:\Windows ext:dll` | 10.915 | 9.246 | -15.3% | 1.927 | 1.938 |
| `microsoft` | 14.839 | 13.488 | -9.1% | 5.042 | 5.043 |
| `path:system32 ext:dll` | 9.154 | 8.508 | -7.0% | 2.204 | 2.204 |
| `*.dll` | 8.289 | 7.980 | -3.7% | 2.406 | 2.406 |
| Extensions analysis | 7.486 | 7.352 | -1.8% | 1.380 | 1.378 |

The three runs do not overlap for any row above except Extensions analysis,
which is unchanged code and counted as unchanged. Reversing the Size,
Modified, Folder and Extension headings measured -0.2% to +1.6% with
overlapping ranges; that code did not change.

Queries include the background search, the default size sort and applying the
result to File View. Filters include the node table copy and recomputed
totals. A filter still allocates one node table (70 MB here), because views
keep immutable snapshots; its bits and pieces add 0.2–1.4 MB.

Navigation and folder removal, measured with a 40-iteration benchmark built
from each source tree: navigating into the largest folder and back
45.3 → 45.0 ms, removing it and restoring 48.0 → 48.0 ms. The headless frame
benchmarks (tree idle about 2.1 ms, treemap hover about 2.1 ms) and the
retained heap (138.9 MiB, index 105.4 MiB) are unchanged. Frame time is
spent in MyGO's painting and text layout, and per-frame allocations
(12–30 KB) are mostly the toolkit's per-row elements. Headless frames cannot
show GPU presentation.

## Verification

- All **484 complete-output digests** of `TestOriginalOutputParity` match
  those of `38f9eeb`. They cover ordered searches and sorts, filtered node
  tables and statistics, treemap data and File View state, with 1, 2, 8 and
  12 workers, on a 70,000-entry fixture large enough for the parallel filter.
- New tests compare the eight-byte helpers with the byte loops on 200,000
  random strings. They also compare incremental path terms with whole-path
  matching on random trees (Unicode, irregular names, unresolved entries,
  negated and multiple terms, sequential and parallel scopes, three roots).
  The rest compare `PathCursor` with `Path` in index and shuffled order,
  `FilteredParallel` with `Filtered` on four tree shapes, the parallel numeric
  sort and name radix sort with `Compare`, and plain-name exclusions with
  `strings.EqualFold`.
- `go test ./...`, `go vet ./...`, the MyGO lifetime checks and the portable
  build (`scripts/build.ps1 -Portable`) pass.

## Reproduction

```powershell
$env:QUARRY_PERF_SNAPSHOT = (Resolve-Path build/useful-pass/baseline.quarry).Path
.\.tools\go\bin\go.exe test ./internal/desktop '-run=^$' '-bench=^BenchmarkReal(Sorts|SortToggle|AnalysisFilters|FileQueries|Extensions)$' '-benchtime=700ms' -benchmem
$env:QUARRY_PARITY_OUT = "$PWD\build\parity.json"
.\.tools\go\bin\go.exe test ./internal/desktop -run '^TestOriginalOutputParity$'
```

For before/after, compile `38f9eeb`'s `internal/desktop` test binary with
`go test -c` and alternate the two binaries.
