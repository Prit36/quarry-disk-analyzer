# Common interaction latency — 18 October 2026

This pass uses **1a9e250** as its baseline, including the preceding performance
pass. `git fetch origin` confirmed no new upstream commits before editing.
The archived baseline and optimized source use identical benchmark harnesses
and the same saved C:\ scan: **1,452,146 nodes**, including 1,115,623 files and
336,522 folders. The original directory scan reported 125 unreadable items.
No rescan, changed dataset, discarded matches, or reduced result limits is
used to produce a speedup.

Environment: Windows/amd64, Go 1.27.2, Intel Core i5-12400F, 12 logical
processors. Final measurements alternate baseline and optimized versions in
three pairs. Tables report medians. Allocation MB are decimal; heap MiB use
2^20 bytes. Headless UI timings include application work, layout, text shaping
and painting into the display list, and exclude GPU/window presentation.

## Changes

1. **Search computes result totals and metadata eligibility during its walk.**
   `SearchResults` preserves `Search`'s complete ordered matches, while adding
   independent per-piece sums. Matched folder ancestry suppresses duplicate
   bytes without a large hash map or another result/ancestor traversal. Two
   temporary bits per node carry coverage and NTFS metadata/unresolved ancestry;
   atomic word updates allow independent workers to touch adjacent packed bits.
   Unmatched leaves need no summary work. The scope itself remains excluded.
2. **Select All reuses the worker's metadata classification and result IDs.**
   Its first frame no longer walks the selected files' ancestors. The cache can
   borrow the immutable result slice; it drops that alias before any later
   selection rebuild could write into it. Existing command validation and
   protected-path checks remain in place.
3. **Small selections release former bulk-selection maps.** The UI library's
   `Selection.Clear` retains its map. The cache rebuilds that map when a
   selection previously exceeded 16,384 items and now contains at most 4,096.
   This preserves every selected key and releases storage retained after
   selecting a whole drive. Ordinary small selection changes need no rebuild.
4. **Common numeric sorts scatter packed words.** Sizes/timestamps whose
   varying key bits and ID tie breaker fit together in 64 bits use two word
   arrays, reducing scratch from 20 to 16 bytes per result. Common constant high
   key bits are omitted without changing order. Full-width keys retain the
   separate-key fallback. Eleven-bit digits reduce passes; ID ties remain
   ascending in both directions.
5. **Name comparisons skip a prefix already ordered by radix sorting.** Each
   equal-prefix group checks once whether its first sixteen bytes are ASCII
   and fully present, then compares only the suffix. Short and Unicode-prefix
   names retain the full comparator. ID preordering remains enabled for name
   sorts because experiments showed that identical names benefit from it.
6. **Folder-path ordering runs in parallel without a merge buffer.** Bounded
   partition levels divide the path references into disjoint ranges, then use
   the standard library sorter. Equal paths remain equivalent for ranking.
   The path references use four-byte arena offsets instead of machine-sized
   offsets, shrinking each reference from 24 to 16 bytes. Derived-key tie
   comparisons already resolve IDs, so their redundant ID radix passes are
   skipped. Cancellation is checked during partitioning and comparisons.
7. **Metric changes and partial-selection totals classify ancestors once.**
   `topLevelEntries` retains input order while memoizing selected-ancestor
   coverage in the existing shared scratch pool. It returns the original slice
   without allocating when no selected item is a folder.

## Measurements

An independent [five-pair recheck](PERFORMANCE-RECHECK.md) reruns these changes
with one-second worker benchmarks and alternating pair order. Its fresh results
and run-to-run ranges confirm the main latency and retained-memory gains below.

Final measurements are recorded in `build/latency-pass/final-*`, with medians
in `final-summary.json`.

Real snapshot worker benchmarks, including complete query application:

| Operation | Baseline ms | Optimized ms | Baseline MB allocated | Optimized MB allocated |
|---|---:|---:|---:|---:|
| All files, largest first | 47.29 | 24.63 | 31.40 | 27.39 |
| Search `*.dll` | 14.45 | 10.02 | 2.25 | 2.41 |
| Search `microsoft` | 28.65 | 18.61 | 7.59 | 5.04 |
| Search `type:folder` | 41.43 | 12.19 | 19.29 | 8.60 |
| Search `path:system32 ext:dll` | 10.99 | 11.57 | 1.84 | 2.20 |
| Sort Name | 99.95 | 87.72 | 40.23 | 40.23 |
| Sort Folder | 130.49 | 102.88 | 80.69 | 79.24 |
| Sort Size | 19.54 | 16.65 | 22.35 | 17.97 |
| Sort Modified | 18.62 | 16.67 | 22.35 | 17.96 |
| Sort Extension | 56.69 | 57.02 | 40.79 | 40.79 |

Headless UI measurements, with the same rendering sequence in both builds:

| Interaction | Baseline ms | Optimized ms |
|---|---:|---:|
| Show the full file list | 63.32 | 41.03 |
| Search `*.dll` and display results | 24.52 | 20.01 |
| Search `microsoft` and display results | 40.01 | 28.61 |
| Search folders and display results | 51.02 | 22.51 |
| Search `path:system32 ext:dll` and display results | 19.39 | 19.62 |
| Sort Name and display results | 124.82 | 110.52 |
| Sort Folder and display results | 182.56 | 160.06 |
| Sort Modified and display results | 34.08 | 31.48 |
| Worst of first five frames after Select All | 39.63 | 8.02 |
| Menu synchronization after invalidating bulk-selection facts | 33.11 | 2.51 |
| Select All command itself | 76.14 | 78.14 |
| Reduce bulk selection to one item | 3.01 | 3.50 |
| File idle frame, median | 1.501 | 1.501 |
| File scrolling frame, median | 2.001 | 2.003 |
| Treemap hover frame, median | 1.501 | 1.501 |
| Extensions ready | 28.05 | 28.04 |

The broad folder query is **3.40x faster** in the worker benchmark, with **55%**
less allocation. Full file-query preparation is **1.92x faster**. The delayed
first bulk-selection frame is about **4.94x faster**. Ordinary frame times and
Extensions readiness are effectively unchanged; there is no universal-frame
speedup claim.

The selective path query's worker benchmark costs an additional **0.58 ms** and
**0.36 MB**, now including metadata eligibility formerly computed on selection.
Its displayed-result latency changes by 0.23 ms. DLL search allocates 0.16 MB
more while completing faster. These are explicit tradeoffs of preparing bulk
selection facts during the query, rather than hidden regression omissions.
The two-bit ancestry workspace is roughly 0.36 MB for this snapshot and is
temporary. No persistent per-file search index or snapshot-node growth is added.

| Heap after GC | Baseline MiB | Optimized MiB |
|---|---:|---:|
| Scan installed | 117.95 | 117.95 |
| All files selected | 160.65 | 154.99 |
| Selection reduced to one item | 153.60 | 135.02 |
| End of the full interaction sequence | 222.13 | 203.53 |
| Compact index storage | 105.35 | 105.35 |

The retained-heap reduction after deselection is **18.59 MiB**. The final
interaction sequence retains **18.60 MiB less**, about 8.4%. These are Go heap
measurements, rather than process working-set or peak-memory measurements.

## Verification and limits

The test suite covers total/metadata parity with independent ancestor-walk
references across scopes, removed entries, MFT metadata, unresolved entries,
both metrics and 1/2/12 workers. Numeric tests cover constant keys, high IDs,
duplicate IDs, descending order and packing boundaries through 64-bit keys.
Name tests compare against the original comparator around Unicode, short
names, NUL padding and prefix boundaries. Folder sorting tests cover random,
sorted, reversed and duplicate inputs and cancellation. Selection tests cover
map compaction, keyboard/mouse changes and immutable-result alias protection.

`go test ./... -count=1` passes. The unchanged complete-output parity harness
records **484 SHA-256 digests** in each source revision, covering full ordered
searches, sorts, file models, selection behavior, analysis and treemap output;
all digests match. The portable build runs the tests, Go vet, MyGO lifetime
checks and compilation before producing `build/portable/Quarry.exe`.

The race detector is unavailable in this environment: CGO is disabled and no C
compiler is installed. Parallel result state uses atomics; other writes are
limited to disjoint slices, with workers joined before publishing results.

These changes target query preparation, result sorting, selection feedback and
retained selection memory. They do not change scan engines or claim a scan
throughput improvement. Ordinary idle, scrolling and treemap frame times were
already low; the report distinguishes those from the delayed first frame after
a bulk action. Select All still constructs the UI library's per-ID selection
map on the UI thread, so its command cost remains a separate limitation.

## Reproduction

Archive revision `1a9e250` into a separate directory. Copy the current
`internal/desktop/useful_perf_test.go` and `perf_test.go` into it so both builds
use the same harness. Set `QUARRY_PERF_SNAPSHOT` to the same saved scan, then
alternate these commands in the baseline and optimized directories:

```powershell
$env:QUARRY_PERF_SNAPSHOT = 'C:\path\baseline.quarry'
.\.tools\go\bin\go.exe test ./internal/desktop -run '^$' `
  -bench '^BenchmarkReal(FileQueries|Sorts|SelectionFacts)$' `
  -benchmem -benchtime=500ms -count=1
$env:QUARRY_PERF_OUT = 'C:\path\ui.json'
.\.tools\go\bin\go.exe test ./internal/desktop -run '^TestPerfReport$' -count=1 -v
$env:QUARRY_PARITY_OUT = 'C:\path\parity.json'
.\.tools\go\bin\go.exe test ./internal/desktop -run '^TestOriginalOutputParity$' -count=1 -v
```

Use an absolute Go executable path for the archived checkout, which does not
contain the ignored `.tools` directory. The UI report measures memory after
reducing a whole-scan selection to one item in both versions; its final heap
numbers should not be compared directly with older reports using a different
sequence. Saved scans and raw reports stay in the ignored local build folder.
