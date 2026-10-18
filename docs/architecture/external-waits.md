# Externally completed tool waits

An external wait suspends a durable task until its originating registered tool
confirms a condition. It reuses the interaction registry, recovery loop and
continuation service. The credential use case is specified in
[phone approval waiting](credential-approval-wait.md); credential wiring and
production rollout follow this foundation separately.

The tool returns `KindExternalWait` with a bounded `ConditionRef`, timeout and
optional absolute deadline. The reference is safe correlation metadata, never
an authorization token, phone-link capability or secret value. Runtime-owned
route, execution and inbound identity accompany the persisted record. Only the
originating registered tool's `ExternalWaitResolver` evaluates readiness using
that identity; model visibility expiry does not remove its lifecycle resolver.
The resolver receives the executing child session for delegated work while the
parent route remains separate for delivery. Ordinary chat answers cannot resolve
this kind. `/stop` uses normal cancellation.

Before suspension, the runtime closes turn resources and preserves native
cleanup receipts. External waits cannot carry a live resource handoff. A failed
cleanup or cancellation during cleanup prevents suspension. Questions and approval handoffs keep their existing
resource behavior.

The existing recovery tick checks waiting conditions. Pending conditions leave
the task suspended; ready/unavailable conditions claim the current revision.
Expiry, cancellation and revision checks fence late results. Duplicate polls
reuse existing continuation single-flight ownership. Lost conditions fail
closed. Timeout or unavailable conditions finish without invoking a model or
replaying the paused operation.

Readiness resumes the model with safe status and fresh-resource instructions.
It never replays the originating tool call as an allow-once policy approval.
The domain must still validate authority when the action actually executes.
Recovery preserves an already recorded final result instead of interpreting
lease loss as permission to repeat completed work.
