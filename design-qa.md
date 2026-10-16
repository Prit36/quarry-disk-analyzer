# Stratum 0.0.1 visual QA

final result: passed

## Visual target and evidence

Source visual truth: `build/design-audit/wiztree-reference.png`, captured in this run from the [official WizTree screenshot](https://antibodysoftware-17031.kxcdn.com/images/wiztree_treeviewwithmap4.jpg), linked from its homepage.

Implementation: `build/design-audit/final/completed-dark.png`. Additional states: `completed-light.png`, `minimum-dark.png`, `empty.png`, and `mft-unavailable.png` in that directory. These are fresh screenshots from the actual MyGO native renderer and a deterministic **synthetic scan fixture**, not claims about disk data or MFT throughput. The native Windows application was also launched and inspected with an actual project-folder scan; its scan strip correctly reported Folders.

Full-view comparison: `build/design-audit/comparison-full.png` contains both the source and implementation in one comparison image. Focused table comparison: `build/design-audit/comparison-table.png`. Minimum-size and light-theme comparison: `build/design-audit/comparison-states.png`.

Source capture: 1280 × 720 pixels at browser density 1, including black margins and WizTree's native title/menu chrome. Implementation: 1280 × 720 native content pixels at scale 1; minimum content viewport 1000 × 600. No stretch or resampling was used to construct comparisons. The source's outer window frame is excluded from spacing conclusions. Different scan contents and expansion states prevent pixel-for-pixel data comparison. The target is the tool's composition and density, with the user's requested reduction in copy and decoration, rather than a branded clone.

## Initial audit

Fresh baseline: `build/design-audit/before/native-preview.png`.

- **P1 — Analysis space displaced by dashboard decoration.** The original 210-pixel sidebar, large promotional heading, four summary cards and generous outer margins left a small data region. The reference places scan controls and analysis panes directly against the window. Fix: replace the dashboard with a 42-pixel toolbar, 30-pixel scan strip, split tables and treemap.
- **P1 — MFT failure hidden by slower scanning.** The prior interface reported directory fallback only in Details. Fix: refuse fallback by default, keep method visible, and show an actionable MFT-unavailable state with an explicit elevation control.
- **P1 — Map hid descendant files.** The original showed only immediate directory children. Fix: retain nested directory membership and render file rectangles within folder groups.
- **P2 — File results were incomplete under other sorts.** A size-based 10,000-file cap preceded column sorting. Fix: sort all matching files and retain virtualization.

## Comparison history

1. The first compact implementation removed the dashboard and restored the two tables plus map. Its minimum-size capture showed extension Files data clipping at the right edge. The table rows were also wider vertically than the reference's utility density.
2. Fixed the default split to leave 32% for the extension table; reduced row content from 21 to 16 pixels; retained 12-pixel Segoe UI names and 11-pixel numeric text. Added sortable extension headers and corrected allocated-size ranking.
3. Fixed map/table selection synchronization, visible selection strokes, and contrast-aware labels on colored tiles. Captured all states again and inspected the combined full-view, table-region and minimum/light comparisons.
4. The final captures show no clipped persistent controls or extension counts at the minimum viewport. Data, headers, scrollable rows, splitters and map stay within their panes. Remaining differences from WizTree are intentional: original branding, flatter map rendering, fewer summary fields, different fixture data and a more compact toolbar.

## Required surfaces

- **Typography:** Segoe UI, restrained hierarchy, consistent UI sizing, single-line names and numeric alignment. Long paths and names truncate within their panes; map labels are clipped to tiles and tooltips expose full names. Source outer chrome and its screenshot compression are not treated as font mismatches.
- **Layout:** No marketing area, sidebar or card grid. Primary controls remain visible at 1000 × 600; both splitters resize; all table columns fit the default minimum-size layout. Narrow user-dragged table panes retain native horizontal scrolling.
- **Color:** Neutral dark surfaces, thin gray borders, blue selection/action color. File-type colors are limited to extension swatches and data rectangles. Light mode preserves equivalent hierarchy. Map label color is chosen by relative luminance. No decorative gradients or shadows were introduced.
- **Assets:** Original Microsoft Fluent System Icons are embedded as licensed SVG files. No source logo was cloned. The treemap is a data visualization drawn with the native painter, not a raster decoration.
- **Copy:** No slogan, onboarding paragraph, reassurance banner or “native engine” branding. Persistent text is control labels, data, paths, scan method and status. The extra MFT message appears only when it is needed to explain a blocked scan.

## Interaction checks

Automated native tests pass for expansion, File view, filtering, Details, theme switching, minimum sizing, MFT-unavailable visibility and map/table synchronization. Separate scanner tests cover all-file results, sorting, hard links, sparse data, junctions, cancellation and fragmented MFT continuation mapping. The live Windows window exposed its controls through UI Automation.

The OS-owned UAC approval was not automated. Physical-volume MFT throughput, secure-desktop behavior, screen-reader narration, Windows contrast themes and additional DPI configurations remain release-validation gaps. The visual pass does not certify accessibility compliance or assert that MFT performance matches WizTree.

## Follow-up polish

P3: add user-persisted table and splitter positions. This does not block the current analysis workflow.
