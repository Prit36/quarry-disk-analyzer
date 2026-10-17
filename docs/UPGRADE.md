# Stratum upgrade — phase verification

The complete `upgrade guildine.txt` (sections 1–26) and attached reference image were reviewed before implementation. Existing uncommitted desktop/query work is retained and developed in place.

Common rules: native Windows behavior; real immutable scan data; distinct logical/allocation/capacity semantics; virtualized tables; background expensive work; bounded visualizations; functional controls only; no destructive file operations; compile and verify each phase before starting the next.

| Phase | State | Verification |
| --- | --- | --- |
| 1 — Foundation | Complete | Shared semantic theme/geometry/palette, Segoe UI Variable, embedded Fluent icons; `go test ./internal/desktop` and `go build .` pass. Native tester verifies 100%, 125%, 150%, 200% scale at 1000×600, 1400×850, 1920×1080. |
| 2 — Core UI | Complete | Native retained caption controls with drag title band; navigation/target separation, capacity-correct volume bar, timestamped summary, percentage-column bars, settings, issue viewer and canceled-state status. Desktop tests/build pass; dark/minimum/light render evidence in `build/upgrade/phase2`. |
| 3 — Analytics | Complete | Background contextual overview/types/top-100; heap-bounded retrieval and explicit follow-selection scope; virtualized metadata/action tables. Scoped-query, metric-order, cancellation and native synchronization tests pass; full tests/build pass. Million-file analytics: 20.9 ms, 1,568 transient bytes (single synthetic run). Render evidence `build/upgrade/phase3`. |
| 4 — Visualization | Complete | Stable folder colors, exact logical/allocated sizing, detailed metadata tooltips, selection and map-specific history/drilldown/parent zoom; bounded cached geometry, Other inspection and metadata visibility. Area/budget/snapshot-semantics/history/color tests and build pass. Evidence `build/upgrade/phase4`. |
| 5 — Interactions and persistence | Complete | Focus/select address, focus-safe navigation and copy/expand shortcuts; advanced size/type/date/scope filtering; column visibility/order/widths, atomic bounded preferences, recent paths, binary/decimal units and collapsible panels. Framework StateKey restores on-screen native window bounds/state. Round-trip/corruption/keyboard/scope/filter tests, full tests, Go vet, MyGO vet and build pass. Evidence `build/upgrade/phase5`. |
| 6 — Advanced functionality | Complete | Four-level interactive sunburst, file-type bars, cancellable full-content SHA-256 duplicate verification, compressed versioned snapshots with bounded retention, same-root comparisons, table/top-file/subtree CSV, streaming JSON, reports and treemap PNG. Integrity/corruption/graph/retention/hard-link/cancellation/export/native action tests pass. Windows executable and NSIS installer rebuilt successfully. Final render evidence `build/upgrade/phase6`. |

Native rendering evidence uses explicitly synthetic fixtures; actual filesystem integration uses temporary directories. Neither substitutes for elevated physical NTFS volume validation.

## Final validation

- `go test ./...` passes; a shuffled three-run regression pass also succeeds.
- Navigation, analytics scope and selection highlighting regressions pass 20 repeated focused runs. The final native window also confirms that address navigation retains the original scan target and global totals while contextual analytics follows the new directory.
- `go vet ./...`, `go tool mygo vet` and `git diff --check` pass.
- `scripts/build.ps1` successfully produces `build/windows-amd64/Stratum.exe` and `Stratum Setup 0.0.1 amd64.exe`, with icons and dependency licenses.
- Native Computer Use smoke tests exercise real folder results, Ctrl+L, indexed navigation, caption maximize/restore and sunburst. UI Automation exposes labeled controls, cells and scope indicators. The runtime tests found and fixed both stale selection state and a retained table-key issue; focused regressions now include the actual highlighted row.
- Headless native-renderer tests exercise 100%, 125%, 150%, 200% scale at 1000×600, 1400×850 and 1920×1080. Settings and advanced-filter actions fit the minimum viewport. Dark/light/empty/blocked and all analytics/visualization modes were rendered and inspected.
- A fresh real `internal/index` folder scan reports 10 files, 46,136 logical bytes, 65,536 allocated bytes and zero issues. Its CSV file count and logical-byte sum were independently checked. This is a small-folder integration check, not a drive throughput claim.

Final synthetic CPU measurements on Intel Core i5-12400F / Windows amd64 / Go 1.27.2:

| Check | Measurement | Scope |
| --- | ---: | --- |
| Virtualized 100,000-row desktop table | 0.662 ms/frame | 20 headless CPU frames; excludes GPU/native presentation |
| Contextual analytics over 1,000,000 files | 24.49 ms/query | Mean of three synthetic runs; bounded top-file/folder heaps, about 1.6 KiB transient allocations |
| Indexed search over 1,000,000 files | 8.07 ms/query | Mean of three synthetic runs; all matching rows retained |

## Remaining release-environment checks

Elevated physical-volume NTFS validation, real monitor DPI transitions, screen-reader narration, Windows high-contrast themes and signing remain outside this development validation. The existing scanner's synthetic NTFS, sparse-file, hard-link, junction and cancellation regressions continue passing. The implementation contains no deletion or cleanup feature, no fabricated scan metrics and no placeholder feature tabs.

Historical indexes deliberately limit individual uncompressed saves to 128 MiB and retention to five saves / 256 MiB. Duplicate checks inspect live content, skip files changed during verification and make no physical reclaimability claim. Preferences and historical saves are independent from the scan engine's immutable results.
