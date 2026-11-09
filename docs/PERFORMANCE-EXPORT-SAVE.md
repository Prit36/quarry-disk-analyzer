# Export, saving, loading and duplicate-view pass — 9 November 2026

This pass profiled every index operation on the existing 1,452,146-entry saved
C: scan (`build/useful-pass/baseline.quarry`), then targeted the slowest
remaining paths and their transient memory. Output is byte-identical: SHA-256
digests of full, scoped and row CSV exports, the JSON export, and a compacted,
saved and reloaded copy match the previous build exactly. Everything ran in
the background, without opening the application.

## Results

Windows/amd64, Go 1.27.2, Intel Core i5-12400F (12 logical processors),
`GOGC=25` as in the desktop app. Separate test executables were built before
and after the production edits from identical benchmark sources. Three pairs
ran alternately (before/after, after/before, before/after), 700 ms per
benchmark; values are medians. Other work was running on the machine, so
small differences are within noise. Memory columns are total Go allocations
per operation, not peak working set.

| Operation (saved C: scan) | Before | After | Speedup | Allocated MB before → after |
|---|---:|---:|---:|---:|
| Export CSV, whole scan | 435.3 ms | 156.4 ms | 2.78× | 1.05 → 1.06 |
| Export JSON, whole scan | 758.9 ms | 276.4 ms | 2.75× | 0.02 → 1.07 |
| Export CSV, 200,000 selected rows | 97.8 ms | 52.1 ms | 1.88× | 25.22 → 1.06 (200,009 → 4 allocations) |
| `Compact` (saving after deletions) | 121.7 ms | 36.8 ms | 3.31× | 135.77 → 116.29 (4,101 → 4 allocations) |
| Files: duplicate results ready | 134.2 ms | 96.5 ms | 1.39× | 56.45 → 49.20 |
| Files: duplicate results ready, 1M synthetic | 58.7 ms | 39.6 ms | 1.48× | 50.39 → 44.57 |
| Load saved scan (repeated in one process) | 85.2 ms | 88.7 ms | 0.96× | 166.24 → 134.83 |

Single loads in fresh processes, as the app loads a scan (five each,
alternating): the Go heap obtained from Windows falls from 175.9 MB to
142.3–146.5 MB. Load time is 87.0 ms against 83.8 ms (medians). Freshly
committed pages and one extra collection under `GOGC=25` cost about 3 ms;
the lower heap ceiling remains for the rest of the session.

## Changes

- **Exports** (`exportfmt.go`). CSV and JSON rows are formatted straight into
  one 1 MiB output buffer. Only the path can need CSV quoting; an eight-byte
  scan decides it, with `encoding/csv`'s exact rule (including leading
  Unicode space and `\.`). Times are formatted from Unix seconds with a
  civil-date calculation instead of `time.Time`. JSON strings copy runs of
  plain bytes and escape quotes and backslashes; any string with control
  bytes, `<`, `>`, `&`, invalid UTF-8 or U+2028/U+2029 is passed to
  `encoding/json` itself, so those keep their exact escaping. Row exports
  build each path in one reused buffer (`AppendPath`) instead of a string per
  row.
- **`Compact`** maps old IDs to new through a dense 4-byte-per-entry array
  rather than a 1.45M-entry map, then reuses that array for relinking.
- **Loading** reads the compressed file into memory outside the Go heap
  (`rawAlloc`, as the scan builder already does) and returns it to Windows
  as soon as decoding finishes. Everything kept is copied out of it.
  Empty, missing, oversized or changing files keep the ordinary path.
- **Duplicate view**. Group numbers are stored as `int32`, halving the
  membership map's slot size (415,199 entries on this scan).
  `groupDuplicates` looks each file's group up once and ranks groups in an
  array indexed by group number instead of a growing map.

## Not changed

Snapshot decompression (standard `compress/flate`) dominates loading and
already runs on all cores; its 88,000 allocations per load are Huffman tables
inside the standard library. Searches and sorts take 3–55 ms on this scan
after earlier passes and were left as they are.

## Verification

New tests compare the formatters with `encoding/csv`, `encoding/json` and
`time.Format` on edge cases and 5,000 random strings each. RFC 3339 output is
checked at every day boundary from 1970 to 2106 and at 200,000 random times.
Whole exports are compared with the former implementations on a tree with
quoting and escaping names. Further tests cover write errors, `Compact`
through save and reload, empty and corrupt files, and `groupDuplicates`
against the former map version (grouped and interleaved).

`go test ./...`, `go vet ./...` and `go tool mygo vet` pass. No interactive
or GPU check, Windows race-detector run or new drive scan was performed.

```powershell
.\.tools\go\bin\go.exe test ./... -count=1
$env:GOGC = '25'
$env:QUARRY_BENCH_SNAPSHOT = (Resolve-Path 'build/useful-pass/baseline.quarry').Path
$env:QUARRY_DUP_BENCH_SNAPSHOT = $env:QUARRY_BENCH_SNAPSHOT
.\.tools\go\bin\go.exe test ./internal/index '-run=^$' '-bench=^BenchmarkMax' -benchmem -benchtime=700ms -count=3
.\.tools\go\bin\go.exe test ./internal/desktop '-run=^$' '-bench=^BenchmarkDuplicateFilesReady$' -benchmem -benchtime=700ms -count=3
# Output digests for comparing builds:
.\.tools\go\bin\go.exe test ./internal/index -run '^TestMaxParityReport$' -v
```

Raw results, profiles and the before/after executables are in the ignored
`build/max-perf/` directory.
