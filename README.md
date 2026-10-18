<img src="resources/icon.png" width="96" alt="Quarry logo">

# Quarry 0.2.1

A disk space analyzer for Windows (formerly Stratum), written in Go with the MyGO native UI toolkit (Direct3D, no webview).
Quarry shows where the space went and gets you from a storage problem to the file or folder responsible in a few clicks.

## Layout

```
File  Edit  View  Tools  Options  Help                         (native menu bar)
[C:\        v] [Scan] [Stop] | <- -> ^ | C: > Users > me | [Search...      v]
Size 307.9 GiB   Allocated 308.8 GiB   1,070,517 files   331,611 folders   Volume C: ...
Tree | Files | Extensions                                     Scope: Entire scan
+--------------------------------------------------------------------------------+
| Name                       Size   Allocated   % of Parent      Files   Modified |
| v C:\                    307 GiB    308 GiB   ##### 100 %   1,070,517          |
|   > Users                152 GiB    153 GiB   ##    49 %       804,863          |
+------------------------------ splitter -----------------------------------------+
| Treemap                                 [Logical v] [Color by extension v] ^ x |
|                                  treemap                                       |
+--------------------------------------------------------------------------------+
Ready                                   1 selected · 152 GiB   Directory scan · 9 s
```

- **Tree** — the folder hierarchy below the current location, sorted by size. Right/Left expand and collapse (Shift for the whole subtree, bounded at 200,000 rows), Enter or double-click expands folders and opens files, Backspace or Alt+Up goes up.
- **Files** — every file in the scope, largest first, or every match of the search. Results are complete (no silent limit) and virtualized.
- **Extensions** — space by file type with Windows' type names. Double-click an extension to list its files.
- **Treemap** — area proportional to size, colored by extension (aggregated blocks take their dominant type), by top-level folder, or monochrome. Click selects the item in Tree View, double-click a folder goes into it, the mouse back button returns.

Selections, the location and the history are shared by the tree and the treemap. Every command lives once in a central registry that drives the menu bar, the context menus and the keyboard shortcuts (Help → Keyboard Shortcuts, F1).

## Working with files

Right-click any entry: Open, Show in Explorer (selects the items), Cut/Copy as real Explorer clipboard items, Copy Full Path (Ctrl+Alt+C), Copy Rows as Text (tab-separated), Reveal in Tree, Focus in Treemap, Search in This Folder, Scan This Folder, Rename (F2), Delete to the Recycle Bin (Del), Delete Permanently (Shift+Del, always confirmed) and Properties (Alt+Enter). Items can be dragged to Explorer; folders dropped on the window are scanned.

Before any operation Quarry checks that the path still exists and is still the same kind of entry; after it, the index, totals, extensions and treemap update without a rescan. NTFS metadata (`$MFT`, `$BadClus`, `$Extend`…), `pagefile.sys`, `System Volume Information` and similar are never offered for deletion or renaming, and items inside Windows or Program Files get an extra warning.

## Search

Type in the search box (Ctrl+F). Matching is case-insensitive; all terms must match.

```
*.mp4                     wildcards over the whole name (* and ?)
backup  -cache            contains "backup", not "cache"
ext:log size:>500MB       fields can be combined
modified:<2025-01-01      also >, <=, >=, a single day, or a range a..b
path:node_modules         any folder in the path
type:file  type:folder    regex:^IMG_\d+\.jpe?g$
```

Tools → Advanced Search builds such a query from fields. The scope (entire scan, current location or a selected folder) is always shown next to the results.

**Analysis filters** (Tools → Analysis Filter) hide entries that are already scanned — e.g. show only `ext:log`, hide `node_modules` — and recompute every total. **Scan exclusions** (Options → Scan Exclusions) keep folders or name patterns out of future scans; the info bar then reports how many entries were excluded.

## Scanning

As administrator, whole NTFS drives are read directly from the master file table (seconds for millions of files). Otherwise — and for folders, FAT/exFAT and network drives — Quarry enumerates directories with `FILE_ID_BOTH_DIR_INFO`. The status bar names the method used and why MFT reading was not possible; Options → Restart as Administrator relaunches through UAC.

Sizes: **Size** is the logical length of files, **Allocated** the disk space they occupy (smaller for compressed or sparse files). Hard links are counted once, junctions and symbolic links are not followed, and unreadable folders are reported (status bar issue count, Tools → Scan Issues). The volume's used space also contains filesystem overhead, so it is shown separately (Tools → Scan Information).

## Saving, exporting, comparing

File → Save Scan As writes a `.quarry` file that opens instantly later (marked as historical data). File → Export writes CSV of the current view, the selection, the entire scan or the extension summary, or JSON. Tools → Compare with Saved Scan shows growth per top-level folder. Tools → Find Duplicates groups same-name/same-size candidates or verifies identical content with SHA-256; it never deletes anything.

## Command line

```
Quarry.exe [folder | drive | file.quarry]
Quarry.exe -scan <folder> [-csv out.csv] [-save out.quarry] [-json] [-exclude pattern]... [-walk] [-quiet]
```

Exit codes: 0 success, 1 failure, 2 invalid arguments, 3 completed with unreadable items. Help → Command Line has the details.

## Settings

Window position, columns (right-click a header to show, hide, fit or reset), sort orders, the active view, treemap visibility and split, units, theme, text size (Ctrl+wheel, Ctrl+=/−/0), recent locations, search history and exclusions are kept in `%APPDATA%\Quarry\settings.json`. Options → Add "Analyze with Quarry" to Explorer registers a per-user context-menu verb on folders and drives; choosing it again removes it.

## Build

```
powershell -File scripts/build.ps1            # tests, vet, MyGO checks, installer in build/windows-amd64
powershell -File scripts/build.ps1 -Portable  # build/portable/Quarry.exe
```

Go 1.27 or later; no cgo. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and [CHANGELOG.md](CHANGELOG.md).
