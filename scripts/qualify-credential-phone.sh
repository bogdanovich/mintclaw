#!/usr/bin/env bash
set -euo pipefail

# Synthetic ordinary-owner setup and virtual passkeys only. The existing
# gateway owns phone approval; no Root console or credential service is used.
repo_root="$(git rev-parse --show-toplevel)"
env -u OP_SERVICE_ACCOUNT_TOKEN -u DEBUG -u PWDEBUG -u NODE_OPTIONS \
  go test -race -tags goolm,stdjson -count=1 \
  "$repo_root/cmd/mintclaw/internal/credentials"

# Install pinned Playwright dependencies and Chromium before running.
env -u OP_SERVICE_ACCOUNT_TOKEN -u DEBUG -u PWDEBUG -u NODE_OPTIONS \
  MINTCLAW_SYNTHETIC_PHONE_BROWSER_FIXTURE=1 \
  go test -tags goolm,stdjson -run '^TestPhoneBrowserFixture$' -count=1 -timeout=110s \
  "$repo_root/internal/credentials/approval"
