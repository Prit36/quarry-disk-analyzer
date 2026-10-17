# Architecture

```
main.go                 command line, or desktop.Run
internal/scan           MFT reader (ntfs) and directory enumerator; exclusions
internal/ntfs           MFT record parsing
internal/index          flat scan index: queries, search syntax, analysis,
                        copy-on-write mutation, snapshots, exports, duplicates
internal/treemap        bounded squarified nested layout
internal/platform       Windows services: drives, elevation, shell (Explorer,
                        Properties, Recycle Bin, rename, type names, verbs)
internal/desktop        the native UI
```

## Index

A scan is a `Snapshot`: 56-byte pointer-free `Node`s (sizes, modified time, parent/first-child/next-sibling IDs, counts) and one UTF-8 name arena. Paths are rebuilt on demand. Folders own no bytes; their totals are their children's.

Snapshots are shared with background goroutines and never mutated in place. A delete, rename, exclusion or analysis filter produces a copy with the same IDs (`Clone`, `Remove`, `Rename`, `Prune`, `Filtered`, `Reaggregate`), so selections, expansion and history survive. `Compact` renumbers a copy for saving.

`ParseFilter` compiles the search syntax (wildcards, quoted phrases, negation, `ext:`, `size:`, `modified:`, `path:`, `type:`, `regex:` with Go's linear-time RE2) into a `Filter`; `Search` walks a scope without recursion and evaluates `path:` terms incrementally per folder rather than by building paths.

## Desktop

- `app.go` — `App`: scanning, the base snapshot and the filtered view snapshot, the single navigation history (location, back, forward, up, reveal).
- `commands.go` — every user action once: label, shortcut, availability, check state, action. The native menu bar, row and treemap context menus and the keyboard dispatcher read the same table. Menu labels show shortcuts without registering native accelerators, so text fields keep Ctrl+C, Delete, etc.
- `tree.go` — Tree View model: flattened rows of the expanded tree below the location, spliced on expand/collapse, keyed by index ID; bounded subtree expansion.
- `files.go`, `extensions.go` — File View and Extensions: queries and sorts on goroutines with generation counters; results are applied on the UI thread.
- `treemap.go` — hierarchy built off-thread (6,000 tiles, 7 levels, "Smaller items" aggregates), dominant extension per aggregated block, geometry cached per size.
- `ops.go` — file operations through `internal/platform`, each re-validating the on-disk entry first, then updating the index copy-on-write.
- `view.go`, `tables.go`, `dialogs.go` — layout, grids and modal dialogs.
- `prefs.go` — persisted settings (atomic write on close).

Rule of thumb: the UI thread only reads snapshots and small models; anything proportional to the number of files runs on a goroutine and posts its result back with `App.post`.

## Safety

Deletion goes through `SHFileOperationW` (Recycle Bin with `FOF_ALLOWUNDO`, or permanent after an explicit warning), rename through `MoveFileExW` without replacement. NTFS metadata, unresolved MFT records, the scan root and Windows system locations are blocked; saved scans are read-only. The index is updated only for entries confirmed gone afterwards.

## Tests

`go test ./...` covers the scanners, NTFS parsing, the index (search syntax, mutation, snapshots, duplicates) and the desktop models, plus headless view tests with `ui.NewTester`. Interactive checks of the built executable are listed in the 0.1.0 changelog entry.
