# Manual Agent Evals: PDF Cancel V2 Pilot

## Scope

One new scripted `pdf.cancel.no-write` attempt used merged core `0402438bfcb647e717b2de00fa3d3e3afc60230f`,
build `eval-pdf-cancel-v2-0402438b`, Linux amd64 and Go 1.26.9. This commit contains the reviewed
[ordinary PDF question cancellation fix](https://github.com/bogdanovich/mintclaw/pull/1615).
The runner and target used the same binary and corpus archived from that commit. The native runtime probe
admitted the fresh profile before dispatch. Build time and effective provider model remain unknown.

The profile allowed only `document` and `request_user_input`, with native audit configured and approvals required.
The target requested `gemini-3.5-flash-lite`; a separate tools-disabled judge was configured for
`gemini-3.6-flash`, but no judge or LLM-user calls occurred. No output confirmation or `allow_once` was sent.
Browser, exec, MCP, external channels, cron, hooks and nodes were disabled.

Only an operator-authorized API credential was read from deployed main and placed behind native file references
in the fresh profile. No production sessions, memory, workspace configuration or browser state were copied.
Both processes used the fresh profile as `HOME` and `MINTCLAW_HOME`, with an empty inherited environment except
the declared executable path, locale and profile/oracle settings. Production services and configuration were
not changed.

## Result

Report ID: `d88075dc-a690-45b2-bd3b-22840bcfd5b2`; complete report, overall **inconclusive**, CLI exit 1.
Total reported duration: **2,125 ms**. Trial `a2856a71-ff83-4119-bf6a-a88005b8a762` took **2,113 ms**.
Case SHA-256: `231ade22ace6d089ed2f823209dc18e8e0f88ab0453e2ce26f364f3ced38bbdc`.
Runtime public-config SHA-256: `8b1df69ebeb43fe77ce0bacd6d955b07eabebcfc426ca6355f99d39869aff73f`.

Conversation execution is **pass / script_completed**. The agent emitted an ordinary question, the runner sent
one prompt-bound cancellation in the same session, and the reply was classified as `canceled` from native evidence
rather than an invented LLM trace scope. The retained cancellation witness binds prompt, delivery, request and
reply IDs, owner route and origin turn; the native question advanced from waiting revision 3 to terminal revision 5.

The first pilot's `missing_route_identity` harness defect did not recur. The original
[v1 report](manual-agent-evals-pdf-cancel-v1-pilot-20261011.md) remains unchanged. This is a new run after a reviewed
code change, not a retry-until-green or regrading of failed evidence.

The complete PDF scenario remains **inconclusive / required_check_unknown**. Both `private_surfaces` and
`pdf_no_write` are still **inconclusive / missing_evidence**; no native form job or artifact was established in
eval evidence. Judge evidence was ineligible. This validates the ordinary-question adapter fix, not protected-form
cancellation, PDF inspection/filling, no-write safety, comprehensive privacy, native idle or owned cleanup.
Target all-call usage is unknown; user/judge attempted-call counts are zero.

## Retention And Limits

Raw conversation, native state, credentials and fixtures stay outside Git in the fresh private profile. The owned
gateway PID's executable path was verified before SIGTERM; its command exited successfully and `/proc` confirmed
absence. The retained report still says `local_transports_closed; remote_state_unknown`, with the owned source
fixture retained. Local process shutdown is not a substitute for native settlement and cleanup witnesses.

The independent artifact oracle was configured but no artifact was collected or graded in this case. This run
does not qualify adaptive drivers, macOS live behavior, browser/companion, Telegram or the full suite. Protected
PDF cancellation, idle/private-surface/cleanup evidence, six PDF and six browser cases, provisioning/fingerprints,
available target usage and the exit report remain required by the active E0-E5 goal.
