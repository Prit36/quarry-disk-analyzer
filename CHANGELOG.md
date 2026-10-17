# Changelog

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

Builds remain unsigned. Physical-volume elevated NTFS performance, physical monitor transitions, screen-reader narration and Windows high-contrast validation remain release-environment checks; see `docs/UPGRADE.md` and `design-qa.md`.

## 0.0.1

- Initial native Windows disk-analyzer release.
