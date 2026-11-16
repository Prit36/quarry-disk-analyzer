# Duplicate performance — 31 October 2026

The default duplicate check is 9.62× faster on the existing saved C: scan.
Preparing its results for the Files table is 5.57× faster overall and allocates
76% fewer bytes. The ordered output is identical. SHA-256 remains optional.

## Baseline and method

The baseline is the working tree at the start of this performance request,
including the previous Files menu and metadata-only default. It is **not** an
older release that enabled content verification by default. Before changing
production code, the duplicate implementation was preserved. Separate test
executables were built for both versions with identical benchmark sources.
Preserved source, binaries, profiles and raw output are in the ignored
`build/duplicate-performance/` directory.

Windows/amd64, Go 1.27.2, Intel Core i5-12400F (6 cores, 12 logical processors),
16 GiB RAM. Both versions use `GOGC=25`, matching the desktop app. Three pairs
ran sequentially in alternating order: before/after, after/before,
before/after. Each benchmark ran for at least 700 ms; values below are medians
of the three process runs. No benchmarks ran concurrently with other checks.

Snapshot construction/loading and fixture generation are outside the timed
region. Every operation recomputes duplicates without a result cache. File
verification uses warm filesystem caches. Files readiness includes duplicate
grouping, UI queue handoff, membership indexing, totals and ordering; it
excludes GPU rendering. Memory values are **total Go allocations per check**,
in decimal MB, rather than peak RAM or retained heap.

The saved scan has 1,452,146 entries and 1,115,623 files. It is the existing
`build/useful-pass/baseline.quarry` artifact, not a new drive scan. Its SHA-256
is `d47d7e07795011538cc9c01c5c174e01b281482a10d9bcb28926e95f09d3509d`.

## Results

| Check | Before | After | Speedup | Allocated MB before → after |
|---|---:|---:|---:|---:|
| Saved C: scan: metadata duplicate finder | 363.269 ms | 37.777 ms | 9.62× | 170.263 → 37.843 |
| Saved C: scan: Files results ready | 488.207 ms | 87.589 ms | 5.57× | 205.889 → 48.992 |
| 1M files, mixed sizes: metadata finder | 317.439 ms | 26.176 ms | 12.13× | 218.505 → 37.352 |
| 1M files, mixed sizes: Files results ready | 371.800 ms | 37.895 ms | 9.81× | 235.003 → 42.888 |
| 1M files, one common size | 312.319 ms | 25.317 ms | 12.34× | 218.652 → 37.350 |
| 1M files, 1,000 repeated name/size keys | 110.921 ms | 21.841 ms | 5.08× | 51.628 → 32.932 |
| 1M files, unique name/size keys | 366.448 ms | 10.737 ms | 34.13× | 286.543 → 28.413 |
| 200K files, Unicode names | 68.512 ms | 12.415 ms | 5.52× | 44.537 → 14.257 |
| Verify 512 small files in 256 size pairs | 39.969 ms | 23.481 ms | 1.70× | 1.245 → 1.214 |
| Verify 64 × 4 MiB files with a shared header | 35.110 ms | 12.188 ms | 2.88× | 0.830 → 0.731 |
| Verify 64 × 4 MiB identical copies | 35.022 ms | 34.892 ms | 1.00× | 0.824 → 0.824 |

The mixed-size synthetic fixture has 100,000 pairs and 800,000 singleton files;
the common-size and Unicode variants use the same duplicate ratio. The mixed
metadata allocation count falls from 1,905,469 to 864 per operation. Unique
keys fall from 2,008,382 allocations to 145. This is primarily the removal of
per-file lowercase strings and singleton slices.

True identical copies still require reading every byte for verification. That
case is essentially unchanged; the improvement does not apply to every content
workload. These results do not include a new whole-drive content verification,
and warm-cache timings do not predict cold-disk or network-drive latency.

All individual runs, medians, allocation counts and ratios are recorded in
[duplicate-performance-results.json](duplicate-performance-results.json).

## Changes

- Metadata matching no longer sorts all candidates by size. Workers fold and
  fingerprint names, then partition candidates among small independent hash
  tables. Full name and size equality resolves every fingerprint collision.
- ASCII names fold into reusable scratch without allocating strings. Singletons
  need no per-key list. Groups share compact ID arenas, and partition workers
  reuse the collected ID array for linked-list scratch. Large temporary tables
  are not retained in a global cache.
- Files receives immutable rows already ordered by group/name and a membership
  map sized for the actual result count. Opening the default table no longer
  iterates a growing map and sorts the same groups again. Other sorts work on
  independent copies.
- Content verification batches files across size groups, so many tiny pairs
  can use the worker pool together. Unique sizes never touch the filesystem.
  Files up to 64 KiB are read once. Larger files get 4 KiB samples from their
  start, middle and end, followed by full SHA-256 only for matching samples.
  Samples only reject candidates; they never establish equality.
- Verification keeps file-change/replacement checks, hard-link exclusion,
  bounded issue samples, cancellation and complete progress accounting.
  `DuplicateProgress.ReadBytes` separately counts actual application reads;
  the UI's byte progress still counts candidates resolved.

In the shared-header fixture, 48 files are distinct and 16 form eight pairs.
The previous algorithm requested 256.25 MiB (prefixes plus all full files);
the new algorithm requests 64.75 MiB (three samples plus the 16 remaining full
files). These byte totals follow the fixed fixture layout and are also covered
by direct read-count assertions on sampling fixtures. They are application
reads, not a measurement of physical storage traffic.

## Correctness and validation

Both executables return 95,607 groups and 415,199 candidate IDs for the saved
scan. Hashing every ordered group size, group length and ID gives the same
digest in both:

`fec83c55a433bf88b24ce8a2f77a5be802bf821712c4c3067355b1e10c648d39`

Regression checks compare sequential and parallel metadata output with an
independent lowercase-name/size map, deliberately collide all fingerprints,
cover Unicode case changes and high file-size bits, and cancel during parallel
work. Content tests compare with independent full-file SHA-256, check changes
outside all sample regions, straddle the 64 KiB threshold and batch boundaries,
and retain hard-link, changed/missing file, scope and empty-file coverage.
Desktop tests check row ordering and source immutability across sort changes.

Full tests, Go vet, MyGO lifetime checks and Linux/amd64 index cross-compilation
pass. Native headless UI tests cover the existing compact menu and file actions.
No new interactive/GPU benchmark or Windows race-detector run was performed.

```powershell
.\.tools\go\bin\go.exe test ./...
.\.tools\go\bin\go.exe vet ./...
.\.tools\go\bin\go.exe tool mygo vet

$env:GOGC = '25'
$env:QUARRY_DUP_BENCH_DIR = Join-Path (Get-Location) 'build/duplicate-performance/fixtures'
$env:QUARRY_DUP_BENCH_SNAPSHOT = (Resolve-Path 'build/useful-pass/baseline.quarry').Path

# Current source; omit the snapshot variable to run only generated fixtures.
.\.tools\go\bin\go.exe test ./internal/index '-run=^$' '-bench=^BenchmarkDuplicate(Metadata|RealMetadata|Verify)$' -benchmem -benchtime=700ms -count=3
.\.tools\go\bin\go.exe test ./internal/desktop '-run=^$' '-bench=^BenchmarkDuplicateFilesReady$' -benchmem -benchtime=700ms -count=3

# Preserved executables allow the exact before/after comparison to be repeated.
& './build/duplicate-performance/before.test.exe' '-test.run=^$' '-test.bench=^BenchmarkDuplicate(Metadata|RealMetadata|Verify)$' '-test.benchmem' '-test.benchtime=700ms'
& './build/duplicate-performance/after.test.exe' '-test.run=^$' '-test.bench=^BenchmarkDuplicate(Metadata|RealMetadata|Verify)$' '-test.benchmem' '-test.benchtime=700ms'
```
