# Linux keyboard qualification checkpoint

Status: private, unmerged backend and separate experimental lifetime probe.
Recorded October 31, 2026 (November 1 UTC). Public native input and production
desktop control remain disabled. This is a qualification record, not approval
to widen a target scope or enable a provider.

## Committed component results

Draft [PR #1813](https://github.com/bogdanovich/mintclaw/pull/1813), head
`9ff9acd58b453b3640df50842cc8462c0befe089`, contains the private native X11
keyboard mechanism. Its backend files are unchanged from
`1cc646fd2494771ac00325208bb84e952668e2d0`; the later commit merges the relevant
main security baseline. The draft has no public input host or model grant.

The [exact-head pinned native CI job](https://github.com/bogdanovich/mintclaw/actions/runs/41263539825/job/124721489854)
passed twenty rounds: 220 native lifecycle cases, 20 actual GTK caret/Unicode
checks, 200 process cases and 160 race-enabled Go host cases. Every round
includes a late all-master root grab after the first held key, with no further
permit. All twenty late-grab cases passed; the log audit found no race report.
The private repeat guard changes only the newly created master and XTEST slave.

These are component repetitions, not twenty distinct tasks or a full-driver,
window-manager, host, model or production qualification. Other CI checks and
review remain separate gates. Passing this job cannot override the findings below.

## Blocking findings

The experiments preserve distinct failure evidence:

- A passive root grab can redirect committed core-event input away from the
  admitted window. Focus and resource identity alone do not establish delivery
  scope. An XI2-only prototype also needed actual grab-route and native
  keyboard-action/map validation; changing the event route alone was insufficient.
- Before repeat suppression, a late grab received seven autorepeat keypresses
  without another host permit. The committed owned-repeat guard addresses that
  autonomous effect, while complete routing remains unqualified.
- After the private pointer entered a child of the GTK target, the committed
  worker returned a complete receipt with cleanup confirmed, then GTK fatally
  reported XIChangeCursor/BadDevice. The routing prototype also triggered this
  failure when it refused before any held key. Removing devices did not prove
  that the application survived.

The [sixth architecture checkpoint](https://github.com/bogdanovich/mintclaw/pull/1813#issuecomment-6551824655)
chose to investigate resource ownership separate from action authority. The PR
remains intentionally incomplete and must not be promoted or merged on component
test results alone.

## Separate lifetime hypothesis

The [experimental result](https://github.com/bogdanovich/mintclaw/pull/1813#issuecomment-6552252574)
is outside PR source. Directly disabling XTEST is rejected by Xorg. Disabling
the private master instead floats its original slave and clears master state,
but a held key remains in that slave. The failed runs are retained.

The later probe fences the original input client, clears private focus,
disables the original private master, and releases actually held keys on its
now-floating original slave. It retains the original channel owner through an
explicit quiescence barrier. An independent Xlib client queries both native key
states and observes GTK before the fixture intentionally closes its own windows.
Actual device disposal happens only after that separate fixture step.

Six single-round GTK smokes passed on worker SHA256
`81d37b88c5f361b454ed8479cca9daa70cc2dc71d5ed4ff78892ea91da749b79`:

| Case | Independent result |
| --- | --- |
| Normal original-caret Unicode | Exact target text, unchanged guard, native states clear |
| Held-key Stop | Native states clear; GTK survives retention |
| Frozen input worker | Stop reaches quiescence; native states clear |
| Crashed input worker | Quiescence and later disposal verified |
| Pointer-descendant grab refusal | No held edge; GTK survives retention |
| Pointer-descendant held-key Stop | Native states clear; unrelated grab does not receive input |

All cases verify original/core controls and focus during retention, original
owner liveness, retained device identity and pair absence after explicit fixture
window destruction. Maximum measured Stop-to-quiescence among the three Stop
cases was 12.68 ms; this is not a universal latency guarantee. Independent checks
replace neither host authority nor the durable invocation lifecycle.

The probe's disposal identity check is deliberately not represented as an
epoch-safe production implementation. Cleanup map/action history, channel-owner
and transport loss, retention/reuse, simultaneous actor loss, selected window
managers and actual host composition remain unqualified. No prototype code has
been adopted and no committed cleanup test or timeout has been weakened.

## Remaining roadmap gates

Keep input quiescence, retained resource ownership and actual disposal distinct
in trusted receipts. Retention must not retain permission to issue more input.
An absent or uncertain owner/cleanup receipt remains unknown and must not permit
replay, stale-resource adoption or an unannounced wider target grant.

Mac [PR #1797](https://github.com/bogdanovich/mintclaw/pull/1797) still needs its
mandatory exact-head ownership test through the actual approved companion app.
The operator selected desktop-free work, so that native gate remains deferred.
Existing successful Mac input effects do not substitute for this ownership test.

The [computer-use roadmap](computer-use-roadmap.md) still requires qualified
native mechanisms joined to full principal/policy/control/freshness authority,
the existing no-replay ledger and protected payload transport, interactive
continuity, twenty distinct tasks separately per OS, and opt-in pilot health
and rollback validation. A provider or scope proposal requires its own decision;
this checkpoint changes no production configuration or grant.
