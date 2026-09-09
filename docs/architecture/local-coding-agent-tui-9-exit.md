# Local Coding Agent TUI.9 Exit Record

Roadmap packet:
[TUI.9 — File-change and diff cells](local-coding-agent-codex-tui-roadmap.md#tui9--file-change-and-diff-cells).

The merge containing this record closes TUI.9. The previously merged
[typed-evidence checkpoint](local-coding-agent-tui-9-evidence.md) established
the immutable, provenance-safe repository observation. This follow-up renders
that exact historical evidence as a bounded, palette-aware diff instead of
reconstructing a patch from current workspace state or model prose.

## Shipped behavior

- Compact cells retain the truthful `Repository diff observed` summary,
  target, aggregate counts, per-file statistics, and baseline provenance. The
  full cell adds typed hunk headers, line numbers, explicit `+`/`-` signs,
  rename paths, and binary, submodule, symlink, omission, truncation, stale,
  unavailable, warning, and indeterminate-provenance states.
- The main transcript stays compact by default. `Alt+J`/`Alt+K` selects an
  individual repository-diff cell and `Ctrl+O` expands or collapses its rich
  historical hunks in place; the separate `Ctrl+T` panel retains complete
  copy-safe plain evidence.
- Inserted and deleted logical lines carry a semantic row role independently
  from their syntax spans. At truecolor and 256-color depth, that role applies
  a background to every content span, wrapped continuation, gutter, and the
  right-side padding through the admitted terminal width. Context rows retain
  the terminal's default background.
- Light truecolor uses GitHub's `#dafbe1` addition and `#ffebe9` deletion
  backgrounds with the stronger `#aceebb` and `#ffcecb` gutters. Dark
  truecolor uses the muted `#213a2b` and `#4a221d` backgrounds. The deliberate
  256-color quantization uses dark indices 22/52 and light indices 194/224,
  with light gutters 157/217.
- ANSI-16 deliberately omits colored backgrounds and uses explicit signs plus
  green/red foregrounds. Light ANSI-16 also uses a black line-number gutter.
  No-color and copy-safe plain output retain the same signs and line numbers
  with no terminal escape sequences, so meaning never depends on color alone.
- A conservative, dependency-free highlighter recognizes common Go,
  C-family, Java, JavaScript/TypeScript, Rust, Python, shell, JSON, YAML, and
  TOML tokens. Keyword, string, number, comment, and type spans survive hard
  wrapping while the enclosing diff background is reapplied after every ANSI
  reset. Cross-extension renames highlight deleted content using the original
  path and additions using the destination path. Unknown extensions remain
  readable default text.
- Wrapping operates on grapheme clusters, expands tabs without overwide
  output, preserves combining characters where they fit, and replaces only a
  grapheme wider than the entire admitted row. Even one-column rich-color rows
  retain their semantic background and never exceed terminal width.

## Safety and authority

The passive repository evidence service remains authoritative. The renderer
does not execute Git, refresh workspace state, parse generic tool text, or
claim that MintClaw authored a change. `pre-existing`, `first observed during
thread`, `resolved since baseline`, and `indeterminate` describe observation
relative to a baseline, not authorship.

Historical cells continue to own deep-cloned typed evidence correlated to the
original turn and call ID. A later `/diff` refresh cannot mutate them. Every
path, ref, diagnostic, header, and source line was already redacted and
byte-bounded at admission; the TUI strips terminal controls again before
rendering. The rich renderer emits only its own fixed SGR palette and never
emits hyperlinks from evidence content.

## Codex reference and deliberate differences

The renderer was checked against OpenAI Codex commit
`6515a72db7a82e8cdebed940aad4ba1a159ce245`, especially
`codex-rs/tui/src/diff_render.rs`. MintClaw adopts its four-layer model of
line-number gutter, sign, syntax content, and full-row background; the same
fallback RGB/256 palettes; foreground-only ANSI-16 degradation; deletion dim
overlay; grapheme-aware hard wrapping; and continuation-row styling.

MintClaw deliberately renders the P6.4 typed repository evidence rather than
Codex's patch event model. Its small deterministic lexer also avoids adding a
large parser/theme dependency to the runtime. The immutable evidence service,
strict provenance vocabulary, explicit stale/omitted states, and copy-safe
plain transcript remain MintClaw-specific safety guarantees.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/tui
go test -race -count=1 ./pkg/coding/tui
go test -count=1 ./pkg/coding/controller ./pkg/coding/frontend/... \
  ./pkg/coding/picker ./pkg/coding/plan ./pkg/coding/review \
  ./pkg/coding/reviewer ./pkg/coding/thread ./pkg/coding/tui \
  ./pkg/coding/worker
scripts/pre-push-lint.sh --changed
```

The complete local `go test -count=1 ./pkg/coding/...` attempt passed every
package before `pkg/coding/workspace` reached its ten-minute timeout inside
the unchanged
`TestCaptureDoesNotInspectDirtySubmoduleContentFilters/clean` external
`git submodule add`. The PR CI remains authoritative for that unrelated
package.

Focused semantic tests prove truthful typed states, hunk and line-number
rendering, syntax-role retention, immutable historical evidence, and the
absence of false `Edited` authorship. Inspectable goldens cover dark/light
truecolor, dark/light ANSI-256, dark/light ANSI-16, and light no-color output.
They prove exact palettes, distinct light gutters, syntax styling under the
row background, wrapped-row backgrounds, full-width padding, unstyled context
rows, and explicit signs. Additional tests cover one-column through ordinary
widths, Unicode, combining characters, wide graphemes, and tab expansion.

## Exit-gate decision

TUI.9's acceptance criteria are satisfied. Semantic generic MCP/tool cards
remain owned by TUI.10; turn hierarchy, status/composer polish, unified
evidence navigation, accessibility hardening, and migration/performance gates
remain TUI.11 through TUI.15.
