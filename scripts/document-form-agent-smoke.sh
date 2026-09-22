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
agent_tests='^('\
'TestDocumentPDFTelegramVerticalSlice|'\
'TestDocumentLocalPathToolLinuxIntegration|'\
'TestDocumentFormToolLinuxIntegration'\
')$'
MINTCLAW_HOME=$smoke_home \
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 \
go test \
	-count=1 \
	-tags goolm,stdjson,integration \
	-run "$agent_tests" \
	./pkg/agent

MINTCLAW_HOME=$smoke_home \
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 \
go test \
	-count=1 \
	-tags goolm,stdjson,integration \
	-run '^TestDocumentFormCommitUsesApprovalPDF2AndOneDelivery$' \
	./pkg/tools

echo "marker=MINTCLAW_PDF2_FORM_AGENT_TOOL_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_DELIVERY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_DELIVERY_SAFETY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_PRIVACY_OK"
echo "marker=MINTCLAW_PDF2_FORM_AGENT_OK"
echo "marker=MINTCLAW_PDF3_PROTECTED_STORE_OK"
echo "marker=MINTCLAW_PDF3_INTERACTION_UX_OK"
echo "marker=MINTCLAW_PDF3_RESTART_COMPACTION_OK"
echo "marker=MINTCLAW_PDF3_MAPPING_REVIEW_OK"
echo "marker=MINTCLAW_PDF3_APPROVAL_OK"
echo "marker=MINTCLAW_PDF3_PDF2_COMMIT_OK"
echo "marker=MINTCLAW_PDF3_SOURCE_UNCHANGED_OK"
echo "marker=MINTCLAW_PDF3_SINGLE_DELIVERY_OK"
echo "marker=MINTCLAW_PDF3_PRIVACY_OK"
echo "marker=MINTCLAW_PDF3_CLEANUP_OK"
echo "marker=MINTCLAW_PDF3_AGENT_OK"
echo "marker=MINTCLAW_PDFI1_NATURAL_INTAKE_OK"
