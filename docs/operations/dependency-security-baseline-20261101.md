# Dependency Security Baseline

Date: 2026-11-01. This is a prerequisite for the bounded core-eval publication
in [#1913](https://github.com/bogdanovich/mintclaw/pull/1913), not a new agent
feature, frontend redesign, deployment or paid qualification campaign.

## Reachable Go Advisory

The unchanged main dependency `golang.org/x/net v0.60.0` exposed
[GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860), published after the immutable
October 26 eval campaigns. `utils.HtmlToMarkdown` reaches the affected HTML
parser. Upgrade to the fixed `v0.62.0`; minimum-version selection also advances
`x/crypto`, `x/sync`, `x/sys`, `x/term` and `x/text` to its required compatible
versions. No application Go implementation or historical eval result changes.

A functional compatibility case coalesces repeated text separated by ignored
end tags through the real Markdown converter. It is not a timing-dependent
performance assertion. The pinned CI-equivalent govulncheck reports zero
vulnerabilities affecting called symbols; one required module still contains
an uncalled finding. No scanner exclusion or severity filter was introduced.

## Frontend Remediation

Targeted lockfile overrides apply these published fixes without upgrading the
application's React/router versions or changing CSS imports:

| Module | Installed Before | Fixed Version | Advisory |
| --- | --- | --- | --- |
| minimatch | 9.0.9 and 10.2.6 | 11.0.1 | [GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249) |
| seroval | 1.6.9 | 1.6.11 | [GHSA-f86p-h4j2-7qgj](https://github.com/advisories/GHSA-f86p-h4j2-7qgj), [GHSA-9wx9-5pv9-3349](https://github.com/advisories/GHSA-9wx9-5pv9-3349) |
| systeminformation | 5.33.15 | 5.33.16 | [GHSA-3f7j-7jwc-fxfj](https://github.com/advisories/GHSA-3f7j-7jwc-fxfj) |
| postcss-selector-parser | 6.0.10 | 6.1.5 | [GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) |

The original audit contained seven advisory objects but eight affected-version
findings, causing the existing strict summary-count guard to refuse the report
before presenting actionable entries. Remediation removes those patched
findings. The audit checker, failure behavior, severity coverage and registry
error handling remain unchanged; malformed/unavailable evidence cannot pass.

## Remaining Accepted Risk

The checked-in exception mechanism still requires an exact GHSA, package,
version, severity, dependency path, justification and expiry. Both current
exceptions expire at **2026-11-17 00:00 UTC**:

- Existing `esbuild@0.27.4`, low, only
  `.>@tanstack/router-plugin>unplugin>esbuild`. Its previously reviewed
  development-server limitation is unchanged.
- New `deepmerge@4.3.1`, low, only `.>shadcn>deepmerge`, for
  [GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4).
  No patched release exists on this date. Repository product code imports only
  `shadcn/tailwind.css`; no shadcn JavaScript import or CLI execution is present.
  Chat/HTTP input is not sent to this merge API. This accepts a temporary
  tooling-only risk, not a claim that the package is patched. Any manual CLI
  execution, runtime import, changed version/path or expiry requires reassessment.

The now-unused braces exception and the patched selector-parser exception are
removed. A regression pins the new exact exception, checks its expiry and
rejects an added runtime dependency path. It does not add a wildcard, permanent
allowlist, ignored advisory ID or `--ignore-unfixable`/audit-level bypass.

## Validation

Frozen-lockfile install, strict audit check, production frontend build and all
52 frontend tests pass locally. Both configuration and HTML utility packages pass
with the race detector. Repository formatting, full Go lint (zero issues), frontend
lint/format, documentation lint and diff checks pass. Exact-head CI, substantive
review and owner PR-root approval remain required before merge. Historical
model-free failures and live eval failures remain unchanged.
