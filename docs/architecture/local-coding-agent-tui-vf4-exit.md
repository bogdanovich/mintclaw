# Local Coding Agent TUI VF.4 Exit Record

Roadmap packet:
[VF.4 — Concise coding response policy and reasoning truthfulness](local-coding-agent-tui-visual-followup-roadmap.md#vf4--concise-coding-response-policy-and-reasoning-truthfulness).

The merge containing this record closes VF.4. Coding answers now have an
explicit proportional response contract, while compact and detailed status
surfaces distinguish a configured `off` reasoning value from an omitted value
whose behavior belongs to the provider.

## Root-cause decision

Rendering changes how an answer is displayed; it does not make the model emit
fewer tokens. The screenshot pair was not a controlled model experiment:
although both surfaces named `gpt-5.6-sol`, Codex showed `xhigh` reasoning and
MintClaw showed `off`, which previously also represented an omitted setting.
The products supplied different base instructions, project context, tool
evidence, and conversation history.

Current Codex source places concise, adaptive final-answer and progress rules
in its base model instructions. They are not supplied by a repository skill.
MintClaw's isolated coding prompt already bounded repository exploration and
discouraged routine tool narration, but it did not define a proportional final
answer. That missing contract, rather than Markdown layout or transcript
clipping, is the bounded root cause addressed here.

## Shipped response contract

The coding-only base instructions now require the model to:

- lead final responses with the outcome and default to concise, factual depth
  proportional to the request;
- summarize a repository by purpose, major components, and useful entry points
  instead of inventorying unrelated files or directories;
- use compact Markdown only when it improves scanability and avoid repeating
  tool output, progress, or conclusions;
- keep progress updates to one or two sentences, covering completed progress
  and the next phase without narrating routine calls; and
- retain the material findings and evidence when the user explicitly requests
  a review, investigation, roadmap, or extensive analysis.

These rules extend the existing MintClaw coding prompt rather than copying a
Codex prompt wholesale or setting an arbitrary line limit. Prompt assembly
tests prove the policy is present in the stable coding prefix and absent from
the personal/always-on system prompt. Project instructions remain a separate,
later prompt layer and can still request a different response shape.

## Reasoning status ownership

The runtime already records `ReasoningConfigured` independently from its
normalized zero-value effort. Provider request construction also omits a
reasoning parameter when configuration is absent. Presentation had discarded
that distinction: the footer printed the normalized `off`, while `/status`
printed `off (default)`.

The footer now says `reasoning default` whenever configuration is absent, and
`/status` says `provider default`. An explicit configured `off`, `medium`, or
other supported value remains visible as that exact value. MintClaw does not
claim to know the provider's effective default when it did not send one.

## Controlled live comparison procedure

A useful before/after comparison must hold every other input constant:

1. Create one disposable Git repository with the same committed files and no
   changing generated output or project instructions.
2. Build the parent and candidate commits, and give each an empty thread store
   under otherwise identical MintClaw configuration and authentication.
3. Pin the same provider, model, and explicit reasoning setting. Record both
   binary versions and the `/status` facts; do not compare an omitted effort to
   an explicit one.
4. Run the exact request `Inspect this repository and summarize its structure.
   Do not modify files.` through noninteractive `code exec` for each build.
5. Compare whether the final answer leads with purpose, identifies only useful
   components and entry points, avoids unrelated path inventories, and remains
   complete. Count final lines and named paths as diagnostic evidence, not a
   hard product limit.
6. Repeat when quantitative confidence matters because provider output is not
   deterministic. Never infer a renderer or prompt regression from one run
   whose context, model snapshot, reasoning, or repository state differs.

## Regression evidence

Prompt tests assert the outcome-first, repository-summary, compact-Markdown,
phase-progress, and extensive-analysis clauses in the isolated coding prefix;
they also assert that representative coding-only clauses do not enter a
personal prompt. Status tests cover missing runtime state, omitted zero and
normalized values, explicit `off`, explicit `medium`, and invalid configured
empty values. Status-card goldens and a wide footer fixture prove the visible
wording; width bounds continue to protect narrow terminals.

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/agent ./pkg/coding/frontend ./pkg/coding/tui \
  ./cmd/mintclaw/internal/coding
go test -race -count=1 -run '^TestCodingRuntimeUsesIsolatedPromptAndSessionIdentity$' ./pkg/agent
go test -race -count=1 -run '^TestCodingResponseGuidanceDoesNotEnterPersonalAgentPrompt$' ./pkg/agent
go test -race -count=1 -run '^TestReasoningStatusDistinguishesProviderDefaultFromExplicitOff$' ./pkg/coding/tui
go test -race -count=1 -run '^TestStatusFooterLabelsUnsetReasoningAsDefault$' ./pkg/coding/tui
go test -race -count=1 -run '^TestStatusFooterKeepsStableFactsAndLeavesActivityToWorkingLine$' ./pkg/coding/tui
scripts/pre-push-lint.sh --changed
make lint-docs
git diff --check
```

## Exit-gate decision

VF.4's coding-only proportional response policy, bounded progress guidance,
personal-prompt isolation, explicit-versus-default reasoning truthfulness, and
controlled comparison procedure are satisfied. No answer is clipped to appear
concise, and no always-on agent behavior changes. Integrated visual/PTY
evidence, final documentation audit, deployment, and production smoke remain
VF.5.
