# Performance and allocation pass — 26 October 2026

This pass targets remaining transient allocations in duplicate detection,
saved-scan loading and treemap layout. It preserves the saved format, complete
results, SHA-256 verification and file-operation safeguards. All work and tests
ran in the background, without opening the application or using mouse input.

## Changes

- Duplicate detection collects eligible IDs into one array and uses the existing
  cancellable size sorter to group them. The former map allocated a slice for
  every distinct size, including sizes that could never have duplicates. Equal
  sizes use ascending IDs, making hard-link representative selection stable.
  Canceled hash workers stop taking candidates before path or filesystem calls.
- Saved-scan decoding reuses each output slot's gzip reader, DEFLATE state and
  input reader. Outstanding slots now follow GOMAXPROCS plus two, capped at 14,
  instead of twice GOMAXPROCS plus two, capped at 18. Every decoder is still
  joined before closing the stream, and corrupt segments retain serial fallback.
- Snapshot validation walks the child graph from the root, checking name bounds,
  parent consistency and duplicate visits together. A final record count rejects
  disconnected records and cycles. This removes a second per-record state array
  and the separate parent-chain coloring pass. Traversal uses no recursive stack.
- Treemap layout allocates tile scratch only for depths actually rendered. The
  buffers retain room for 128 siblings to avoid repeated growth across folders.

## Measurements

Windows/amd64, Go 1.27.2, Intel Core i5-12400F, 12 logical processors. Baseline
executables were compiled from checkout `bb64bef` before production edits;
both versions use identical benchmark fixtures. Three pairs ran in alternating
order (before/after, after/before, before/after), 700 ms per benchmark. Values are
medians. Raw output and binaries are in ignored `build/memory-pass/`.

| Operation | Before | After | Allocated bytes/op before → after |
|---|---:|---:|---:|
| Duplicate candidate grouping, 100,000 unique sizes | 8.157 ms | 2.324 ms | 11.293 → 0.454 MB |
| Load saved scan, approximately 500,000 records | 71.947 ms | 72.906 ms | 68.901 → 63.382 MB |
| Decode 64 × 1 MiB gzip members | 4.774 ms | 3.883 ms | 21.673 → 15.383 MB |
| Nested treemap layout, 6,000-cell budget | 180.748 µs | 164.760 µs | 430,083 → 354,817 bytes |

Duplicate grouping allocates about 96% fewer bytes and runs about 72% faster on
this unique-size fixture. This benchmark performs no content reads. Its timing
does not describe the cost of reading and hashing real candidate files.

Full snapshot-load latency is effectively unchanged: the before range was
70.903–78.446 ms and the after range 71.975–73.617 ms. It allocates about 8% fewer
bytes. The isolated stream fixture contains highly compressible blocks and is
not a prediction of real snapshot decompression speed. Treemap allocation count
falls from 17 to 3; the renderer and visual design are unchanged.

These are total Go allocations per operation, not peak working set or retained
heap. Duplicate sorting reuses pooled numeric-sort scratch; a cold sort still
needs temporary key buffers. No globally minimum memory or latency is claimed.

## Verification

New regression checks cover candidate scopes, size thresholds, excluded flags,
incorrect saved counts, prefix/full-content duplicate groups and progress
accounting. Existing hard-link tests remain in the full suite. Decoder checks
force slot reuse across 40 members, corrupt a reused member's CRC and close a
stream without draining it. Snapshot validation is compared with an independent
breadth-first reference on 2,000 corrupted graphs, reordered IDs, a disconnected
cycle and a 100,000-level valid chain.

Run with the bundled Go toolchain:

```powershell
.\.tools\go\bin\go.exe test ./... -count=1
.\.tools\go\bin\go.exe vet ./...
.\.tools\go\bin\go.exe tool mygo vet
.\.tools\go\bin\go.exe test ./internal/index -run '^$' -bench 'Benchmark(DuplicateCandidatesUnique|SnapshotLoadMemory|SegmentStreamMemory)$' -benchmem -count=3
.\.tools\go\bin\go.exe test ./internal/treemap -run '^$' -bench 'BenchmarkNestedLayout$' -benchmem -count=3
```

Full tests, Go vet and MyGO lifetime checks pass. The new index regressions also
pass five consecutive runs. The headless UI performance report passes on the
500,051-node synthetic fixture (`build/memory-pass/ui-after.json`).
No interactive/GPU tests, new whole-drive throughput
comparison or race-detector run was performed; a C compiler for Windows race
instrumentation was unavailable on PATH. A separate optimized portable executable
is built at `build/memory-pass/Quarry.exe` without launching it.
