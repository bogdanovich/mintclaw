#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document read oracle: skipped on $runtime"
	exit 0
fi

clawpdf_root=${PDF1A_CLAWPDF_ROOT:-}
if [ -z "$clawpdf_root" ] || [ ! -f "$clawpdf_root/package.json" ] || \
	[ ! -f "$clawpdf_root/dist/cli.js" ]; then
	echo "document read oracle: PDF1A_CLAWPDF_ROOT must name an unpacked clawpdf 0.3.2 package" >&2
	exit 2
fi
node_binary=${PDF1A_NODE_BINARY:-node}
if [ "$($node_binary --version | sed 's/^v//' | cut -d. -f1)" -lt 22 ]; then
	echo "document read oracle: Node.js 22 or newer is required" >&2
	exit 2
fi
if [ "$($node_binary -p "require('$clawpdf_root/package.json').version")" != "0.3.2" ]; then
	echo "document read oracle: clawpdf version must be 0.3.2" >&2
	exit 2
fi

oracle_root=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-oracle.XXXXXX")
trap 'rm -rf -- "$oracle_root"' EXIT HUP INT TERM
binary=${MINTCLAW_BINARY:-$oracle_root/mintclaw}
if [ -z "${MINTCLAW_BINARY:-}" ]; then
	cd "$repo_root"
	go build -o "$binary" ./cmd/mintclaw
fi
pixel_compare=${PDF1A_PIXEL_COMPARE_BINARY:-$oracle_root/pixel-compare}
if [ -z "${PDF1A_PIXEL_COMPARE_BINARY:-}" ]; then
	go build -o "$pixel_compare" "$repo_root/scripts/internal/document-pixel-compare"
fi

for fixture in text unicode rotated-crop ambiguous-reading-order acroform; do
	production=$oracle_root/$fixture.jsonl
	production_report=$oracle_root/$fixture-production.json
	oracle=$oracle_root/$fixture-oracle.json
	MINTCLAW_HOME=$oracle_root/home "$binary" document extract \
		--input "$repo_root/pkg/document/testdata/$fixture.pdf" \
		--pages 1 \
		--output "$production" \
		--json >"$production_report"
	"$node_binary" "$clawpdf_root/dist/cli.js" extract \
		"$repo_root/pkg/document/testdata/$fixture.pdf" \
		--mode text \
		--pages 1 \
		--max-pages 1 \
		--max-text-chars 256000 \
		--json >"$oracle"
	python3 - "$production" "$oracle" "$fixture" <<'PY'
import json
import pathlib
import sys

production_path, oracle_path, fixture = sys.argv[1:]
production = "\n".join(
    json.loads(line)["text"] for line in pathlib.Path(production_path).read_text(encoding="utf-8").splitlines()
)
oracle = json.loads(pathlib.Path(oracle_path).read_text(encoding="utf-8"))["text"]
markers = {
    "text": ["MintClaw text fixture"],
    "unicode": ["MintClaw Café résumé"],
    "rotated-crop": ["MINTCLAW_ROTATED_CROP"],
    "ambiguous-reading-order": ["MINTCLAW_FIRST_VISUAL", "MINTCLAW_SECOND_VISUAL"],
    "acroform": ["Synthetic AcroForm"],
}[fixture]
for marker in markers:
    assert marker in production, (fixture, "production", marker)
    assert marker in oracle, (fixture, "oracle", marker)
PY
done

for fixture in text rotated-crop acroform; do
	production_dir=$oracle_root/$fixture-production
	oracle_png=$oracle_root/$fixture-oracle.png
	MINTCLAW_HOME=$oracle_root/home "$binary" document render \
		--input "$repo_root/pkg/document/testdata/$fixture.pdf" \
		--pages 1 \
		--output-dir "$production_dir" \
		--dpi 144 \
		--json >"$oracle_root/$fixture-render.json"
	"$node_binary" "$clawpdf_root/dist/cli.js" render \
		"$repo_root/pkg/document/testdata/$fixture.pdf" \
		--page 1 \
		--dpi 144 \
		-o "$oracle_png"
	"$pixel_compare" "$production_dir/page-0001.png" "$oracle_png" 0.08
done

echo "production=poppler version=24.02.0"
echo "oracle=clawpdf version=0.3.2"
echo "marker=MINTCLAW_PDF1A_ORACLE_OK"
