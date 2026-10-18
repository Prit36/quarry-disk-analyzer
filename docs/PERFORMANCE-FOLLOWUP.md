# Useful interaction paths — 18 October 2026

Baseline is the synchronized checkout at **844792f**, including all upstream
changes and the previous local optimizations. `git fetch origin` confirmed no
new upstream commits before this pass. Earlier reports are not used as this
pass's baseline.

Measurements use one newly saved directory scan of C:\: **1,452,146 entries**,
including 1,115,623 files and 336,522 folders. The original scan reported 125
unreadable items; both versions use the identical saved data. Windows/amd64,
Go 1.27.2, Intel Core i5-12400F, 12 logical processors. Baseline and optimized
executables ran alternately in three pairs; tables show medians. MB in allocation
figures is decimal; heap figures are MiB.

## Why these paths

The fresh baseline showed ordinary frames already around 1–2 ms. The substantial
interactive delays were name sorting, folder sorting, switching to Extensions,
and the first frame after selecting the whole file list. Profiling found serial
prefix-tie comparisons, individual folder-path allocations, general-purpose
analysis in Extensions, and repeated metadata ancestor walks in selection facts.

## Changes

1. **Name and derived-column sorts resolve prefix ties in parallel.** Radix
   sorting was already parallel, but full comparisons within equal prefix keys
   were serial. Worker boundaries now advance to complete key groups before
   sorting disjoint ranges. Descending order, Unicode comparisons and ID ties
   retain the original semantics.
2. **Folder sorting stores folded paths in shared chunks.** `AppendPath` writes
   into reusable caller-owned scratch storage. Workers copy folded paths into
   bounded string-builder chunks and sort references to those chunks. One
   four-byte parent-rank table also replaces the old separate seen/key tables.
   ASCII paths avoid per-folder strings; unusual and Unicode paths retain the
   original normalization and lowercase behavior.
3. **Extensions computes a scoped type summary.** It no longer calculates top
   folders it never displays. Independent subtrees are tallied on up to half the
   available processors, then exact integer totals are merged. This respects
   removed subtrees, selected scope and size metric; the existing whole-index
   `Summarize` cannot substitute because it also reads detached records.
4. **Large selections classify shared ancestors once.** Metadata availability
   uses the existing shared scratch pool instead of walking to the root for
   every selected file. Selecting all displayed results also reuses the
   non-overlapping total computed by the query worker. File-operation protection
   rules and on-disk checks are unchanged.

## Measured query work

These benchmarks use the real saved scan. Sorts include copying the original ID
order into a reusable buffer, then sorting. Extensions includes background
grouping and applying its result; asynchronous shell descriptions are canceled
between iterations. Selection facts measures a deliberately invalidated facts
cache after the selected-ID list is already available.

| Work | Baseline | Optimized | Allocation per operation |
|---|---:|---:|---:|
| Sort 1,115,623 files by name | 203.48 ms | 95.75 ms | 40.22 → 40.23 MB |
| Sort those files by folder | 150.59 ms | 126.60 ms | 104.60 → 80.69 MB |
| Group Extensions and apply result | 29.72 ms | 8.05 ms | 0.96 → 1.37 MB |
| Recompute facts for all selected files | 120.38 ms | 27.37 ms | Shared scratch pool |

Folder sorting falls from **361,256 allocations to 530**, and allocates about
**23.9 MB less**. Parallel extension grouping trades about **0.4 MB** of additional
temporary allocations for lower latency. Large-selection facts use the same
one-byte-per-node scratch pool as command availability; its buffer can be
reallocated after GC, so this is not a claim of zero allocated bytes.

## Measured UI paths

`TestPerfReport` exercises the same scan in the toolkit's headless tester. These
times include applying results and building a frame, and exclude Windows/GPU
presentation. They are distinct from the worker benchmarks above.

| UI path | Baseline | Optimized |
|---|---:|---:|
| Sort File View by name | 229.15 ms | 118.26 ms |
| Sort File View by folder | 191.43 ms | 180.79 ms |
| Open Extensions | 47.70 ms | 27.56 ms |
| Worst of the first five frames with all files selected | 130.18 ms | 38.30 ms |
| Refresh command availability for the selection | 127.23 ms | 32.08 ms |
| Idle file frame, median | 1.501 ms | 1.502 ms |
| Scrolling file frame, median | 2.001 ms | 2.002 ms |
| Settled heap after the full UI sequence | 226.405 MiB | 226.402 MiB |

The remaining costs matter: constructing Select All's toolkit selection map
still takes about 70 ms; ordinary all-file display remains about 60 ms. Folder
sorting's end-to-end improvement is smaller than its worker improvement. No
scan-throughput or GPU-rendering improvement is claimed in this pass.

## Verification

- All **484 complete-output SHA-256 digests** match the baseline, covering
  searches, sort orders, analysis and file models, and treemap output.
- New tests compare scoped summaries with `Analyze` for removed and unusual
  trees, multiple scopes, metrics and worker counts; compare parallel prefix
  ties with `Compare`; and verify appended paths preserve existing path results
  and caller prefixes.
- Folder tests exercise Unicode, normalized paths, UNC roots and multiple arena
  chunks. Selection tests compare bulk metadata classification with `IsMetadata`
  and worker-total reuse with the original selection total.
- `go test ./... -count 1`, `go vet`, MyGO lifetime checks and the portable build
  pass. The race detector and native GPU timing were not run.

## Reproduction and artifacts

The saved scan and raw measurements are local ignored artifacts in
`build/useful-pass/`. The baseline source archive is from 844792f; both versions
use the same `useful_perf_test.go` benchmark harness.

```powershell
$env:QUARRY_PERF_SNAPSHOT = (Resolve-Path build/useful-pass/baseline.quarry).Path
.\.tools\go\bin\go.exe test ./internal/desktop -run '^$' -bench '^BenchmarkReal(Sorts|Extensions|SelectionFacts)$' -benchmem -benchtime=500ms -count 3
$env:QUARRY_PERF_OUT = "$PWD/build/useful-pass/ui-repeat.json"
.\.tools\go\bin\go.exe test ./internal/desktop -run '^TestPerfReport$' -count 1
powershell -NoProfile -File scripts/build.ps1 -Portable
```

`baseline-round-*.txt` and `optimized-round-*.txt` hold the alternating benchmark
runs; `summary.json` contains their medians. `ui-baseline-*.json`,
`ui-optimized-*.json` and `ui-summary.json` contain UI results. Baseline and updated
parity hashes are `parity-before.json` and `parity-after.json`. The updated
executable is `build/portable/Quarry.exe`.
