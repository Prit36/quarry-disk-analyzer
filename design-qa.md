# Stratum — supplied-reference visual QA

## Target and evidence

- Source visual truth: `C:/Users/My_Home/.codex/attachments/90dfe854-2c4d-44cf-aebe-178eaf7cb635/image-1.png`. This supersedes the earlier WizTree baseline, retained in `docs/DESIGN-QA-BASELINE.md`.
- Latest implementation: `build/reference-match/final/reference-desktop.png`, captured from the actual native MyGO renderer, not a web mockup or painted screenshot.
- Source and implementation: **1672×941 pixels**, 1672×941 native content DIPs at scale **1**. No resizing, stretching or density correction is applied inside the comparison artifacts. OS-owned caption buttons are not present in the headless content capture; their actual native rendering is checked in the packaged application.
- Matched state: dark theme; completed scan; Tree view with root expanded and selected; File types analytics; Size metric; root treemap; default pane proportions.
- Full-view evidence: `build/reference-match/final/comparison-full.png` — source left, implementation right.
- Focused evidence in that directory: `comparison-title.png`, `comparison-toolbar.png`, `comparison-summary.png`, `comparison-explorer.png`, `comparison-analytics.png`, `comparison-treemap.png`.
- Alternate native-renderer views: `reference-minimum.png` (1000×600), `reference-intermediate.png` (1386×842), `reference-light.png` (1672×941).

The scan fixture is explicitly **synthetic**: 275 files, 81 folders, roughly 1.26 TiB. Source file counts, timestamps, issue markers, extension ordering and tile areas are not copied into production. The supplied image's displayed percentages and rectangle areas are not mutually consistent; the application retains correct indexed proportions. Comparison conclusions concern visual chrome, type, spacing and hierarchy, not equality of disk contents.

## Findings and fixes

No remaining P0/P1/P2 findings in the inspected views. The final comparison pass introduces no further visual fixes.

| Earlier finding | Severity | Fix and post-fix evidence |
| --- | --- | --- |
| Main/treemap proportions and toolbar density differed substantially | P1 | 42-DIP title, 54-DIP toolbar, 62-DIP summary, 12-DIP gutters, 68% explorer split and 71.5% vertical split. Final full-view comparison shows the same six-region composition and shallow treemap. |
| Generic controls and icon treatment drifted from the reference | P2 | Licensed Fluent controls, exact supplied brand crop, cached native shell icons, joined Scan split button, raised navigation/Stop controls, compact underline tabs and blue 12-DIP zoom thumb. Final title/toolbar/analytics/treemap comparisons inspected. |
| Invalid font family and sparse table styling | P2 | Installed Segoe UI verified through runtime font enumeration; 13-DIP body, 12-DIP metadata, 28-DIP rows, tabular digits, restrained alternating stripes, continuous blue selection and stable 44×14 percentage bars. Final explorer comparison inspected. |
| Hierarchical labels and zoom control did not match the target | P2 | Folder headers now show name/size/share when space permits; smaller directories retain useful metadata. Real zoom/pan and root reset are implemented and tested, not static chrome. Final treemap comparison inspected. |
| Narrow analytics tabs and quick-stat values wrapped or clipped | P2 | Compact tabs and two-row metrics below 500-DIP pane width; reduced footer spacing at the 1000×600 minimum. Final intermediate/minimum captures inspected, with a regression test checking metric bounds and responsive row placement. |
| Light selected tabs and rows had weak contrast | P2 | White-on-blue active tabs and dark text on pale-blue selected rows. Final light capture inspected. |
| Size column could follow allocation mode; extension sort indicator was missing | P2 | Explicit Size comparison uses logical size independently; initial extension sort uses the visible Bytes column ID. Focused regression and final header comparison pass. |
| Flat extension icons lost the reference's folded-document shading and category colors | P2 | Palette-only variants of the official Fluent document asset now retain the folded corner and gradients: gray data, yellow binary/resource, purple package/document, green executable, red disk/bitmap and cyan image/debug icons. The allocated-size asset is blue and the folder statistic uses the native folder image. The revised full/analytics comparisons were inspected. |

## Required fidelity surfaces

- **Fonts and typography:** Segoe UI is installed and selected explicitly, rather than relying on an unavailable `Segoe UI Variable` family. The supplied raster does not expose authoritative font metadata; Segoe UI is the closest grounded Windows UI match. Names and values use 13 DIPs, muted metadata 12, summary values 14, title 16 with semibold brand. Normal/compact rows are 28/24 DIPs. Single-line text, tabular numerals, baseline alignment and truncation were inspected in focused comparisons. Native rasterization remains slightly different from the supplied raster.
- **Spacing and layout rhythm:** Regions, gutters, borders, six-DIP panel radii, table/control heights and default splits match the reference composition. The explorer toolbar and table are separate surfaces. Analytics retains ten initial types, Show more and bottom statistics. The treemap header remains shallow with a right-aligned metric selector. At smaller widths, horizontal table scrolling is intentional; persistent controls remain available and statistics do not disappear behind the panel edge.
- **Colors and tokens:** Source surfaces were sampled: title approximately `#141E2B`, raised summary `#19232F`, panels `#121A23`. These drive explicit native tokens with blue action/selection and muted blue-gray metadata. Dark/light selected states were checked. Subtle tile/selection shading replaces the earlier flat mismatch; no decorative dashboard cards or unrelated accent palette was introduced.
- **Image quality and icons:** Exact reference brand crop is rendered at 20×20 DIPs. Navigation/action SVGs are original pinned Microsoft Fluent assets, with provenance in `internal/desktop/assets/ASSETS.md`; no handcrafted replacement SVGs or emoji controls are used. The total-file icon retains Fluent geometry with the reference's purple palette. Folder/drive assets come from the installed Windows Shell, cached once. These are sharp at the inspected scales but naturally differ slightly by OS version. Treemap shapes remain real data visualization, not an image asset.
- **Copy and content:** Persistent labels closely follow the source. `% of Scope` deliberately replaces `% of Disk` because analytics can follow a selected directory. Allocation/logical ratio is distinct from actual volume utilization and can exceed 100%; a regression verifies 200% is not clamped. Dynamic counts, dates, errors and map proportions are truthful. History, advanced filters and settings remain reachable without adding unrelated persistent toolbar chrome.
- **Interaction and accessibility:** Native editable address, labeled buttons, menus, tables, range slider and Windows caption controls are retained. Tests cover keyboard address/search editing, navigation/selection, zoom/pan/root reset, sorting, preference migration and million-entry virtualization. Native capture confirms real folder scans and caption controls. This is not a screen-reader, high-contrast or physical multi-monitor DPI certification.

## Comparison history

1. `build/reference-match/iteration1`: replaced the older design with measured reference geometry. Full/focused comparisons exposed treemap-header and narrow-window differences.
2. `build/reference-match/iteration2`: introduced native shell icons, directory metadata headers and actual viewport zoom/pan. Comparison exposed thin percentage bars, missing row treatment and compact tab wrapping.
3. `build/reference-match/iteration3`: restored table stripes/selection and 14-DIP bars, single-line view tabs. Subsequent minimum/light inspection exposed compact-footer clipping and selected-text contrast.
4. `build/reference-match/final`: fixed contrast, tightened minimum footer, replaced the default white slider thumb, joined Scan controls and restored the allocation ratio. A real packaged-window check at intermediate width exposed a four-column quick-stat threshold that was still too low.
5. Final recapture: raised the compact threshold, added the intermediate capture and bounds regression, aligned tree indentation/percentage bars, retained three-line smaller folder headers and fixed visible sort defaults. Reopened the source and combined full/focused outputs, then inspected minimum/intermediate/light states. No actionable P0/P1/P2 findings remain.
6. Completion audit identified flat, incorrectly colored extension icons. Preserved the original Fluent geometry in six palette variants, adjusted allocated-size blue and reused the native folder asset. Regenerated all captures and combined comparisons, inspected the revised analytics/full views and alternate sizes, and rebuilt into a separate release directory without controlling or replacing the user's running window. The post-fix comparison identifies no further P0/P1/P2 changes.

## Verification and deliverables

- `scripts/build.ps1`: full `go test ./...`, `go vet ./...`, `go tool mygo vet`, executable and NSIS packaging pass.
- Three repeated runs pass for quick-stat bounds, zoom/pan, million-entry virtualization, preference migration and keyboard editing; explicit Size/allocation sort regression also passes three runs.
- Latest versioned artifacts: `build/releases/0.0.2/windows-amd64/Stratum.exe` and `build/releases/0.0.2/windows-amd64/Stratum Setup 0.0.2 amd64.exe`. The prior `build/windows-amd64` and `build/reference-release` packages are retained. The version bump changes package metadata, not the verified interface.
- Final non-interactive gate: `go test ./...`, `go vet ./...`, `go tool mygo vet`, `go tool mygo build -o build/reference-release` and `git diff --check` pass. The original packaging script passed before the final palette-only refinement.
- Release 0.0.2 repeats tests and both static checks, then successfully packages with `go tool mygo build -o build/releases/0.0.2`.
- A final packaged-window recheck was interrupted by physical Escape; it is not represented as a successful new live check. Current visual verification uses the actual native renderer, current controls are interaction-tested, and earlier packaged-window evidence confirms native caption/file-scan integration. No further desktop automation is required or performed for this visual handoff.
- Evidence generation: `STRATUM_REFERENCE_DIR=build/reference-match/final` with `TestReferenceMatchEvidence`; paired comparisons generated by `scripts/reference-qa.py`.
- Earlier six-phase implementation evidence remains in `docs/UPGRADE.md`. The separate 26-section audit in `docs/SECTION-VERIFICATION.md` is not claimed complete by this visual pass.

## Follow-up polish and constraints

- P3: OS shell icon appearance and native caption metrics can differ from the supplied raster. Their native behavior is intentionally retained.
- P3: The source has stronger glossy icon/tile shading and some inconsistent rasterized glyphs. Original Fluent/Shell assets are used instead of tracing those artifacts; a pixel-perfect claim is not made.
- Correct scan contents, area proportions, responsive truncation and truthful issue states necessarily differ from the reference fixture.
- Physical-volume elevated NTFS throughput, signing, screen-reader narration, Windows high contrast and physical monitor transitions remain separate release-environment checks, not prerequisites for the visual-reference match.

## Implementation checklist

- [x] Match measured region geometry, hierarchy and table density.
- [x] Verify installed font, typography, semantic colors and icon provenance.
- [x] Compare combined full view and focused regions after fixes.
- [x] Inspect minimum, intermediate and light states.
- [x] Preserve functional controls and accurate indexed data.
- [x] Run regressions, static checks and Windows packaging.

final result: passed
