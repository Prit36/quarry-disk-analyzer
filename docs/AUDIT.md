# Stratum v0.0.3 — Feature Audit (pre-overhaul)

Audit of the codebase before the native usability overhaul described in
`v2.txt`. The backend packages (`internal/scan`, `internal/ntfs`,
`internal/index`, `internal/treemap`, `internal/platform`) were reviewed for
reuse; the UI package `internal/desktop` (≈3,200 lines, one 130-field `App`
struct) was reviewed control by control.

## Architecture findings

| Area | Finding | Decision |
|---|---|---|
| Scan engines | MFT reader + parallel `FILE_ID_BOTH_DIR_INFO` enumerator, hard-link de-duplication by file ID, reparse points not followed, cancellation produces a partial index. Solid. | **Keep unchanged.** Add exclusions. |
| Automatic scan mode | Without elevation, a drive root fails with "MFT unavailable" and scans nothing. Users must find Settings → Folders. | **Implement properly:** fall back to enumeration, report the engine in the status bar. |
| Index | Flat 56-byte nodes, name arena, no pointers. Snapshot treated as immutable. No way to reflect delete/rename. | **Keep and improve:** copy-on-write mutation (remove, rename, prune). |
| Search/filter | Substring words plus `ext:`, `min:`, `max:`, `after:`, `before:`, `type:`. No wildcards, no path matching, no `size:>` syntax. | **Implement properly:** wildcard + field query engine with tests. |
| Tree query | Full async re-flatten on every expand/collapse; rows cleared while pending; selection by row index. | **Replace:** incremental flattened tree with stable IDs. |
| Treemap | Squarified nested layout, bounded budget, "other" aggregation. Good. | **Keep.** New renderer, color modes. |
| Duplicates | Size grouping → full SHA-256, hard-link aliases excluded, stale files skipped. Good. | **Keep, move to Tools menu dialog.** |
| Snapshots | Versioned gzip format, validation, compare. Good. | **Keep, expose as File → Save/Open Scan.** |
| Threading | Background work posts events; UI drains them. Correct pattern. | **Keep.** |

## UI control inventory

| Control | Status | Decision |
|---|---|---|
| Custom title band with brand | Decorative, blocks native menu bar | **Remove** – native title bar + native menu bar. |
| Back / Forward / Folder buttons | Functional | **Simplify** – Alt+Left/Right/Up + View menu; toolbar keeps Up. |
| Editable address box + Scan target drop-down (two separate path concepts) | Functional but confusing | **Simplify** – one target box (drive list + recent + browse). |
| Scan split button, Stop | Functional | **Keep.** |
| Export menu (7 formats) | Functional | **Move into menus** (File → Export). |
| Settings dialog | Mix of real and misplaced settings | **Move into menus** (View / Options / Scan). |
| Drive summary strip (Size, Allocated, Files, Folders, Last scan, volume bar) | Duplicates footer and Quick Stats | **Remove** – one compact info row. |
| Tree view / File view / Details tabs | "Details" is a text page of scan notes | **Keep Tree/File; Details → Scan → Scan Issues/Information dialog; add Extensions view.** |
| Analytics panel (Overview, File types, Top files, Duplicates) | Permanent right sidebar, duplicates File view | **Remove** sidebar. File types → Extensions view; Top files → File View; Duplicates → Tools. |
| Quick stats cards | Repeat totals 3–4 times | **Remove.** |
| Visualization tabs (Treemap, Sunburst, File types chart, History) | Sunburst and chart are decorative duplicates | **Remove** Sunburst/chart; History → File menu. |
| Treemap zoom slider and pan | Decorative/unneeded with drill-down | **Remove** – drill-down navigation instead. |
| Footer (errors, index memory, Administrator) | Index memory is diagnostic | **Simplify** – status, engine, issues link. |
| Row context menu (11 items, no Open, no Properties, no Explorer clipboard) | Partially functional | **Implement properly** via central commands. |
| Ctrl+C | Copies path text, not files | **Fix** – Ctrl+C copies shell items, Ctrl+Alt+C copies paths. |

## Missing native behaviour

Open with default app, Properties (Alt+Enter), shell clipboard copy/cut,
drag to Explorer, Recycle Bin delete, permanent delete, rename (F2),
multi-select, select all, header context menu (auto-size, reset),
expand/collapse helpers, reveal-in-tree from File View/treemap, drag a
folder in to scan, recent locations menu, font scaling, hideable info row,
Explorer "Analyze with Stratum" verb, CLI exit codes.

## Dangerous operations / UI-freeze risks

- No filesystem mutation existed; all new mutations go through
  `internal/platform` shell APIs (SHFileOperation with undo, MoveFileEx)
  after re-validating the target exists and matches the index.
- Expanding a huge subtree recursively could create millions of rows:
  expand-subtree is bounded (row budget) and the flattening is incremental.
- Sorting/filtering of whole-scan file lists runs off the UI thread.
- NTFS metadata (`$MFT`, `$BadClus`, …) is marked protected: no delete,
  rename, or "reclaimable" claims.
