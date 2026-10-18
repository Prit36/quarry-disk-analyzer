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
