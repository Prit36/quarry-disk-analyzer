# Changelog

## 0.1.1 — 2026-10-17

- **NTFS sizes.** Sparse named streams count the space they occupy, not their length: `$BadClus` no longer shows the size of the whole volume and `$Extend` (the USN journal) no longer tens of gigabytes, so drive totals are no longer inflated past the disk size. Sparse files keep their logical size.
- **Treemap context menu.** Commands act on the tile that was right-clicked. Before, the menu followed the pointer, so Open, Show in Explorer, Copy, Delete or Go Into could do nothing or act on the previous selection. The menu names its tile and adds Copy Name, Up One Level and Search in This Folder.
- **Treemap hover.** A tip beside the pointer, without delay, names the tile with its path, size, share, type and date; the tile is outlined. Previously one delayed tooltip sat at the top of the map. The treemap bar names the folder shown.
- **Explorer.** Show in Explorer and Open on folders start File Explorer as its own process when Quarry runs as administrator, where the shell call opened nothing. A drive root opens the drive. Show in Explorer is limited to 8 folders at once.
- **NTFS metadata is hidden by default.** `$MFT`, `$LogFile`, `$BadClus`, `$Extend` and the other filesystem records of MFT scans, plus records whose folder could not be resolved, are no longer listed or counted in totals, searches, extensions or the treemap. The information bar says "NTFS metadata hidden" (its tooltip gives the size); Options → Show NTFS Metadata Files lists them again. When shown, they no longer offer Open, Show in Explorer or Properties, which failed and wrongly marked the results as outdated.
- **Status bar** shows the full path and size of the row or tile under the pointer, or of the focused entry, instead of "Ready". Notifications give way after 5 seconds; errors stay.
- **Extensions view** highlights the chosen extension's tiles in the treemap and dims the rest.
- Rows under the pointer are easier to follow; Advanced Search's scope also updates Extensions.

## 0.1.0 — 2026-10-17

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

## 0.0.3 — 2026-10-17

- Nested treemap tiles are now clearly distinct: depth-aware borders and stable within-family shading replace gaps that blended into the parent fill, in both the live map and the PNG export.
- Distribute hierarchy detail with a shared budget across folders so one large directory cannot consume the entire visualization; compact deep-level headers preserve space for their children.
- Extend nested-selection visibility and app-wide detail polish: sunburst rings, file-type bars, selected-row metadata and statistics separators.
- Add regression coverage for nested tile edges and selection, zoom clamping, viewport zoom/pan and exported nested tiles; refresh the reference-comparison QA evidence.

## 0.0.2 — 2026-10-17

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
