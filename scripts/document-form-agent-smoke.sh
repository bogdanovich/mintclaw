#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime=$(go env GOOS)/$(go env GOARCH)
if [ "$runtime" != "linux/amd64" ]; then
	echo "document form agent smoke: skipped on $runtime"
	exit 0
fi

smoke_home=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-document-form-agent.XXXXXX")
trap 'rm -rf -- "$smoke_home"' EXIT HUP INT TERM

cd "$repo_root"
agent_tests='^(TestDocumentPDFTelegramVerticalSlice/'
agent_tests=$agent_tests'(verified_form_fill_is_delivered_exactly_once_without_retaining_values|'
agent_tests=$agent_tests'definite_form_delivery_rejection_is_recorded_without_replay|'
agent_tests=$agent_tests'ambiguous_form_delivery_stops_the_turn_without_replay)|'
agent_tests=$agent_tests'TestDocumentFormToolLinuxIntegration)$'
MINTCLAW_HOME=$smoke_home \
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 \
go test \
	-count=1 \
	-tags goolm,stdjson,integration \
	-run "$agent_tests" \
	./pkg/agent

echo "marker=MINTCLAW_PDF2_FORM_AGENT_TOOL_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_DELIVERY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_DELIVERY_SAFETY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_PRIVACY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_OK"
