#!/bin/sh

set -eu

usage() {
	echo "usage: $0 --evidence-dir DIR" >&2
	exit 2
}

evidence_dir=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--evidence-dir)
		evidence_dir=${2:-}
		shift 2
		;;
	*) usage ;;
	esac
done
[ -n "$evidence_dir" ] || usage

for command in dpkg-query git go python3 sha256sum tar; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "native document backend qualification requires $command" >&2
		exit 2
	}
done
export GOSUMDB=off
export GOTOOLCHAIN=local
export GOPROXY=off

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_commit=$(git -C "$repo_root" rev-parse --verify HEAD)
[ -z "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ] || {
	echo "native document backend qualification requires a clean source tree" >&2
	exit 2
}
runtime=$(go env GOOS)/$(go env GOARCH)
[ "$runtime" = "linux/amd64" ] || {
	echo "native document backend qualification is admitted only on linux/amd64" >&2
	exit 2
}
if [ -e "$evidence_dir" ] && [ -n "$(find "$evidence_dir" -mindepth 1 -print -quit 2>/dev/null)" ]; then
	echo "evidence directory must be absent or empty" >&2
	exit 2
fi
mkdir -p "$evidence_dir"
chmod 700 "$evidence_dir"
evidence_dir=$(CDPATH= cd -- "$evidence_dir" && pwd)
case "$evidence_dir/" in
"$repo_root/"*)
	echo "evidence directory must be outside the source tree" >&2
	exit 2
	;;
esac

scratch=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-native-backend-qualification.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
binary=$scratch/mintclaw
snapshot=$scratch/source
archive=$scratch/source.tar

mkdir -p "$snapshot"
git -C "$repo_root" archive --format=tar --output="$archive" "$source_commit"
tar -xf "$archive" -C "$snapshot"
rm -f -- "$archive"

(cd "$snapshot" && go build -buildvcs=false -o "$binary" ./cmd/mintclaw)
MINTCLAW_HOME=$scratch/home "$binary" document capabilities --json >"$evidence_dir/capabilities.json"

dpkg-query -W -f='${binary:Package}\t${Version}\n' \
	bubblewrap poppler-utils libpoppler134 ghostscript libgs-common fonts-dejavu-core fonts-liberation \
	| LC_ALL=C sort >"$evidence_dir/packages.txt"
sha256sum /usr/bin/bwrap /usr/bin/pdftotext /usr/bin/pdftoppm /usr/bin/pdfinfo /usr/bin/gs \
	>"$evidence_dir/executables.sha256"

fixture_output=$evidence_dir/fixtures.txt
if ! (cd "$snapshot" && go test -count=1 -v \
	-run '^(TestReadBackendFixtureOutcomes|TestPDFCPUFormWriteBackendProducesVerifiedFlattenedHybridDerivative|TestDocumentNativeBoundaryDeniesHostFileAndNetwork|TestProcessReaderUsesPinnedPopplerAndAdoptsVerifiedArtifacts|TestProcessFormWriterUsesRealSubprocessAndAdoptsPrivateCandidate|TestProcessFormWriterProducesVerifiedFlattenedHybridDerivative)$' \
	./pkg/document) >"$fixture_output" 2>&1; then
	cat "$fixture_output" >&2
	exit 1
fi

commit=$(git -C "$repo_root" rev-parse --verify HEAD)
[ "$commit" = "$source_commit" ] &&
	[ -z "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ] || {
	echo "source tree changed during native document backend qualification" >&2
	exit 1
}
python3 -I - \
	"$evidence_dir/capabilities.json" \
	"$evidence_dir/packages.txt" \
	"$evidence_dir/executables.sha256" \
	"$fixture_output" \
	"$evidence_dir/result.json" \
	"$commit" <<'PY'
import json
import pathlib
import sys

capabilities_path, packages_path, digests_path, fixtures_path, result_path, commit = sys.argv[1:]
capabilities = json.loads(pathlib.Path(capabilities_path).read_text(encoding="utf-8"))


def require(condition, message):
    if not condition:
        raise SystemExit("native document backend qualification failed: " + message)


packages = {}
for line in pathlib.Path(packages_path).read_text(encoding="utf-8").splitlines():
    package, revision = line.split("\t", 1)
    packages[package] = revision
expected_packages = {
    "bubblewrap": "0.9.0-1ubuntu0.3",
    "fonts-dejavu-core": "2.37-8",
    "fonts-liberation": "1:2.1.5-3",
    "ghostscript": "10.02.1~dfsg1-0ubuntu7.9",
    "libgs-common": "10.02.1~dfsg1-0ubuntu7.9",
    "libpoppler134:amd64": "24.02.0-1ubuntu9.9",
    "poppler-utils": "24.02.0-1ubuntu9.9",
}
require(packages == expected_packages, f"package revisions differ: {packages!r} != {expected_packages!r}")
digests = {}
for line in pathlib.Path(digests_path).read_text(encoding="utf-8").splitlines():
    digest, path = line.split(None, 1)
    digests[path] = digest

backends = capabilities.get("backends", [])
require(
    {backend["identity"]["name"] for backend in backends} == {"poppler", "ghostscript"},
    f"unexpected backend set: {backends!r}",
)
for backend in backends:
    identity = backend["identity"]
    require(backend["state"] == "supported", f"backend is not supported: {backend!r}")
    require(
        identity["isolation_mode"] == "bubblewrap_document_worker_v1",
        f"backend isolation differs: {identity!r}",
    )
    require(
        packages.get(identity["package"]) == identity["package_revision"],
        f"backend package identity differs: {identity!r}; packages={packages!r}",
    )
    for executable in backend["executables"]:
        require(
            executable["sha256"] == executable["observed_sha256"],
            f"runtime executable digest differs: {executable!r}",
        )
        require(
            digests.get(executable["path"]) == executable["sha256"],
            f"recorded executable digest differs: {executable!r}; digests={digests!r}",
        )
require(
    digests.get("/usr/bin/bwrap") == "e318903862396f96de3df57264e0158682b952fd3fb53ac23d876413e7b30f71",
    f"bubblewrap digest differs: {digests!r}",
)

fixture_output = pathlib.Path(fixtures_path).read_text(encoding="utf-8")
fixture_tests = [
    "TestReadBackendFixtureOutcomes",
    "TestPDFCPUFormWriteBackendProducesVerifiedFlattenedHybridDerivative",
    "TestDocumentNativeBoundaryDeniesHostFileAndNetwork",
    "TestProcessReaderUsesPinnedPopplerAndAdoptsVerifiedArtifacts",
    "TestProcessFormWriterUsesRealSubprocessAndAdoptsPrivateCandidate",
    "TestProcessFormWriterProducesVerifiedFlattenedHybridDerivative",
]
for test in fixture_tests:
    require("--- PASS: " + test in fixture_output, f"fixture did not pass: {test}")

result = {
    "schema_version": "mintclaw.document_native_backend_qualification.v2",
    "runtime": capabilities["platform"] + "/" + capabilities["architecture"],
    "mintclaw_commit": commit,
    "go_environment": {
        "GOSUMDB": "off",
        "GOTOOLCHAIN": "local",
        "GOPROXY": "off",
    },
    "backends": backends,
    "isolation": {
        "mode": "bubblewrap_document_worker_v1",
        "package_revision": packages["bubblewrap"],
        "executable": "/usr/bin/bwrap",
        "executable_sha256": digests["/usr/bin/bwrap"],
        "denials": ["unmounted_host_file", "host_network"],
    },
    "packages": packages,
    "fixture_tests": fixture_tests,
    "rollback": {
        "mintclaw_commit": commit,
        "packages": packages,
    },
}
pathlib.Path(result_path).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

echo "MINTCLAW_DOCUMENT_NATIVE_BACKEND_QUALIFICATION_OK"
echo "evidence_dir=$evidence_dir"
echo "mintclaw_commit=$commit"
