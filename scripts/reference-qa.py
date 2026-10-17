import argparse
import json
from collections import Counter
from pathlib import Path

from PIL import Image


parser = argparse.ArgumentParser()
parser.add_argument("reference", type=Path)
parser.add_argument("implementation", type=Path, nargs="?")
parser.add_argument("--output", type=Path, default=Path("build/reference-match"))
arguments = parser.parse_args()
arguments.output.mkdir(parents=True, exist_ok=True)
reference = Image.open(arguments.reference).convert("RGB")
regions = {
    "title": (12, 0, 1660, 42),
    "toolbar": (12, 42, 1660, 96),
    "summary": (12, 96, 1660, 158),
    "explorer": (12, 215, 1126, 686),
    "analytics": (1134, 164, 1660, 686),
    "treemap": (12, 695, 1660, 896),
    "nested-detail": (900, 743, 1530, 890),
    "statistics": (1134, 589, 1660, 686),
}
print(json.dumps({"size": reference.size, "surfaces": {name: Counter(reference.crop(bounds).get_flattened_data()).most_common(4) for name, bounds in regions.items()}}, indent=2))
if arguments.implementation:
    implementation = Image.open(arguments.implementation).convert("RGB")
    if implementation.size != reference.size:
        raise SystemExit("Capture at the reference dimensions before comparing")
    comparison = Image.new("RGB", (reference.width * 2, reference.height), "#101820")
    comparison.paste(reference, (0, 0))
    comparison.paste(implementation, (reference.width, 0))
    comparison.save(arguments.output / "comparison-full.png")
    for name, bounds in regions.items():
        original = reference.crop(bounds)
        rendered = implementation.crop(bounds)
        combined = Image.new("RGB", (original.width * 2, original.height), "#101820")
        combined.paste(original, (0, 0))
        combined.paste(rendered, (original.width, 0))
        combined.save(arguments.output / ("comparison-" + name + ".png"))
