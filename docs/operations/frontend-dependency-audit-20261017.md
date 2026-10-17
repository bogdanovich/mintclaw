# Frontend Dependency Audit — October 2026

Status: implementation evidence for R12 of the
[reliability/code-health roadmap](../architecture/reliability-code-health-20261017-roadmap.md).
Merge and deployment are separate gates.

## Snapshots And Changes

On 2026-10-17, publication reported 56 open GitHub Dependabot alerts in the frontend
lockfile. A fresh `pnpm audit --json` on the original graph reported 69 findings:
four critical, 33 high, 26 moderate, and six low. The sources have different
snapshots; their totals are not interchangeable coverage measures.

Refresh dependencies within the previously declared version ranges using pnpm
10.33.0. The updated graph reports three findings: one high, one moderate, and
one low; zero critical. Updated parents include the TanStack router packages,
shadcn, and Vite. React remains at its pinned version. Retain and pin the original
Prettier 3.8.3 because its newer formatter would require unrelated source changes.

The router generator reordered checked-in route metadata. Comparison of its
route IDs, paths, and imports found the same sets before and after generation.

## Remaining Findings

Exceptions expire at 2026-11-17 00:00 UTC and require a fresh assessment before
renewal. They apply to the reviewed use below, not arbitrary use of the package.

| Package / advisory | Resolved version | Reviewed use and disposition |
| --- | --- | --- |
| [esbuild — GHSA-g7r4-m6w7-qqqr](https://github.com/advisories/GHSA-g7r4-m6w7-qqqr) | 0.27.4 | The affected API serves a directory on Windows. Vite/unplugin/tsx use the build tool; reviewed frontend commands do not start esbuild's HTTP development server. MintClaw serves built assets through Go. Retain the bounded tooling exception while the parent ranges converge. |
| [braces — GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) | 3.0.3 | No upstream patch is listed. shadcn, its registry and ts-morph reach the parser through fast-glob/micromatch. Those paths consume developer/tooling glob patterns. The product imports shadcn CSS; it does not send chat or HTTP input to this parser. |
| [postcss-selector-parser — GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) | 6.0.10 | Tailwind typography pins this major and parses repository selectors while compiling CSS. The affected request-time parsing of attacker-supplied selectors is outside that use. shadcn's separate 7.1.6 copy is patched. Retain the bounded exception while evaluating an upstream update or a tested major transition. |

The exposure assessment is an inference from dependency paths, the repository's
entry points, the advisory conditions, and the built output. It is not a claim
that the affected libraries are safe for other consumers or untrusted build
inputs. Review a change to those boundaries before renewing an exception.

`pnpm why --json braces esbuild postcss-selector-parser` identifies multiple
paths. The audit API reports representative paths rather than an exhaustive
call graph; the exception's path check does not replace that dependency review.

A production build witness inspected 755 JavaScript module IDs in emitted
chunks. It found zero modules from esbuild, braces, or postcss-selector-parser.
CSS remains compiled normally, including the required shadcn stylesheet. This
supports the browser-bundle assessment while the Node tooling remains installed.

## Repeatable Check

From `web/frontend` with a supported Node version and pnpm 10.33.0:

```sh
pnpm install --frozen-lockfile
pnpm audit:check
pnpm test
pnpm lint
pnpm format
pnpm build
pnpm why --json braces esbuild postcss-selector-parser
```

The PR frontend job runs `audit:check`. Its Node script bounds the registry audit
at 45 seconds and 8 MiB, rejects missing/error/inconsistent evidence, and compares
findings with `scripts/advisory-exceptions.json`. Matching requires advisory ID,
module, version, severity, every reported path, and an unexpired date. New,
changed, or expired findings require action. Registry failure does not pass.

Nine deterministic policy tests exercise expiry, changed identities/versions/
severity/paths, an additional uncovered finding, missing/error/inconsistent
reports, and invalid exception dates. They use the existing Vitest runner.

Local validation on Node 22.23.1 passed frozen installation, all 22 tests in five
files, lint, formatting, the advisory check, TypeScript, and the production
bundle. The local installation used `--ignore-scripts`; PR CI uses the normal
frozen installation. This record does not claim deployed or live browser-domain
qualification, which retains its existing roadmap owners.

To repeat the bundle witness, collect the keys of `chunk.modules` from a Vite
`generateBundle` hook during `build({ build: { write: false } })`. Search those
module IDs for `/node_modules/esbuild/`, `/node_modules/braces/`, and
`/node_modules/postcss-selector-parser/`. Keep the normal repository config and
plugins so the witness covers the actual production graph.
