# Historical local performance pass — 18 October 2026

This report measured commit `f805b56` against `a92797a` before the local checkout
was synchronized with upstream. It is retained as historical evidence only;
its figures do not measure the reconciled build. Upstream's report is in
[PERFORMANCE.md](PERFORMANCE.md).

This pass targets query latency, transient allocations, background cancellation,
and pointer interaction. Complete results, sort tie-breaking, snapshot format,
file-operation safeguards, and visual design are preserved.

## Findings and changes

- **Wide-folder search:** previously, each immediate child became a task with its
  own result slice. Small sibling subtrees now share a task. Substantial subtrees
  remain independent for load balancing. Results retain sequential walk order.
- **Numeric sorting:** separate key and ID arrays replace two padded record
  arrays. Scratch storage falls from 48 to 20 bytes per entry, excluding digit
  histograms. Key generation and scatter passes check cancellation periodically.
  Name and derived-column sorts retain the two-word keys they need.
- **Repeated File View sorts:** reuse a private copy of displayed IDs and their
  total when snapshot, query, scope and metric match. Folder sorting removes an
  entry-sized rank array; extensions are folded once per distinct spelling.
- **Filtering:** inherited path matches are computed once per directory rather
  than walking ancestors for every file. Name-only exclusions skip path
  construction. Folder totals use a constant-space postorder traversal instead
  of an entry-sized buffer. Superseded analysis filters are canceled, including
  their relinking, isolation and aggregation phases.
- **Search interaction:** typing cancels old work immediately and invalidates
  queued results. Debounce is 60 ms, down from 150 ms. Resets also invalidate
  queued model results, including treemaps, whose generations previously
  restarted from zero.
- **Treemap:** cached subtree ends let hit-testing skip whole branches. Tooltip
  facts are reused over the same tile; styles reuse capacity on resize. Tooltip
  caches release old snapshots when data changes.
- **Memory retention:** scan queue compaction clears obsolete path references.
  Starting another scan releases selection maps, ID buffers and cached snapshot
  references even when their view stays hidden. Large cached selection ID
  buffers are released when the selection becomes small or empty.
- **Tree and Extensions:** large child lists use the existing radix sorter;
  sorting extension descriptions folds each description once per sort.

## Measurements

Windows/amd64, Go 1.27.2, Intel Core i5-12400F, 12 logical processors.
Baseline: commit `a92797a`. Results are same-session medians of three Go benchmark
samples, using identical synthetic fixtures. MB means decimal MB. Allocation
figures are bytes allocated per operation, not retained heap or peak working
set. Benchmarks use Go's default GC setting; the desktop retains GOGC=25.

| Operation | Before | After | Allocation before → after |
|---|---:|---:|---:|
| Search 100,000 sibling files, all matches | 8.293 ms | 1.673 ms | 25.03 → 2.01 MB |
| Search 100,000 sibling files, no matches | 10.217 ms | 1.633 ms | 23.82 → 0.01 MB |
| Search sibling files by path and extension | 9.520 ms | 1.762 ms | 25.13 → 2.12 MB |
| Sort 500,000 IDs by size | 24.828 ms | 18.090 ms | 25.31 → 11.33 MB |
| Sort 500,000 IDs by modified time | 19.539 ms | 13.990 ms | 25.05 → 11.07 MB |
| Recompute totals, 100,000 sibling files | 0.642 ms | 0.445 ms | 401,408 → 0 bytes |
| Filter 100,000 files under 64 ancestor levels | 94.504 ms | 3.935 ms | 5.32 → 5.02 MB |
| Resort File View, 131,072 files | 4.637 ms | 4.566 ms | 9.27 → 3.93 MB |
| Hit-test a treemap with a 6,000-cell budget | 2.290 µs | 0.229 µs | 0 → 0 bytes |
| Headless treemap hover frame | 6.822 ms | 6.898 ms | 150 → 114 allocations |

Full treemap frame cost is effectively unchanged; faster hit-testing does not
imply tenfold faster rendering. Name-sort timing remains close to baseline.
An eight-query smoke benchmark on a nested half-million-entry tree remained
within 8% in short samples; no general search speedup is claimed for that shape.

## Verification and reproduction

Regression tests compare search order with sequential walking, full-width numeric
keys with the comparison sorter, filtered totals with an ancestor-walking
reference, and non-topological/removed-node aggregation with a postorder
reference. Treemap tests compare accelerated hit-testing with reverse scanning,
including edges and resizes. Additional tests cover queued-result rejection,
typing cancellation, cancellation during work, and sort reuse across changes of
scope, snapshot and metric.

```powershell
.\.tools\go\bin\go.exe test ./... -count 1
.\.tools\go\bin\go.exe test ./internal/index -run '^$' -bench 'Benchmark(SearchWide|ReaggregateWide|SortContext)$' -benchmem -count 3
.\.tools\go\bin\go.exe test ./internal/desktop -run '^$' -bench 'Benchmark(Treemap|FileResort|AnalysisFilterDeep)' -benchmem -count 3
powershell -NoProfile -File scripts/build.ps1 -Portable
```

The portable build runs tests, `go vet`, and MyGO UI lifetime checks before
compilation. Raw logs are in ignored `build/`: `perf-before.txt`, `perf-after.txt`,
`perf-wide-final.txt`, `perf-ui-before.txt`, `perf-extra-before.txt`, and
`perf-ui-after.txt`.

## Limits

These targeted improvements do not prove globally minimum latency or memory use.
Headless frames exclude GPU presentation. No fresh whole-drive throughput
comparison or Go race-detector run was performed. Disk throughput, shell calls,
snapshot copies, large selection fingerprints, and synchronous tree expansion
and rebuilding can still dominate particular workloads. Remaining UI-thread tree
work warrants profiling on unusually wide real folders before a larger
asynchronous model change.
