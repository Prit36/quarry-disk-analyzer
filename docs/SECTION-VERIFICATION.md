# Guideline verification — sections 1–26

The specification has 26 numbered sections and six implementation phases in section 24. The six-phase implementation record remains in `UPGRADE.md`. This second pass checks every numbered section sequentially against current source, behavior and rendered evidence; earlier completion claims are not substitutes for fresh checks.

For each section: enumerate required behavior, distinguish suggestions/optional features, inspect implementation, close gaps, verify, record evidence, then move to the next section. Cross-cutting requirements remain binding throughout; section 26 is the final acceptance gate.

| Section | Topic | Status | Evidence / next action |
| --- | --- | --- | --- |
| 1 | Mission and fundamental principles | Complete | Architecture reinspected; unchanged scanner contract, real immutable data, bounded worker-backed presentation. Fresh full tests/build pass. New million-entry viewport test passes with 200 rendered text elements and functional Home/End navigation. |
| 2 | Overall application structure | In progress | Verify six-region geometry, independent panes, both splitter directions and resize bounds. |
| 3 | Visual design system | Pending | |
| 4 | Application title bar | Pending | |
| 5 | Navigation and scan toolbar | Pending | |
| 6 | Drive summary strip | Pending | |
| 7 | Explorer and file table | Pending | Known audit finding: explicit Size sorting must remain logical when visualization uses allocated bytes. |
| 8 | Analytics panel | Pending | |
| 9 | Contextual quick statistics | Pending | |
| 10 | Interactive treemap | Pending | |
| 11 | Additional visualization modes | Pending | |
| 12 | Status bar and issues | Pending | |
| 13 | Keyboard shortcuts | Pending | |
| 14 | Context-aware analytics state | Pending | |
| 15 | Search and advanced filters | Pending | |
| 16 | Scan engine and data consistency | Pending | |
| 17 | Large-dataset performance | Pending | |
| 18 | Windows integration and accessibility | Pending | |
| 19 | Polish and micro-interactions | Pending | |
| 20 | Responsive window layout | Pending | |
| 21 | Persisted preferences | Pending | |
| 22 | Optional advanced features | Pending | Distinguish optional additions from implemented-feature obligations. |
| 23 | Features to avoid | Pending | |
| 24 | Implementation strategy | Pending | Verify phase gates and architecture documentation. |
| 25 | Validation and testing | Pending | Full interaction, scale, performance, filesystem and edge-case matrix. |
| 26 | Final acceptance criteria | Pending | Requirement-by-requirement completion audit; no unverified blanket completion claim. |

## Section 1 — scope and evidence

- Scanner ownership: `internal/scan` builds the filesystem index off the UI thread and publishes an immutable snapshot. The redesign retains both explicit folder enumeration and the existing direct NTFS reader; it does not replace the engine with presentation-layer filesystem traversal.
- Data ownership: the desktop consumes indexed stable IDs, generation-tagged background query/analytics/visualization results and bounded treemap branches. Scope, navigation, selection and metric are separately represented.
- Fidelity: actual logical/allocation/capacity data is retained. Synthetic data appears only in explicitly named test fixtures/evidence. No deletion or cleanup control is exposed.
- Verified: `go test ./...`, `go build -o build/upgrade/sections-check.exe .`, and `TestMillionEntryViewportStaysVirtualized` pass. The million-entry fixture retains every indexed row while rendering only 200 text elements at the end viewport; Home/End navigation reaches both extremes. Detailed component requirements remain separately gated by their numbered sections and final acceptance.
