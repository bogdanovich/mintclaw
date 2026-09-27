#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-pre-push-lint.XXXXXX")"
temporary_dir="$(cd "$temporary_dir" && pwd -P)"
trap 'rm -rf "$temporary_dir"' EXIT

mkdir -p \
	"$temporary_dir/bin" \
	"$temporary_dir/pkg/root" \
	"$temporary_dir/nested/pkg/child"
cp "$repo_root/scripts/pre-push-lint.sh" "$temporary_dir/pre-push-lint.sh"
printf 'module example.com/root\n\ngo 1.26.0\n' >"$temporary_dir/go.mod"
printf 'module example.com/nested\n\ngo 1.26.0\n' >"$temporary_dir/nested/go.mod"
printf 'package root\n' >"$temporary_dir/pkg/root/root.go"
printf 'package child\n' >"$temporary_dir/nested/pkg/child/child.go"

git -C "$temporary_dir" init --quiet
git -C "$temporary_dir" config user.email "pre-push-lint-test@mintclaw.invalid"
git -C "$temporary_dir" config user.name "MintClaw lint test"
git -C "$temporary_dir" add .
git -C "$temporary_dir" commit --quiet -m baseline
base="$(git -C "$temporary_dir" rev-parse HEAD)"

printf '\nvar Changed = true\n' >>"$temporary_dir/pkg/root/root.go"
printf '\nvar Changed = true\n' >>"$temporary_dir/nested/pkg/child/child.go"
git -C "$temporary_dir" add .
git -C "$temporary_dir" commit --quiet -m changed

cat >"$temporary_dir/bin/golangci-lint" <<'EOF'
#!/usr/bin/env bash
printf '%s\t%s\n' "$PWD" "$*" >>"$PRE_PUSH_TEST_LOG"
EOF
chmod +x "$temporary_dir/bin/golangci-lint"

log="$temporary_dir/golangci-lint.log"
(
	cd "$temporary_dir"
	PATH="$temporary_dir/bin:$PATH" \
		PRE_PUSH_BASE="$base" \
		PRE_PUSH_TEST_LOG="$log" \
		./pre-push-lint.sh --changed
)

root_runs=0
nested_runs=0
while IFS=$'\t' read -r working_dir arguments; do
	if [[ "$arguments" != run\ * ]]; then
		continue
	fi
	case "$working_dir:$arguments" in
	"$temporary_dir":*" ./pkg/root")
		root_runs=$((root_runs + 1))
		;;
	"$temporary_dir/nested":*" ./pkg/child")
		nested_runs=$((nested_runs + 1))
		;;
	esac
done <"$log"

if ((root_runs != 1 || nested_runs != 1)); then
	printf 'unexpected module routing (root=%d nested=%d):\n' "$root_runs" "$nested_runs" >&2
	cat "$log" >&2
	exit 1
fi

printf 'pre-push lint module routing: OK\n'
