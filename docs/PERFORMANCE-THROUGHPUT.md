# Useful sorting and snapshot-memory pass

Baseline: `4c7d3d2`. This pass targets first-time Name/Folder/Extension sorts, repeated heading direction changes, plain-name search, bulk-selection bookkeeping, and retained memory after changing a scan.

## Method

Windows amd64, Intel Core i5-12400F, 12 logical processors, Go 1.27.2. The input is the same saved `C:\` scan used in the previous passes: 1,452,146 nodes, 1,115,623 files, and 336,522 folders. Its SHA-256 is `d47d7e07795011538cc9c01c5c174e01b281482a10d9bcb28926e95f09d3509d`. No live disk scan is included.

The baseline source was exported from Git and built separately. Both binaries use byte-identical UI and worker benchmark harnesses, including the new repeated-heading benchmark. Five pairs alternate before/after and after/before; each worker benchmark runs for one second. Tables show medians of five independent samples. The JSON summary records every sample and its range. Worker allocation figures are decimal MB/op; retained heap figures are MiB after GC. Retained heap is not peak memory or process working set.

The UI harness measures headless layout, text shaping, display-list construction, worker completion and application; it excludes native GPU presentation. Its reported durations can round to zero, which is shown as below timer resolution. Those results do not mean an operation literally costs no time.

## First-time sorting

| Column | Before ms | After ms | Latency change | Before MB/op | After MB/op | Allocation change |
|---|---:|---:|---:|---:|---:|---:|
| Name | 73.066 | 51.070 | -30.1% | 40.218 | 22.494 | -44.1% |
| Folder | 85.376 | 57.914 | -32.2% | 79.245 | 59.310 | -25.2% |
| Size | 13.496 | 14.056 | +4.1% | 17.965 | 17.965 | +0.0% |
| Modified | 13.580 | 13.498 | -0.6% | 17.965 | 17.965 | -0.0% |
| Extension | 48.579 | 30.438 | -37.3% | 40.786 | 25.609 | -37.2% |

## Reversing the active heading

This benchmark starts with an already sorted File View and clicks the same heading in the opposite direction each iteration. It includes cloning the immutable result IDs, the background operation, and applying the result. Name and numeric equal-value groups retain ascending ID ties; Folder and Extension directions still use their complete sort paths.

| Column | Before ms | After ms | Latency change | Before MB/op | After MB/op |
|---|---:|---:|---:|---:|---:|
| Name | 82.934 | 9.946 | -88.0% | 44.676 | 4.466 |
| Size | 20.779 | 5.249 | -74.7% | 22.430 | 4.466 |
| Modified | 15.997 | 3.519 | -78.0% | 22.430 | 4.466 |

## Common queries and extension summary

| Task | Before ms | After ms | Change | Before MB/op | After MB/op |
|---|---:|---:|---:|---:|---:|
| All files | 19.970 | 19.700 | -1.4% | 27.391 | 27.391 |
| `*.dll` | 8.369 | 8.102 | -3.2% | 2.406 | 2.406 |
| `microsoft` | 15.997 | 14.888 | -6.9% | 5.043 | 5.043 |
| `path:system32 ext:dll` | 10.042 | 9.531 | -5.1% | 2.204 | 2.204 |
| `type:folder` | 9.825 | 9.818 | -0.1% | 8.605 | 8.605 |
| Extensions summary | 7.597 | 7.529 | -0.9% | 1.366 | 1.366 |

## UI sequence

| Task | Before ms | After ms | Change | Before range ms | After range ms |
|---|---:|---:|---:|---:|---:|
| Show all files | 37.468 | 36.365 | -2.9% | 30.953–43.223 | 36.202–38.370 |
| Sort Name | 95.682 | 71.810 | -24.9% | 90.485–98.309 | 67.660–76.815 |
| Sort Folder | 133.709 | 98.676 | -26.2% | 129.616–138.553 | 89.007–103.504 |
| Sort Extension | 72.552 | 56.645 | -21.9% | 69.451–78.807 | 52.430–62.533 |
| Sort Modified | 27.122 | 26.151 | -3.6% | 25.511–30.468 | 20.153–26.489 |
| Search microsoft | 27.022 | 25.929 | -4.0% | 26.116–30.984 | 22.697–26.993 |
| Search folders | 20.262 | 22.710 | +12.1% | 19.794–21.111 | 20.538–24.892 |
| Select All command | 58.649 | 57.488 | -2.0% | 54.648–59.648 | 52.654–60.105 |
| Worst of first five selected frames | 7.925 | 7.183 | -9.4% | 7.539–13.381 | 6.121–10.184 |
| Forced selection/menu checkpoint | 9.659 | 0.000 | Below timer resolution | 6.024–15.763 | 0.000–0.000 |
| Return to one selected file | 3.100 | 0.000 | Below timer resolution | 0.000–6.469 | 0.000–0.000 |
| Extensions | 24.870 | 23.671 | -4.8% | 23.415–27.108 | 23.088–27.566 |
| Folder mutation and treemap refresh | 33.611 | 27.859 | -17.1% | 30.013–35.015 | 25.707–30.351 |

The folder-search UI median rose by 2.448 ms in this batch, while its worker median stayed at 9.82 ms. This interaction has no demonstrated improvement; the measurement does not establish the cause of the UI difference. Initial Size sorting also measured 0.560 ms slower despite using the unchanged algorithm. Both results are included above rather than counted as gains.

## Retained heap

| Checkpoint | Before MiB | After MiB | Difference MiB |
|---|---:|---:|---:|
| Scan installed | 117.95 | 117.95 | +0.00 |
| All files selected | 155.00 | 151.45 | -3.55 |
| Selection reduced to one | 135.02 | 131.47 | -3.55 |
| End of mutation sequence | 203.52 | 136.70 | -66.82 |
| Index storage | 105.35 | 105.35 | +0.00 |

## Implementation and practical limits

- Name sorts use two eight-byte key buffers and one four-byte ID buffer. Later chunks are read only for shared-prefix groups, scratch space is reused across groups, and long shared prefixes fall back to suffix comparison after 64 bytes. Full comparison remains available for short names, embedded zero bytes, and Unicode folds that change UTF-8 length. Equal byte strings use the runtime equality fast path.
- Folder and Extension sorts counting-partition the existing compact ranks, then use the normal exact name/size order within each group. Histograms are bounded; very sparse ranks fall back to the general key sorter. This adds no persistent per-file index or Node fields.
- Reversing a numeric/name sort reverses the existing rows and restores each equal-value run in disjoint parallel ranges. Query, scope, snapshot, column, and metric must agree before reuse. An unchanged order reuses its immutable results directly.
- Plain substring search folds ASCII while searching instead of first scanning the whole filename for non-ASCII text. Unicode encounters retain the full lowercase fallback, including length-changing folds.
- Complete file selections reuse their immutable result set without hashing a million IDs at each explicit checkpoint. Partial selections retain fingerprint checks; query changes intersect selections with the new results. Replacing a bulk selection with one item discards the old map instead of clearing its buckets. Constructing Select All still uses the UI library’s map and remains a substantial cost.
- Collapse All drops hidden sorted-child caches while retaining the current folder’s children. Re-expanding those hidden folders sorts them again, trading a bounded repeat cost for releasing the cache.
- A snapshot change cancels and rejects old File/Extensions work, clears stale cache identities, derived selection facts, formatted cells and duplicate results, and resets a treemap tied to the obsolete snapshot. Stable row IDs and selections are retained. If the treemap deliberately uses the unfiltered base snapshot, that valid base is preserved. New filtered/base copies still coexist when the product requires both.
- Numeric first-sort algorithms, extension summarization, scanner I/O, and frame painting were not rewritten in this pass. Small fluctuations in those rows should not be presented as new rendering/scanning improvements. Substring-search gains are smaller than the sort-direction gains.

## Verification and reproduction

The comparison harness recorded 484 SHA-256 digests of complete outputs, including order, against the committed baseline: **zero mismatches**. Targeted tests compare adaptive/ranked/reversed sorts with the original comparator and cover Unicode, duplicate IDs, shared prefixes, sparse ranks, cancellation, cached selections, collapse/re-expansion, and obsolete queued workers. The new snapshot-lifetime tests check stale cache references, queued completions, selection preservation, and the deliberately unfiltered treemap.

The final portable build passed all package tests, Go vet, MyGO UI lifetime checks, and compilation. All ten worker benchmark invocations and ten UI reports in the final five pairs passed. `gofmt` and `git diff --check` are clean.

Raw results, metadata, binary inputs, comparison scripts and `summary.json` are under `build/throughput-pass/` (ignored build artifacts). Final five-pair results are under `build/throughput-pass/final/`. Earlier trial results in the parent directory are excluded from all tables.

```powershell
$env:QUARRY_PERF_SNAPSHOT = (Resolve-Path build/useful-pass/baseline.quarry).Path
.\.tools\go\bin\go.exe test ./internal/desktop '-run=^$' '-bench=^BenchmarkReal(Sorts|FileQueries|Extensions|SortToggle)$' '-benchtime=1s' -count=5
.\.tools\go\bin\go.exe test ./internal/desktop -run TestPerfReport -v
.\.tools\go\bin\go.exe test ./... -count=1
powershell -NoProfile -File scripts/build.ps1 -Portable
```

The portable build runs tests, Go vet, the MyGO UI lifetime checker, and compilation. The race detector is unavailable in the current CGO-disabled toolchain. No GPU-presentation, live-scan-throughput, or peak-working-set claim is made.
