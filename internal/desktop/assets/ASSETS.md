# Interface asset provenance

- Navigation/action/table SVGs: Microsoft Fluent UI System Icons, pinned at `a347af95478f989038cccbf085978865264c048a`, from `assets/<icon name>/SVG/`. The existing `resources/licenses/fluent-icons-LICENSE.txt` is distributed with the app. Source: https://github.com/microsoft/fluentui-system-icons/tree/a347af95478f989038cccbf085978865264c048a/assets
- `brand.png`: supplied reference image, exact crop `(18,9)-(40,31)` used at 20×20 DIPs in the application title. The original screenshot is the visual authority, not the earlier procedural green launcher mark.
- Colored drive and folder images: Windows Shell stock icons obtained once through `SHGetStockIconInfo` and `DrawIconEx`. These are OS-provided runtime assets, with Fluent SVG fallbacks on other platforms. No filesystem traversal is performed to fetch icons.
- Native Windows caption controls are retained by the platform and are not replaced with painted replicas.
- `document_text_color.svg` retains the upstream Fluent geometry; its gradient palette is adjusted to the reference's purple total-file metric.
- `document_gray/yellow/purple/green/red/cyan.svg` are palette-only variants of the upstream `document_color.svg`; the official paths, folded corner and shading are unchanged. These preserve the reference's extension-icon categories without hand-drawn replacements. `database_color.svg` similarly uses a blue palette for allocated size.
- Treemap and sunburst shapes are live data visualizations, not raster decorations or recreated screenshot tiles.
