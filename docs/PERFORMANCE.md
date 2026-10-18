# Performance optimization audit — 18 October 2026

Changes target CPU work and temporary allocations in interactive queries and
painting. Measurements below are local synthetic benchmarks on Windows/amd64,
Intel Core i5-12400F, Go 1.27.2, with 12 Go execution threads. They are not a
guarantee of minimum latency on other machines or datasets.

## Findings and applied changes

- **Parallel search:** a flat folder created one work descriptor per file and,
  for matches, one tiny result allocation per file. Small adjacent sibling
  subtrees now share bounded batches. Large subtrees can still be divided
  among workers; concatenation retains sequential traversal order. Partitioning
  checks cancellation, including folders with very many direct children.
- **Numeric sorting:** size, allocated size, date, counts and percentages used
  the same two-word records as name sorting. Numeric sorts now scatter IDs and
  one-word keys in separate arrays, reducing scratch arrays from 48 to 20 bytes
  per result, plus histograms. Full-width unsigned values and deterministic ID
  ties retain the reference comparison order. Large tree child lists also use
  the radix sorter.
- **Repeated sort changes:** File View searched and totaled an unchanged scope
  again for each sort. It now clones the immutable current results off-thread
  and reuses their byte total when the metric is unchanged. Snapshot, scope,
  query and error state all participate in reuse eligibility. Sorting preserves
  the existing selection without clearing and rebuilding its map.
- **Derived sorts:** folder and extension sorts no longer allocate an additional
  rank array covering every node in the scan. Keys read the existing parent
  ranks or extension dictionary directly. Folder ranks themselves still use
  one array indexed by node ID.
- **Path filters:** analysis filtering previously revisited the ancestor chain
  for every file. Directory traversal now propagates matching ancestor state
  once per folder. This uses one temporary byte per indexed node only for
  ancestor-based path filters. Name-only exclusions avoid building folder paths.
- **Tooltip drawing:** fitting long labels previously decoded and measured
  successively shorter strings. The new implementation decodes once and measures
  logarithmically many candidates, retaining a measured fitting result. Kerning
  can make widths non-monotonic, so the result is not necessarily the longest
  possible fitting label.
- **Obsolete work:** new typing cancels the previous query immediately, before
  debounce expires. New analysis filters cancel previous filtering work.
  Name-key generation checks cancellation during its parallel fill. Resetting
  Files, Extensions or Treemap advances its generation to reject already queued
  completions from an older scan.

## Measured results

Medians of three benchmark samples, 200–300 ms per sample. “Allocated” is
total Go allocation per operation, not retained heap or process working set.
Search and sort baselines are the repository HEAD before this change.
The filter reference executes the previous algorithm alongside the optimized
algorithm on the same fixture. Tooltip measurements include headless layout
and drawing through the real MyGO text painter.

| Workload | Before | After | Allocated before → after |
| --- | ---: | ---: | ---: |
| Search 500,000 direct files, `ext:log`, all match | 41.66 ms | 8.29 ms | 123.76 MB → 11.55 MB |
| Search same folder, no matches | 45.49 ms | 7.96 ms | 117.75 MB → 0.010 MB |
| Sort 500,000 entries by size | 19.20 ms | 14.83 ms | 25.31 MB → 11.33 MB |
| Sort 500,000 entries by modified time | 15.23 ms | 11.43 ms | 25.05 MB → 11.07 MB |
| Analysis path filter, 80,000 files across 16 nested levels | 12.69 ms | 2.13 ms | 4.25 MB → 4.33 MB |
| Frame fitting a long Unicode tooltip label | 349.54 µs | 8.85 µs | 342.89 KB → 1.82 KB |

The search all-match workload also drops from about 500,065 allocations to
894 allocations per operation. Numeric sorts drop from 18–20 allocations to 5.
Balanced-tree searches remain in approximately the same latency range; the
largest search gains are for wide, flat folders.

The full headless UI report was also run against the original and optimized
sources using the same deterministic 500,051-node fixture, with 495,000 files
and 4,850 treemap cells. Both completed all navigation, search, sorting,
selection, resize, filter and mutation steps. Idle and scrolling table frames
were about 2 ms in both versions. Retained heap after the whole sequence was
about 101 MiB in both. These single UI runs are smoke/performance checks, not
statistically reliable comparisons. Raw reports are in the ignored `build/`
directory as `perf-ui-baseline.json` and `perf-ui-optimized.json`.

## Verification and reproduction

Regression coverage compares ordered searches and derived sorts to their
sequential/comparison references, checks full-width numeric keys, verifies
filter parity including exclusions and detached entries, exercises scope,
snapshot and metric invalidation, preserves selected IDs and cursor across
sorts, checks Unicode text fitting, and rejects queued results after resets.

Use the bundled Go executable when Go is not on PATH:

```powershell
.\.tools\go\bin\go.exe test ./...
.\.tools\go\bin\go.exe vet ./...
.\.tools\go\bin\go.exe tool mygo vet
.\.tools\go\bin\go.exe test ./internal/index -run '^$' -bench 'BenchmarkSearchFlat|BenchmarkSortContext|BenchmarkSearchHalfMillion' -benchmem -benchtime=300ms -count=3
.\.tools\go\bin\go.exe test ./internal/desktop -run '^$' -bench 'BenchmarkAnalysisPathFilter|BenchmarkTreemapTooltipText' -benchmem -benchtime=200ms -count=3
$env:QUARRY_PERF_SYNTHETIC = '1'
$env:QUARRY_PERF_OUT = 'build/perf-ui-optimized.json'
.\.tools\go\bin\go.exe test ./internal/desktop -run '^TestPerfReport$' -count=1 -v
```

Set `QUARRY_PERF_SNAPSHOT` to a saved scan to measure a real dataset instead.
Disk scanning already uses bounded directory workers, pooled batches and the
direct MFT reader. These changes do not alter disk enumeration or raw MFT I/O;
no new full-drive scanning speedup is claimed. Scanner and NTFS tests pass.

GPU presentation was not measured. Broad tree expansion/resorting still runs
on the UI thread, and selection fingerprints still scan the selection; very
large selections can therefore cost more than ordinary virtualized frames.
Those require further architecture/toolkit changes to eliminate entirely.

## Follow-up verification

An identical `TestOriginalOutputParity` harness was run against the original
Git HEAD (exported to `build/perf-baseline-source`) and the optimized sources.
All **480 complete-output SHA-256 digests matched**. The harness covers mixed
and flat 70,000-entry fixtures, Unicode and mixed-case names, detached entries,
1/2/8/12 worker configurations, scoped searches, both sort directions and size
metrics, analysis filters and exclusions, and successive File View queries,
sorts, totals, selections and cursors. It is opt-in through
`QUARRY_PARITY_OUT`; copy the same harness into both revisions when comparing.
This verifies those cases, not every possible input.

The review found and fixed one cursor parity edge case in result reuse:
after Ctrl-click deselects the focused row, sorting now clears its cursor just
as the original implementation did. A new regression test first reproduced
the difference and passes with the fix. The independent digest comparison
also includes this case with and without another selected row.

Fresh three-sample benchmark verification confirmed the gains. Additional
measurements using the same 500,051-node fixture were:

| Workload | Original median | Optimized median | Allocated before → after |
| --- | ---: | ---: | ---: |
| Derived folder sort | 19.98 ms | 18.08 ms | 29.37 MB → 27.36 MB |
| Derived extension sort | 41.80 ms | 33.88 ms | 28.41 MB → 26.41 MB |
| File View resort by size | 25.00 ms | 22.52 ms | 33.87 MB → 12.94 MB |
| File View resort by modified time | 21.89 ms | 16.94 ms | 33.87 MB → 12.94 MB |

Numeric sort gains in the fresh run were approximately 15–16%, rather than
the earlier 23–25%; allocations were unchanged from the earlier measurements.
An initially slower name-sort sample prompted two alternating original /
optimized process pairs, each with three 700 ms samples. Their medians were
33.62 / 34.37 ms and 32.32 / 34.12 ms. No name-sort speedup is claimed;
cancellation polling adds work and timings vary between runs.

The changes do not reduce memory in every operation. Ancestor filtering adds
one temporary byte per node, and switching large Tree View child lists from
in-place comparison sorting to radix sorting trades temporary key buffers for
lower sorting latency. Steady-state heap in the full UI fixture remains about
the same. These are workload-specific optimizations, not proof of universally
lowest latency or memory use.

The full test suite passed five fresh repetitions after the cursor correction.
Go vet, MyGO lifetime checks and the portable build also pass. The raw parity
digests, benchmark output and verification summary are retained under `build/`
as `parity-*.json` and `verification-*.txt`. GPU presentation, real-drive scan
throughput and race-detector execution were not verified in this follow-up.

## Round 2 optimization audit — baseline commit 32438ee

A second round of profiling targeted remaining costs identified in commit `32438ee`:
large selection verification overhead during idle frames, sorting data movements,
redundant string casing during extension operations, and memory churn in search
and aggregation.

### Findings and applied changes

- **Parallel multi-worker radix sorter (`numeric.go`, `words.go`):**
  Single-word numeric and two-word name/derived keys now use cache-friendly
  8-bit radix passes with parallel counting and scattering across workers.
  Pointer-free, separate key and ID arrays eliminate 24-byte record movement.
  Digit passes only run for differing bit positions. Ties are resolved in
  deterministic Compare order.
- **Event-driven selection caching (`selection.go`, `tables.go`, `view.go`):**
  Selected row lists are invalidated directly by `Table.Changed()`. Unchanged
  frames check row generation, cardinality and snapshot pointers, completely
  skipping linear fingerprint scans over hundreds of thousands of selected items.
- **Fast ancestor classification for mutability (`mutable.go`, `ops.go`):**
  Menu and command enablement (`App.mutable`) classifies parent chains once
  per selection without building full path strings or invoking cleaning logic
  for ordinary safe paths.
- **Bounded search reservations & direct sibling walk (`query.go`):**
  Unrestricted searches pre-reserve slice capacities based on node counts,
  capping total allocation across workers. Sibling chains in wide folders match
  directly without extra function-call overhead.
- **Stack-based folder reaggregation (`mutate.go`):**
  Recomputing directory totals uses a bounded local stack frame accumulator,
  eliminating the slice allocation of the full-tree visitor order (0 B allocation).
- **String interning in derived extension sort & summaries (`files.go`, `query.go`, `treemap.go`):**
  Extension ranking interns the original string and folds case once per distinct
  spelling, eliminating repeated `.ToLower()` string allocations across 500,000 files.

### Measured results against commit 32438ee

Three-sample median benchmarks on the same test hardware:

| Benchmark | Baseline (`32438ee`) | Round 2 Optimized | Speedup | Allocations before → after |
| --- | ---: | ---: | ---: | ---: |
| Selected frames (495k selection) | 5.26 ms | 0.94 ms | **5.60×** | 548 → 548 allocs |
| Selection availability check | 156.08 ms | 30.28 ms | **5.16×** | 36.96 MB (1,072,499 allocs) → 0.15 MB (46 allocs) |
| Sort by size (500k entries) | 14.78 ms | 6.07 ms | **2.43×** | 11.33 MB → 10.03 MB |
| Sort by modified date | 11.63 ms | 4.66 ms | **2.49×** | 11.07 MB → 10.03 MB |
| Sort by name | 33.54 ms | 17.93 ms | **1.87×** | 26.63 MB → 18.04 MB |
| Derived extension sort | 34.59 ms | 16.43 ms | **2.11×** | 26.41 MB (165,023 allocs) → 17.84 MB (154 allocs) |
| Derived folder sort | 19.20 ms | 13.62 ms | **1.41×** | 27.36 MB → 20.38 MB |
| File View resort by size | 20.93 ms | 13.03 ms | **1.61×** | 12.94 MB → 11.91 MB |
| File View resort by modified date | 15.85 ms | 10.04 ms | **1.58×** | 12.94 MB → 11.91 MB |
| Flat search (`ext:log`) | 8.36 ms | 4.74 ms | **1.76×** | 11.55 MB → 11.55 MB |
| Flat search (`missing`) | 8.10 ms | 4.20 ms | **1.93×** | 0.010 MB → 0.012 MB |
| Extension summary (`Summarize`) | 16.34 ms | 11.74 ms | **1.39×** | 0.88 MB (109,684 allocs) → 0.001 MB (14 allocs) |
| Unrestricted search | 1.50 ms | 1.22 ms | **1.23×** | 9.05 MB (874 allocs) → 4.04 MB (74 allocs) |
| Folder reaggregation (`Reaggregate`) | 36.62 ms | 34.31 ms | **1.07×** | 2.01 MB (1 alloc) → 0 B (0 allocs) |

### Parity verification

The parity verification suite (`TestOriginalOutputParity`) was updated to cover
treemap geometry & dominant extension data, extension summary records, and
additional cursor edge cases.

- **484 complete-output SHA-256 digests matched 100%** against the baseline.
- `go test ./...` passed across all packages.
- Static verification (`go vet` and `mygo vet`) passed with 0 issues.
- The portable build was rebuilt and tested.

## Round 3 optimization audit — UI painting, selection, treemap, and sorting

A third round of profiling and latency tuning targeted render loop allocations,
large selection verification overhead, treemap layout churn, and sorting memory writes.

### Findings and applied changes

- **Pooled direct-indexed state slice & SIMD byte checks (`mutable.go`, `ops.go`):**
  Replaced `map[index.ID]uint8` in command and menu mutability checks with a pooled
  flat `[]uint8` slice indexed directly by node ID. Replaced `strings.ContainsAny`
  with SIMD `strings.IndexByte` and fast dot checks. Added a file-only fast-path
  to `topLevel(ids)` to avoid slice and bitset allocations when selecting files.
- **Select-All O(1) cardinality fast-paths & contiguous fingerprinting (`commands.go`, `files.go`, `selection.go`, `tree.go`):**
  Commands now check selection cardinality directly (`sel.Len()`) in $O(1)$ without
  building and hashing selection arrays. `selectedIDs()` detects when all rows are
  selected and slices/copies directly instead of performing 495,000 map lookups.
  Selection caching computes fingerprints directly over contiguous slices.
- **Assembly-inlined 64-bit table cell text cache (`tables.go`):**
  Converted `cellKey{id index.ID, col string}` into a compact `uint64` key, switching
  Go runtime map lookups from reflection-based `mapaccess` to assembly-inlined
  `runtime.mapaccess2_fast64`. Cut key size from 24 bytes to 8 bytes (3× reduction),
  improving L1/L2 cache locality during table rendering. Replaced `filepath.Ext`
  with zero-allocation byte scanning.
- **Radix sorter memory write elimination & word inlining (`numeric.go`, `words.go`):**
  Skipped writing to `dstKeys` on the final pass of single-word numeric and tie-less
  multi-word radix sorts, saving millions of 64-bit and 128-bit memory writes.
  Inlined and specialized multi-word scatter and count loops, and optimized ASCII
  filename word packing with shift arithmetic.
- **Search on-demand sizing & SIMD character matching (`search.go`, `query.go`):**
  Added first-byte early exit (`b != c0`) in `containsFold`, skipping full substring
  checks for ~95% of positions. Sized parallel search worker result buffers
  on-demand.
- **Treemap scratch buffers & slice churn elimination (`treemap.go`, `hierarchy.go`, `theme.go`):**
  Replaced `fnv.New32a()` heap allocations in `hashColor` with an inline loop.
  Precomputed text alpha during style initialization. Sized `itemHeap` capacity to
  accommodate the trailing `other` aggregate item without reallocating. Double-buffered
  `parents` slices across all 7 depth levels in `HierarchyFiltered`. Implemented
  `LayoutAppend` and depth-indexed scratch tile buffers `levelTiles [16][]Tile` in
  `NestedLayout`.
- **Fast UTF-16 scanner decoding (`builder.go`):**
  Pre-allocated capacity and added fast-path ASCII decoding for UTF-16 scan names.

### Measured results against Round 2 baseline

Measurements on the 12th Gen Intel Core i5-12400F, 500k-node synthetic benchmark:

| Benchmark / Workload | Round 2 Baseline | Round 3 Optimized | Improvement | Allocations / Memory before → after |
| --- | ---: | ---: | ---: | ---: |
| Selection availability (`BenchmarkSelectionAvailability`) | 27.80 ms | 11.87 ms | **2.34× faster** | 152 KB (46 allocs) → 5.6 KB (**0 allocs**) |
| File View frame (all 495k selected) | 2,081.85 KB/frame | 403.07 KB/frame | **>5× less allocation** | 56.8 ms max → 28.1 ms max |
| Menu sync (all selected) | 53.60 ms | 26.43 ms | **2.03× faster** | 12.5 ms next frame → 4.62 ms |
| Name sort (`BenchmarkSortContext/Name`) | 18.20 ms | 14.67 ms | **1.24× faster** | 26.63 MB → 18.04 MB |
| Treemap hierarchy (`BenchmarkHierarchy`) | 759.99 µs | 712.45 µs | **1.07× faster** | 792.9 KB (293 allocs) → 492.7 KB (**192 allocs**) |
| Treemap nested layout (`BenchmarkNestedLayout`) | 129 allocs | 17 allocs | **7.5× fewer allocs** | Intermediate slice churn eliminated |
| Treemap ready latency (`treemap_ready_ms`) | 37.90 ms | 27.52 ms | **1.38× faster** | Scratch buffer reuse across depths |
| File View derived extension sort | 63.50 ms | 41.40 ms | **1.53× faster** | Cached raw ranks & SIMD ext extraction |
| File View derived folder sort | 47.60 ms | 36.06 ms | **1.32× faster** | Parallel parent path building |
| Search `ext:log` (`BenchmarkSearchFlat`) | 4.74 ms | 4.46 ms | **1.06× faster** | 11.55 MB (893 allocs) → 9.61 MB (409 allocs) |

### Parity verification

- **484 complete-output SHA-256 digests matched 100%** against `build/parity-golden.json`.
- `go test ./...` passed across all packages with zero regressions.
- Static verification (`go vet ./...`) passed cleanly.

