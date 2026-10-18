# Changelog

## Unreleased

Less on screen, nothing lost: every total was shown up to four times at once.

- **No information bar.** Size, allocated size, files and folders are in the tree's root row; the volume's free space moved to the status bar and is refreshed after deletions. Warnings (saved scan, partial scan, exclusions, outdated results, analysis filter with Clear) appear beside the view tabs only when they apply. "NTFS metadata hidden" is in the scan method's tooltip. View → Information Bar is gone.
- **Location bar works like Explorer's.** The separate scan-target box, which repeated the location, is gone. Click the empty part of the breadcrumbs or press Ctrl+L to type a path: a folder inside the scan is opened, a file is revealed in the tree, any other folder is scanned, and a saved scan file is opened. Esc or clicking elsewhere returns to the breadcrumbs.
- **Scan menu.** One Scan button lists drives with their free space, recent locations, Scan Folder… and Open Saved Scan…. Rescan becomes Stop while a scan runs, instead of a Stop button that was disabled the rest of the time.
- **Scan progress** (files, folders, elapsed time, phase and percentage) is in the status bar during a rescan and on the scanning page for a first scan.
- **Status bar** counts the selection only when more than one item is selected; a single item is already described on the left.
- **Treemap bar** no longer repeats the location and its size; the metric reads "Logical size" or "Allocated size".
- **Start page** shows the drive table and recent folders, nothing else; one click scans either. Gone: the heading and instructions, the Scan Selected Drive, Scan Folder… and Open Saved Scan… buttons (all in the Scan menu), drive roots repeated under Recent, and the administrator paragraph, now one line with a link. While the first scan runs, its progress is shown once, on the page.

## 0.2.1 - 2026-10-18

Faster searching, sorting, opening saved scans and duplicate verification. Measured on a saved scan of C:\ (1.43 million entries), 0.2.0 against 0.2.1, alternating runs, medians; every search, sort, analysis, treemap layout, JSON export and duplicate result was checked to be identical in both versions. Details in `docs/benchmark-results.json`.

- **Opening a saved scan** 397 → 86 ms. Files written by 0.2.0 and later are decoded member by member on up to eight cores; older files, and any file that does not split cleanly, are read as before. The file format is unchanged.
- **Search** is 2–4× faster on large scopes: the walk is divided between cores and the results keep their order. `size:>100MB` 12.1 → 2.8 ms, `*.log` 21 → 7.6 ms, `microsoft` 67 → 25 ms, `path:node_modules` 86 → 45 ms.
- **Search as you type** runs once per pause in typing instead of once per keystroke; the 150 ms delay was being bypassed.
- **Sorting** a million files by name 257 → 210 ms, by date 55 → 43 ms: radix passes skip digits in which every entry agrees, and name keys are computed on all cores. File View by folder 384 → 346 ms, by extension 156 → 143 ms.
- **Extensions** analysis 32 → 25 ms, with 132 allocations instead of about 110,000.
- **Duplicates** verification 30 → 16 s on 499 files of 20 MB or more: candidates are compared on their first 4 KiB before being read in full, and files are hashed on up to eight workers. On a hard disk, parallel reads may be slower.
- Smaller changes with no measurable effect in this test: grid cell text and treemap tile colors are cached between frames, availability of Cut, Rename and Delete is cached per selection, plain-name scan exclusions no longer use a regular expression, and directory scans query folders with a 256 KiB buffer.
- Directory scans (about 6.7 s) and MFT scans (about 1.7 s, with about a third less CPU) take the same time as in 0.2.0.

## 0.2.0 - 2026-10-18

Performance and memory, measured on C:\ (1.09 million files) before and after; details in `docs/benchmark-results.json`.

- **MFT scans are faster, use half the memory and are complete.** 2.3 s → 1.7 s, peak working set 360 → 184 MB. Two readers with their own unbuffered volume handles (the MFT no longer floods the file cache), records parsed into per-chunk arenas without allocating, and the `$MFT`'s `$BITMAP` sizes the index exactly and skips unused regions. Records whose folder's name is stored in an MFT extension record are now placed correctly: before, 14,755 entries (all of `C:\Windows\WinSxS`, among others) were listed at the root as unresolved, without paths.
- **Folder scans** (without administrator rights): 9.5 s → 6.7 s, peak working set 447 → 279 MB, allocations 1.15 GB → 0.45 GB. Up to eight workers instead of four, names decoded straight into pooled arenas, folders opened through a reused UTF-16 buffer, the index grown in chunks instead of by copying, and hard links found by sorting file IDs after the scan instead of a hash set during it.
- **Quarry uses far less memory.** After a scan of C:\ the app settles at about 175 MB instead of 390–440 MB. Hiding NTFS metadata no longer keeps a second copy of the index; index nodes are 48 bytes instead of 56 (modification times before 1970 are shown as 1970); the garbage collector runs before the heap doubles; scan buffers are returned to Windows when a scan ends.
- **Select All no longer freezes the window.** With a million files selected, every frame took 0.3 s and refreshing the menus 9 s; the selection and what depends on it (on disk, total size) are now cached between frames: 10 ms.
- **File View and Extensions no longer restart while loading.** Moving the pointer while a query ran restarted it, so the view could take many seconds to appear. Extensions shows its table at once (1.6 s → 65 ms) and fills in Explorer's type names in the background.
- **Sorting and searching.** Sorting a million files by size 342 → 73 ms, by name 715 → 266 ms, by extension 1.2 s → 0.18 s; `*.log` searches 213 → 33 ms.
- **Saving, loading and exporting.** Save Scan 767 → 144 ms (parallel compression; the file format is unchanged and readable by earlier releases), Open 555 → 428 ms, CSV export 1.2 s → 0.44 s with 100× fewer allocations.
- The treemap caches tile colors and labels instead of recomputing them on every frame.
- Benchmarks: `scripts/bench.go`, `TestPerfReport` and the elevated `TestMFTDiagnostics` (see `docs/ARCHITECTURE.md`).

## 0.1.1 - 2026-10-17

- **NTFS sizes.** Sparse named streams count the space they occupy, not their length: `$BadClus` no longer shows the size of the whole volume and `$Extend` (the USN journal) no longer tens of gigabytes, so drive totals are no longer inflated past the disk size. Sparse files keep their logical size.
- **Treemap context menu.** Commands act on the tile that was right-clicked. Before, the menu followed the pointer, so Open, Show in Explorer, Copy, Delete or Go Into could do nothing or act on the previous selection. The menu names its tile and adds Copy Name, Up One Level and Search in This Folder.
- **Treemap hover.** A tip beside the pointer, without delay, names the tile with its path, size, share, type and date; the tile is outlined. Previously one delayed tooltip sat at the top of the map. The treemap bar names the folder shown.
- **Explorer.** Show in Explorer and Open on folders start File Explorer as its own process when Quarry runs as administrator, where the shell call opened nothing. A drive root opens the drive. Show in Explorer is limited to 8 folders at once.
- **NTFS metadata is hidden by default.** `$MFT`, `$LogFile`, `$BadClus`, `$Extend` and the other filesystem records of MFT scans, plus records whose folder could not be resolved, are no longer listed or counted in totals, searches, extensions or the treemap. The information bar says "NTFS metadata hidden" (its tooltip gives the size); Options → Show NTFS Metadata Files lists them again. When shown, they no longer offer Open, Show in Explorer or Properties, which failed and wrongly marked the results as outdated.
- **Status bar** shows the full path and size of the row or tile under the pointer, or of the focused entry, instead of "Ready". Notifications give way after 5 seconds; errors stay.
- **Extensions view** highlights the chosen extension's tiles in the treemap and dims the rest.
- Rows under the pointer are easier to follow; Advanced Search's scope also updates Extensions.

## 0.1.0 - 2026-10-17

A rebuild of the interface as a compact Windows utility, released under a new name: **Stratum is now Quarry**, with a new logo. The scan engines and index format are unchanged.

- **Name.** Executable `Quarry.exe`, settings in `%APPDATA%\Quarry`, saved scans use `.quarry` (`.stratum` files from earlier releases still open), `QUARRY_CONFIG_DIR` overrides the settings folder.
- **Layout.** Native title bar and menu bar (File, Edit, View, Tools, Options, Help), one compact toolbar, a one-line information bar, Tree/Files/Extensions views over a resizable treemap, and a status bar. Removed: the summary strip, quick-stat cards, the permanent analytics panel, sunburst and file-type charts, the treemap zoom slider and repeated totals.
- **Commands.** One command table drives menus, context menus and shortcuts, with availability based on the selection, the scan and the item type.
- **Tree View.** Incremental expand/collapse with stable selection, Explorer keyboard behaviour, multi-select, expand/collapse one level, collapse all, bounded expand subtree, % of parent and % of scan, sortable, resizable, movable and hideable columns with auto-size and reset.
- **Explorer integration.** Open, Show in Explorer with selection, Properties (Alt+Enter), Cut/Copy as shell items, Copy Full Path (Ctrl+Alt+C), copy rows as text, drag to Explorer, drop folders to scan, Recycle Bin and permanent delete, F2 rename, optional "Analyze with Stratum" Explorer verb.
- **Search.** Wildcards, phrases, negation, `ext:`, `size:`, `modified:`, `path:`, `type:` and `regex:`; Advanced Search dialog; visible scope; search history; the File View lists every match.
- **Filters.** Analysis filters recompute totals for what remains; scan exclusions skip folders or name patterns and are reported.
- **Treemap.** Synchronized with the tree, drill-in and back, color by extension (aggregated blocks by dominant type), top-level folder or monochrome, logical or allocated size, hide, maximize.
- **Duplicates.** Candidate (name and size) or verified (SHA-256) groups in a Tools dialog, with a minimum size and scope.
- **Snapshots and CLI.** Save and open scans, compare with a saved scan; `-scan`, `-csv`, `-save`, `-json`, `-exclude`, `-quiet` with documented exit codes.
- **Behaviour change.** Automatic scanning now falls back to directory enumeration when direct MFT reading is not permitted, and says so in the status bar, instead of refusing to scan.

Verified on Windows 11 with the built executable: scans of a fixture tree and of C:\ (1.07 million files, 9 s warm, about 390 MB with a 200,000-row expansion), tree keyboard and mouse navigation, search and filters, extension drill-down, treemap selection and drill-down, back/forward, rename, Recycle Bin delete with index update, Properties, shell clipboard, duplicate verification, snapshot save/open, CLI exit codes, Explorer verb install/removal, theme and layout persistence. Not verified here: elevated MFT scans, removable drives, drag and drop with Explorer, screen readers and high-contrast themes.

## 0.0.3 - 2026-10-17

- Nested treemap tiles are now clearly distinct: depth-aware borders and stable within-family shading replace gaps that blended into the parent fill, in both the live map and the PNG export.
- Distribute hierarchy detail with a shared budget across folders so one large directory cannot consume the entire visualization; compact deep-level headers preserve space for their children.
- Extend nested-selection visibility and app-wide detail polish: sunburst rings, file-type bars, selected-row metadata and statistics separators.
- Add regression coverage for nested tile edges and selection, zoom clamping, viewport zoom/pan and exported nested tiles; refresh the reference-comparison QA evidence.

## 0.0.2 - 2026-10-17

- Rebuild the native interface around the supplied visual reference: compact title/toolbar/summary, resizable explorer and analytics panes, Segoe UI typography, Fluent and Windows Shell icons, dark/light themes and responsive statistics.
- Add scoped overview, extension distribution, top files, contextual quick statistics and verified duplicate analysis.
- Add interactive treemap zoom/pan, sunburst, file-type visualization, saved scan history and same-root comparisons.
- Add advanced indexed filters, explicit export scopes, CSV/JSON/text/PNG exports and persisted layout, columns, theme, units and recent locations.
- Preserve independent scan, navigation, selection and analysis scopes; distinguish logical size, physical allocation and filesystem capacity.
- Fix indexed navigation/selection synchronization, explicit logical Size sorting, allocation ratios and narrow-window/light-theme presentation.
- Expand regression coverage for million-entry virtualization, keyboard navigation, zoom/pan, preferences, snapshots, duplicates, exports and responsive layout.

Builds remain unsigned. Physical-volume elevated NTFS performance, physical monitor transitions, screen-reader narration and Windows high-contrast validation remain release-environment checks.

## 0.0.1

- Initial native Windows disk-analyzer release.
