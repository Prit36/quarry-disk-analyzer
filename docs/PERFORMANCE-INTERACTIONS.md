# Search, filtering and sort-direction performance

Baseline: `cb57e1f`, the current 0.2.2 checkout before this pass. Measurements
use the same saved directory scan of `C:\` in both versions: **1,452,146 nodes,
1,115,623 files and 336,522 folders**. Windows amd64, Go 1.27.2, Intel Core
i5-12400F, 12 logical processors. No live scan is included.

Input SHA-256:
`d47d7e07795011538cc9c01c5c174e01b281482a10d9bcb28926e95f09d3509d`.

The baseline source was exported from Git and compiled with the same benchmark
harness as the changed version. Three pairs alternate before/after and
after/before; each worker benchmark runs for 700 ms. Tables report medians of
three independent samples. MB/op means decimal bytes allocated per operation,
not peak working set or retained memory. The final raw samples and JSON summary
are under `build/interaction-pass/verified/`; earlier trials are excluded.

Before committing, the shared-output-buffer experiment was removed because
it did not improve unrestricted-search latency. Only sort-direction reuse,
full-path traversal buffers and fused analysis filtering remain. Their timed
paths are unchanged by that removal. Retained-build benchmark confirmation is
in `build/interaction-pass/commit-benchmarks.txt` (three samples, 500 ms each),
with a fresh output comparison in `commit-parity.json`.

## Changes

- **Sort directions reuse exact groups.** The initial Folder/Extension sort
  returns group lengths from its existing counting boundaries. A direction
  change copies the immutable result IDs, reverses the groups and preserves
  each group's internal name/size order. This avoids rebuilding paths,
  extensions, ranks and sort scratch. Four bytes per group remain cached until
  the result, column or snapshot changes; there is no retained path arena or
  per-file rank table. The ordinary first sort adds no second per-entry key
  lookup. Unicode and distinct folder IDs with equal normalized paths use the
  same ranks as before.
- **Full-path search reuses traversal buffers.** Each worker maintains the
  current folder path and builds file paths into reusable storage. Every
  directory feeds the walker even if another criterion rejects it. Existing
  normalization and unresolved-metadata fallbacks are preserved, and borrowed
  path strings are consumed before the buffer changes.
- **Analysis filters combine tree work.** Matching, relinking, totals and
  empty-folder isolation finish during one depth-first folder traversal,
  replacing several complete tree walks and a per-node drop array. The copy
  keeps stable IDs, callback order, totals and removal flags, including inside
  detached empty subtrees. A small eligibility pass preserves the old general
  behavior for unusual imported trees with children under files. Scratch
  storage grows with depth, and cancellation is checked throughout.

## Measurements

Queries include the background search, default size sort and application to
File View. Direction changes include copying the immutable IDs, changing the
order and applying the result. Filter measurements include the snapshot copy
and recomputed totals.

| Interaction | Before ms | After ms | Latency change | Before MB/op | After MB/op |
|---|---:|---:|---:|---:|---:|
| Reverse Folder heading | 47.349 | 1.876 | -96.0% | 63.775 | 5.194 |
| Reverse Extension heading | 49.297 | 1.161 | -97.6% | 30.074 | 4.475 |
| `path:C:\Windows\System32` | 185.987 | 57.108 | -69.3% | 193.890 | 1.401 |
| `type:file path:C:\Windows ext:dll` | 12.256 | 11.124 | -9.2% | 8.617 | 1.927 |
| Filter: show `ext:dll` | 54.967 | 42.316 | -23.0% | 71.868 | 69.709 |
| Filter: show `path:system32 ext:dll` | 63.116 | 53.102 | -15.9% | 73.129 | 71.168 |
| Filter: hide `node_modules;*.tmp` | 54.036 | 44.063 | -18.5% | 71.670 | 69.710 |
| Filter: no matching files | 65.363 | 57.684 | -11.7% | 71.670 | 69.710 |

Folder direction samples were 47.199–47.790 ms before and 1.869–1.880 ms
after. Extension direction samples were 48.681–49.605 ms before and
1.150–1.178 ms after. The broad full-path query measured 185.657–190.217 ms
before and 56.801–59.931 ms after. Its allocation reduction is **99.3%**;
Folder/Extension direction allocations fall **91.9%/85.1%**. The discarded
shared-output-buffer experiment reduced unrestricted-search allocation by
16.5–16.7% but measured 1.1–3.3% slower. Those memory savings are not part of
the retained implementation.

Ordinary query changes are small and include regressions: `*.dll` measured
7.879 → 8.292 ms (+5.2%), `microsoft` 14.408 → 14.845 ms (+3.0%), and
`path:system32 ext:dll` 8.999 → 9.197 ms (+2.2%). Their allocations are
essentially unchanged. These queries are not counted as speedups.

First-time sort workers are also essentially unchanged: Name 50.032 → 50.700 ms,
Folder 56.563 → 57.059 ms, Extension 30.331 → 30.258 ms, Size 13.528 →
13.370 ms and Modified 13.454 → 13.467 ms. The headless UI sequence's first
Extension sort measured 57.097 → 62.682 ms and Folder 99.721 → 101.832 ms;
these interactions have no demonstrated improvement despite the large gains
when reversing an already sorted heading.

| Rendering/memory checkpoint | Before | After |
|---|---:|---:|
| Tree idle frame median | 2.070 ms | 2.107 ms |
| Treemap hover frame median | 2.143 ms | 2.172 ms |
| Pointer-move plus frame benchmark, larger treemap fixture | 5.812 ms | 5.819 ms |
| Selected-file frame benchmark | 0.902 ms | 0.903 ms |
| Retained heap at end of UI sequence, after GC | 138.942 MiB | 138.954 MiB |
| Index storage | 105.351 MiB | 105.351 MiB |

Rendering and steady-state storage are unchanged. The memory gains are in
temporary operation allocations; these measurements do not establish a peak
working-set reduction. The Folder group cache adds about 0.7 MiB for this
fixture while that column is active and is released on column/snapshot changes.
Some individual UI timings round to zero in the raw report because of timer
resolution; that does not mean zero work.

## Verification and limits

All **484 complete-output digests match** the baseline. They cover ordered search and sort results, filtered node
tables and statistics, treemap data, and File View state. Additional regression
tests cover deep filters, callback order, source immutability, cancellation,
Unicode and normalized paths, sparse ranks, repeated
direction changes, metric changes and snapshot-cache lifetime.

The portable build passed all package tests, Go vet, MyGO UI lifetime checks and
compilation. A Windows scanner handle-count assertion failed once during the
first build attempt; it passed five consecutive targeted runs and the full
build rerun, without scanner changes. Formatting and `git diff --check` are clean.

The UI is already virtualized and its normal rendering is inexpensive. This
pass changes the work behind UI interactions; it does not change the visual
design, GPU renderer, scanner, saved format, result completeness or file
operation protection. Headless rendering measurements cannot establish native
GPU presentation improvements. Small changes in unrelated frame and first-sort
timings are included in the raw reports, rather than presented as new gains.

## Reproduction

```powershell
$env:QUARRY_PERF_SNAPSHOT = (Resolve-Path build/useful-pass/baseline.quarry).Path
.\.tools\go\bin\go.exe test ./internal/desktop '-run=^$' '-bench=^BenchmarkReal(Sorts|SortToggle|AnalysisFilters|FileQueries)$' '-benchtime=700ms' -count=3
.\.tools\go\bin\go.exe test ./internal/desktop -run TestPerfReport -v
.\.tools\go\bin\go.exe test ./... -count=1
powershell -NoProfile -File scripts/build.ps1 -Portable
```

For before/after comparisons, build `cb57e1f` separately with the current
`internal/desktop/useful_perf_test.go` copied into it, then alternate the two
binaries. `TestOriginalOutputParity` writes its complete-output digests when
`QUARRY_PARITY_OUT` names an output JSON file. Windows filesystem/scanner tests
must run outside the restricted sandbox, which denies rename and native file
handle operations.
