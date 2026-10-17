# Stratum 0.0.2

A Windows disk analyzer written in Go with **MyGO v0.5.5 native UI**. Direct3D 11 renders the interface. There is no webview, browser frontend, HTML, JavaScript, or Node build step.

## October 2026 upgrade

The six-phase redesign and verification record is in [docs/UPGRADE.md](docs/UPGRADE.md). The rebuilt application is `build/windows-amd64/Stratum.exe`.

- A custom title band retains Windows caption buttons, dragging, snapping and maximize/restore. Scan target, inspection location, selection, analytics scope and visualization root are separate states.
- The summary shows logical size, allocated file size and scan start time. Its volume bar uses filesystem-reported used/free capacity, independently of file totals.
- Analytics provides Overview, searchable File types, Top 100 files and explicit content-verified Duplicates. Follow selection changes analytics scope; the lower statistics describe the selected entry.
- Treemap, Sunburst and File types visualizations share the logical/allocated metric, scope and stable colors. Map Back, parent zoom, selected-folder zoom and Root navigate the visualization. An optional metadata switch changes visualization scope without altering scan totals.
- Settings remembers density, units, theme and collapsed panels. Column menus configure visibility; native header dragging resizes/reorders columns. Layout, sorting, recent locations and native window bounds/state persist across restarts.
- Advanced filters operate on indexed data: `ext:zip min:1GiB max:10GiB type:file after:2026-01-01 before:2026-12-31`. Subtree/Scan explicitly selects scope. Date boundaries use local dates; `before` includes that day. Files with missing modification dates do not match date ranges.
- History saves compressed, versioned scan indexes and restores them without filesystem rescanning. Same-root comparisons report logical or allocated growth for immediate root children. Retention is five saves or 256 MiB, with a 128 MiB uncompressed limit per index. Saves are manual; settings and history live under `%APPDATA%\Stratum`.
- Export offers current table results, top-100 results, subtree CSV, streaming JSON, a text summary, whole-scan CSV and a 1600×900 treemap PNG. Export scope is explicit and captures the immutable scan active when the command starts.
- Duplicates requires a deliberate content check. Candidate sizes are grouped, complete SHA-256 hashes confirm matching contents, hard-link aliases are excluded, changed/missing files are skipped, and the operation can be canceled. It does not estimate physical reclaimability or delete files.

Shortcuts: Ctrl+L selects the address, Ctrl+F focuses indexed filtering, Alt+Left/Right navigate history, Alt+Up visits the parent, F5 rescans, Ctrl+E opens subtree export, and Escape clears/dismisses or cancels the active interaction. Table Ctrl+C copies the selected path, Space expands/collapses and Enter inspects a folder or file. Editing retains standard text shortcuts.

## Run

After building, open `build/windows-amd64/Stratum.exe`, or install `Stratum Setup 0.0.2 amd64.exe`. The packaged output includes dependency licenses. Go is not needed to run the application. See [CHANGELOG.md](CHANGELOG.md) for version history.

For **whole NTFS drives**, use **Run as administrator**, accept Windows' UAC prompt yourself, and click **Scan**. The scan strip must say **NTFS MFT**. This reads filesystem metadata directly rather than opening each folder.

Without elevation, the app reports **MFT unavailable** immediately. It does **not** silently run a slower directory scan. To deliberately enumerate folders, select **Folders** in **Settings → Scan method**. Selecting a folder rather than a drive root also uses folder enumeration.

## Interface

- The **folder button** selects a folder. **Scan target** selects a drive independently of the editable inspection address.
- **Automatic** uses MFT for drive roots and enumeration for individual folders. **Folders** explicitly chooses enumeration.
- **Tree view** expands directories. Double-click a directory to focus it. Arrow keys expand/collapse selected directories; **Alt+Up** returns to the parent.
- **File view** lists every matching file, sorted by the selected column. Results are virtualized and have no 10,000-file cutoff.
- **Filter** supports case-insensitive name terms, `ext:mp4`, `ext:none`, and `min:100MB`. For example: `backup ext:zip min:1GiB`. Select Subtree or Scan to set the indexed search scope.
- **File types** shows extension groups for the explicit analytics scope. Click a row to filter files; click a header to sort.
- The nested **Treemap** shows files within directories. Click a tile to select its table row; double-click to focus its directory. **Root** restores the scan root. Switch between logical size and allocated size.
- Drag splitters to resize panes. Table columns can be resized, reordered and sorted.
- Right-click a file or directory to copy its path or reveal it in Explorer.
- **Export** offers scoped CSV, JSON, text summaries and treemap PNG. **Details** inspects selection metadata; the issue indicator opens the scan issue table.
- **Ctrl+O** opens a folder, **Ctrl+F** focuses the filter, **F5** rescans, and **Esc** stops a scan.

## Initial release

The native desktop utility layout uses WizTree's official interface as a structural reference and follows Product Design's audit and QA workflow. It has compact controls, resizable data panes, a virtualized table, a nested treemap and Microsoft Fluent System Icons.

The scanner refuses silent MFT-to-directory fallback. It has an explicit Windows elevation action, a compact active-method indicator, aligned raw I/O buffers, dense MFT record lookups, preallocated arenas, direct bootstrap reads of continuation records, and cycle validation without a per-record chain allocation. The file table sorts all matches. The treemap retains directory hierarchy.

## Performance evidence

Measurements are from an Intel Core i5-12400F, Windows amd64, Go 1.27.2. These are not guarantees or a same-machine comparison with WizTree.

| Measurement | Result | Scope |
| --- | ---: | --- |
| Search across one million files | 6.52 ms | Mean of 3 synthetic runs, `ext:mp4 min:900KB`, all 100,000 matches retained |
| Table backed by 100,000 rows | 0.537 ms/frame | 3 headless CPU frames; synthetic fixture, excludes native presentation |
| Earlier full C: folder scan | 9.78 seconds | Warm cache, 1.047 million file entries, 329,581 folders, 124 access errors |
| Retained index from that scan | 113.64 MiB | Records and names; excludes UI, GPU and transient structures |
| Earlier synthetic million-record index | 76 MB | Preallocated 56-byte records and UTF-8 names |

The C: measurement above was **folder enumeration**, not MFT. **Physical-volume MFT throughput remains unmeasured:** the development session is not elevated and Windows denies raw read access. The reader is integration-tested with contiguous and fragmented synthetic volume images, including MFT attribute-list continuations, file stream continuations, Unicode names and stale parent sequence numbers. A real elevated volume test is still required before making a WizTree-speed claim.

## Build and test

Requires Go **1.27.1+**. This workspace has a checksum-verified portable Go 1.27.2 under `.tools/go`; the PowerShell scripts use it if Go is absent from PATH.

```powershell
.\scripts\run.ps1
.\scripts\build.ps1
# With Go on PATH:
go run . -path "C:\Data"
go test ./...
go vet ./...
go tool mygo vet
go tool mygo build
```

MyGO creates a native executable with icon, version and DPI resources plus an NSIS installer. Artifacts are unsigned. `scripts/build.ps1 -Portable` builds a stripped executable without generated Windows resources.

Use a console build for diagnostics:

```powershell
go build -o build/stratum-cli.exe .
.\build\stratum-cli.exe -scan "C:\"               # Requires elevation; fails rather than walking
.\build\stratum-cli.exe -scan "C:\" -walk         # Explicit folder enumeration
.\build\stratum-cli.exe -scan "C:\Data" -csv "build\scan.csv"
.\build\stratum-cli.exe -scan "C:\" -fallback     # Explicit permission to fall back
```

Tests cover hard links, sparse files, junction exclusion, cancellation, aggregation, sorting beyond 10,000 files, CSV, NTFS fixups, signed runs, fragmented MFT continuation mappings, nested map budgets, native navigation, synchronized selection and minimum window size. Prior parser fuzzing completed 10.7 million inputs without failure. A Windows symlink test skips if the system denies symlink creation; the separate junction test runs without elevation.

For layout captures, set `STRATUM_DESIGN_DIR` to an output directory and run `go test ./internal/desktop -run TestDesignEvidence`. Captures use a **synthetic fixture**, not real scan totals.

For the supplied 1672×941 reference, set `STRATUM_REFERENCE_DIR` and run `go test ./internal/desktop -run TestReferenceMatchEvidence -count=1`. This captures reference-sized dark/light views, an intermediate window and the 1000×600 minimum. `scripts/reference-qa.py REFERENCE IMPLEMENTATION --output DIRECTORY` generates full and focused side-by-side comparisons. The current interface uses installed Segoe UI, 28-pixel rows, native shell file-system icons and licensed Fluent controls; scan contents and proportions always come from the index rather than the reference screenshot.

## Limits

Scans are live, not point-in-time snapshots. Folder mode measures the default stream; MFT mode includes data streams. Allocated totals exclude some filesystem overhead and need not equal Windows volume-used bytes. Hard-linked data is counted once. Directory links are not followed. Access errors and unresolved MFT parents are reported; unresolved filesystem actions are disabled.

The treemap is limited to 4,096 branches and seven nested levels; sunburst shows at most four levels with drilldown. Remaining small children are represented by an aggregate tile. The name arena is limited to 4 GiB and record IDs to 32 bits. Historical saves have the smaller documented storage limit. There is no deletion, raw MFT export, MTP scan, or continuous watcher. Duplicate checks read live files and are not an atomic content snapshot. macOS/Linux source fallbacks compile; runtime validation remains Windows-only.

See [architecture and release validation](docs/ARCHITECTURE.md) and [visual QA](design-qa.md).
