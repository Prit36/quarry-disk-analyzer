# Stratum 0.0.1

A compact Windows disk analyzer written in Go with **MyGO v0.5.5 native UI**. Direct3D 11 renders the interface. There is no webview, browser frontend, HTML, JavaScript, or Node build step.

## Run

After building, open `build/windows-amd64/Stratum.exe`, or install `Stratum Setup 0.0.1 amd64.exe`. The packaged output includes dependency licenses. Go is not needed to run the application.

For **whole NTFS drives**, use **Run as administrator**, accept Windows' UAC prompt yourself, and click **Scan**. The scan strip must say **NTFS MFT**. This reads filesystem metadata directly rather than opening each folder.

Without elevation, the app reports **MFT unavailable** immediately. It does **not** silently run a slower directory scan. To deliberately enumerate folders, select **Folders** in the method selector. Selecting a folder rather than a drive root also uses folder enumeration.

## Interface

- **Open…** selects a folder. The adjacent dropdown selects a drive; the path is editable.
- **Automatic** uses MFT for drive roots and enumeration for individual folders. **Folders** explicitly chooses enumeration.
- **Tree view** expands directories. Double-click a directory to focus it. Arrow keys expand/collapse selected directories; **Alt+Up** returns to the parent.
- **File view** lists every matching file, sorted by the selected column. Results are virtualized and have no 10,000-file cutoff.
- **Filter** supports case-insensitive name terms, `ext:mp4`, `ext:none`, and `min:100MB`. For example: `backup ext:zip min:1GiB`. Search covers the whole scan.
- **Extensions** shows all extension groups, including allocated size, count and percentage. Click a row to filter files; click a header to sort.
- The nested **Treemap** shows files within directories. Click a tile to select its table row; double-click to focus its directory. **Root** restores the scan root. Switch between logical size and allocated size.
- Drag splitters to resize panes. Table columns can be resized, reordered and sorted.
- Right-click a file or directory to copy its path or reveal it in Explorer.
- **Export…** writes all indexed records to CSV. **Details** lists scan errors.
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

## Limits

Scans are live, not point-in-time snapshots. Folder mode measures the default stream; MFT mode includes data streams. Allocated totals exclude some filesystem overhead and need not equal Windows volume-used bytes. Hard-linked data is counted once. Directory links are not followed. Access errors and unresolved MFT parents are reported; unresolved filesystem actions are disabled.

The treemap is limited to 4,096 branches and seven nested levels. Remaining small children are represented by an aggregate tile. The name arena is limited to 4 GiB and record IDs to 32 bits. There is no deletion, duplicate finder, scan comparison, raw MFT export, MTP scan, or continuous watcher. macOS/Linux source fallbacks compile; runtime validation remains Windows-only.

See [architecture and release validation](docs/ARCHITECTURE.md) and [visual QA](design-qa.md).
