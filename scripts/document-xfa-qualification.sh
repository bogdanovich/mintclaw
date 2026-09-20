#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
ASSET_DIR="$ROOT_DIR/scripts/internal/document-xfa-qualification"
OUTPUT_DIR=""

usage() {
  echo "usage: scripts/document-xfa-qualification.sh --output DIR" >&2
}

while (($# > 0)); do
  case "$1" in
    --output)
      OUTPUT_DIR=${2:-}
      shift 2
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ -z "$OUTPUT_DIR" ]]; then
  usage
  exit 2
fi

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR=$(cd -- "$OUTPUT_DIR" && pwd)
if find "$OUTPUT_DIR" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  echo "output directory must be empty: $OUTPUT_DIR" >&2
  exit 2
fi

for command_name in git go node npm python3 curl; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command not found: $command_name" >&2
    exit 2
  fi
done
if ! python3 -m pip --version >/dev/null 2>&1; then
  echo "required Python module not found: pip" >&2
  exit 2
fi

SCRATCH_DIR=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-pdf4a.XXXXXX")
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$SCRATCH_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$SCRATCH_DIR/fixtures"
go run "$ASSET_DIR/generate" --output "$SCRATCH_DIR/fixtures"

PYTHON_PACKAGES="$SCRATCH_DIR/python-packages"
python3 -m pip install --disable-pip-version-check --target "$PYTHON_PACKAGES" \
  pikepdf==10.13.0.post1 beautifulsoup4==4.13.5 >"$SCRATCH_DIR/pip-install.log"
install -m 0600 "$ROOT_DIR/pkg/document/testdata/encrypted-password-required.pdf" \
  "$SCRATCH_DIR/fixtures/encrypted.pdf"

git init -q "$SCRATCH_DIR/pdf-xfa-tools"
git -C "$SCRATCH_DIR/pdf-xfa-tools" remote add origin https://github.com/AF-VCD/pdf-xfa-tools.git
git -C "$SCRATCH_DIR/pdf-xfa-tools" fetch -q --depth 1 origin da2e899e7ea3520a2ad85b2a041db5841b23b1ed
git -C "$SCRATCH_DIR/pdf-xfa-tools" checkout -q --detach FETCH_HEAD

go run "$ASSET_DIR/gate" "$SCRATCH_DIR"/fixtures/*.pdf >"$SCRATCH_DIR/gate.ndjson"

SAFE_VALUE='MINTCLAW <&> café 😀'
(
  cd "$ASSET_DIR/pdfer-probe"
  go run . --mode safe --value "$SAFE_VALUE" \
    "$SCRATCH_DIR/fixtures/static.pdf" "$SCRATCH_DIR/static-filled-pdfer.pdf"
) >"$SCRATCH_DIR/pdfer-safe.json"
(
  cd "$ASSET_DIR/pdfer-probe"
  go run . --mode safe --value "$SAFE_VALUE" \
    "$SCRATCH_DIR/fixtures/static.pdf" "$SCRATCH_DIR/static-filled-pdfer-repeat.pdf"
) >"$SCRATCH_DIR/pdfer-repeat.json"
(
  cd "$ASSET_DIR/pdfer-probe"
  go run . --mode raw-negative-control --value "$SAFE_VALUE" \
    "$SCRATCH_DIR/fixtures/static.pdf" "$SCRATCH_DIR/static-filled-pdfer-raw.pdf"
) >"$SCRATCH_DIR/pdfer-raw-negative.json"

PYTHONPATH="$PYTHON_PACKAGES" python3 "$ASSET_DIR/pike-probe.py" \
  --backend pikepdf "$SCRATCH_DIR/fixtures/static.pdf" "$SCRATCH_DIR/static-filled-pikepdf.pdf" \
  >"$SCRATCH_DIR/pikepdf.json"
PYTHONPATH="$PYTHON_PACKAGES" python3 "$ASSET_DIR/pike-probe.py" \
  --backend pdf-xfa-tools --xfa-tools "$SCRATCH_DIR/pdf-xfa-tools/xfaTools.py" \
  "$SCRATCH_DIR/fixtures/static.pdf" "$SCRATCH_DIR/static-filled-xfa-tools.pdf" \
  >"$SCRATCH_DIR/pdf-xfa-tools.json"

npm install --prefix "$SCRATCH_DIR/node-env" --no-audit --no-fund --package-lock=false \
  pdfjs-dist@6.3.289 playwright@1.63.0 >"$SCRATCH_DIR/npm-install.log"
PLAYWRIGHT_BROWSERS_PATH="$SCRATCH_DIR/browsers" \
  "$SCRATCH_DIR/node-env/node_modules/.bin/playwright" install chromium >"$SCRATCH_DIR/playwright-install.log"
install -m 0644 "$ASSET_DIR/viewer.html" "$SCRATCH_DIR/viewer.html"
install -m 0644 "$ASSET_DIR/screenshot.mjs" "$SCRATCH_DIR/screenshot.mjs"
install -m 0644 "$ASSET_DIR/pdfjs-probe.mjs" "$SCRATCH_DIR/pdfjs-probe.mjs"

QUALIFICATION_PORT=${MINTCLAW_XFA_QUALIFICATION_PORT:-18794}
python3 -m http.server "$QUALIFICATION_PORT" --bind 127.0.0.1 --directory "$SCRATCH_DIR" \
  >"$SCRATCH_DIR/http.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:$QUALIFICATION_PORT/viewer.html" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$QUALIFICATION_PORT/viewer.html" >/dev/null

PLAYWRIGHT_BROWSERS_PATH="$SCRATCH_DIR/browsers" node "$SCRATCH_DIR/screenshot.mjs" \
  "http://127.0.0.1:$QUALIFICATION_PORT" fixtures/static.pdf "$SCRATCH_DIR/before.png" \
  >"$SCRATCH_DIR/pdfjs-before.json"
PLAYWRIGHT_BROWSERS_PATH="$SCRATCH_DIR/browsers" node "$SCRATCH_DIR/screenshot.mjs" \
  "http://127.0.0.1:$QUALIFICATION_PORT" static-filled-pdfer.pdf "$SCRATCH_DIR/after.png" \
  >"$SCRATCH_DIR/pdfjs-after.json"
node "$SCRATCH_DIR/pdfjs-probe.mjs" "$SCRATCH_DIR/static-filled-pikepdf.pdf" \
  >"$SCRATCH_DIR/pikepdf-render.json" 2>>"$SCRATCH_DIR/pdfjs-probe-warnings.log"
node "$SCRATCH_DIR/pdfjs-probe.mjs" "$SCRATCH_DIR/static-filled-xfa-tools.pdf" \
  >"$SCRATCH_DIR/pdf-xfa-tools-render.json" 2>>"$SCRATCH_DIR/pdfjs-probe-warnings.log"

(
  cd "$ROOT_DIR"
  CGO_ENABLED=0 go test ./pkg/document
) >"$SCRATCH_DIR/current-refusal.log"

python3 "$ASSET_DIR/aggregate.py" "$SCRATCH_DIR" "$OUTPUT_DIR" "$ROOT_DIR"
echo "PDF4A qualification passed; evidence written to $OUTPUT_DIR"
