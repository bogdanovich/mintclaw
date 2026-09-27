#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

mode="${1:---changed}"
base="${PRE_PUSH_BASE:-origin/main}"
concurrency="${GOLANGCI_LINT_CONCURRENCY:-4}"
if [[ "$(uname -s)" == "Darwin" ]]; then
	default_cgo_enabled=0
else
	default_cgo_enabled=1
fi
cgo_enabled="${GOLANGCI_LINT_CGO_ENABLED:-$default_cgo_enabled}"
run_step() {
	local label="$1"
	shift
	local started elapsed status
	started="$(date +%s)"
	echo "pre-push: starting ${label}"
	if "$@"; then
		elapsed=$(( $(date +%s) - started ))
		echo "pre-push: completed ${label} in ${elapsed}s"
	else
		status=$?
		elapsed=$(( $(date +%s) - started ))
		echo "pre-push: failed ${label} after ${elapsed}s (exit ${status})" >&2
		return "$status"
	fi
}

module_root_for_dir() {
	local current="$1"
	while true; do
		if [[ -f "$current/go.mod" ]]; then
			printf '%s\n' "$current"
			return
		fi
		if [[ "$current" == "." ]]; then
			printf '.\n'
			return
		fi
		if [[ "$current" != */* ]]; then
			current="."
		else
			current="${current%/*}"
		fi
	done
}

append_unique() {
	local value="$1"
	shift
	local existing
	for existing in "$@"; do
		if [[ "$existing" == "$value" ]]; then
			return 1
		fi
	done
	return 0
}

lint_module_format() {
	local module_root="$1"
	shift
	(
		cd "$module_root"
		golangci-lint fmt --config "$repo_root/.golangci-format.yaml" --diff "$@"
	)
}

lint_module_packages() {
	local module_root="$1"
	shift
	(
		cd "$module_root"
		env CGO_ENABLED="$cgo_enabled" golangci-lint run \
			--config "$repo_root/.golangci.yaml" \
			--allow-serial-runners \
			--concurrency "$concurrency" \
			--build-tags=goolm,stdjson \
			"$@"
	)
}

lint_all() {
	run_step "Go formatting" golangci-lint fmt --config .golangci-format.yaml --diff
	run_step "all Go packages" env CGO_ENABLED="$cgo_enabled" golangci-lint run \
		--allow-serial-runners \
		--concurrency "$concurrency" \
		--build-tags=goolm,stdjson
}

run_step "golangci-lint config verify" golangci-lint config verify

case "$mode" in
--all)
	lint_all
	exit
	;;
--changed)
	;;
*)
	echo "usage: $0 [--changed|--all]" >&2
	exit 2
	;;
esac

if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
	echo "pre-push: $base is unavailable; falling back to full lint"
	lint_all
	exit
fi

merge_base="$(git merge-base "$base" HEAD)"

# Dependency and lint-policy changes can affect every package.
if ! git diff --quiet "$merge_base"...HEAD -- \
	go.mod \
	go.sum \
	.golangci.yml \
	.golangci.yaml \
	.golangci-format.yaml \
	.golangci-lint-version; then
	lint_all
	exit
fi

changed_dirs=()
while IFS= read -r -d '' file; do
	dir="${file%/*}"
	if [[ "$dir" == "$file" ]]; then
		dir="."
	fi

	seen=false
	if ((${#changed_dirs[@]} > 0)); then
		for existing_dir in "${changed_dirs[@]}"; do
			if [[ "$existing_dir" == "$dir" ]]; then
				seen=true
				break
			fi
		done
	fi
	if [[ "$seen" == false ]]; then
		changed_dirs+=("$dir")
	fi
done < <(git diff --name-only --diff-filter=ACMRTUXBD -z "$merge_base"...HEAD -- '*.go')

if ((${#changed_dirs[@]} == 0)); then
	echo "pre-push: no changed Go packages relative to $base"
	exit
fi

module_roots=()
for dir in "${changed_dirs[@]}"; do
	if find "$dir" -maxdepth 1 -type f -name '*.go' -print -quit | grep -q .; then
		module_root="$(module_root_for_dir "$dir")"
		if ((${#module_roots[@]} == 0)) || append_unique "$module_root" "${module_roots[@]}"; then
			module_roots+=("$module_root")
		fi
	fi
done

if ((${#module_roots[@]} == 0)); then
	echo "pre-push: changed Go files only removed packages"
	exit
fi

for module_root in "${module_roots[@]}"; do
	packages=()
	for dir in "${changed_dirs[@]}"; do
		if [[ "$(module_root_for_dir "$dir")" != "$module_root" ]]; then
			continue
		fi
		if ! find "$dir" -maxdepth 1 -type f -name '*.go' -print -quit | grep -q .; then
			continue
		fi
		if [[ "$dir" == "$module_root" ]]; then
			packages+=(".")
		elif [[ "$module_root" == "." ]]; then
			packages+=("./$dir")
		else
			packages+=("./${dir#"$module_root"/}")
		fi
	done

	echo "pre-push: linting ${#packages[@]} changed Go package(s) in module $module_root relative to $base"
	printf '  %s\n' "${packages[@]}"
	run_step "changed Go package formatting ($module_root)" lint_module_format "$module_root" "${packages[@]}"
	run_step "changed Go packages ($module_root)" lint_module_packages "$module_root" "${packages[@]}"
done
