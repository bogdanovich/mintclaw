# Durable Ingress

MintClaw protects inbound chat messages with a normalized durable ingress spool.

The spool sits behind `bus.MessageBus.PublishInbound`, after a channel adapter has
converted a platform-specific update into a `bus.InboundMessage`. This makes the
first layer channel-agnostic: Telegram, Slack, Discord, MQTT, webhooks, and other
channels all use the same normalized persistence path once they publish into the
bus.

## Flow

1. A channel receives a platform update.
2. The channel normalizes it into `bus.InboundMessage`.
3. `MessageBus.PublishInbound` writes the message to disk under:

   ```
   <workspace>/state/ingress-spool/inbound/
   ```

4. The bus publishes the message to the in-memory inbound channel.
5. The agent loop processes or accepts the message.
6. The agent loop calls `AckInbound`, which removes the spool entry and confirms the directory update.

On gateway startup, `PendingInboundSpool` captures the unacked pending and
processing entries before channel ingress starts. `ReplayInboundMessages` then
republishes that exact snapshot after the agent loop is ready. Capturing first
prevents a message that arrives during startup from being selected again by the
same replay. This covers the common failure case where the process exits after
a normalized inbound message has entered MintClaw but before the agent has
processed it.

## File States

- `*.json`: pending entry, written but not yet delivered to the in-memory bus.
- `*.processing`: entry delivered to the in-memory bus and awaiting ack.
- `*.failed`: terminal tombstone for entries intentionally failed by runtime
  code.

Current gateway wiring enables the spool automatically for `mintclaw gateway`.
One-shot CLI helpers and tests use a plain in-memory bus unless they explicitly
attach an `InboundSpool`.

## Guarantee Boundary

This layer stores normalized `bus.InboundMessage` values. It does not store raw
platform payloads before channel normalization.

That means it protects all channels uniformly after they publish into MintClaw,
but it does not yet provide OpenClaw-style raw transport offset protection for
Telegram long polling before `PublishInbound` is called. A deeper transport-level
spool can be added later for channels whose upstream protocol needs exactly that
offset/watermark behavior.

## Filesystem Durability

Spool records use the shared atomic-write helper: sync file contents before
rename, then sync the containing directory. Claim, retry and failure renames also
confirm directory metadata before reporting success. Acknowledgement removes both
possible entry names and confirms the directory, including on an absent-name
retry after an earlier sync failure. Newly created directory components are
anchored at the nearest existing parent and created through the durable-directory
helper.

A reported sync failure can follow a committed rename or unlink. It does not
prove the old path still exists. Recovery retains the same message identity;
callers must not treat unconfirmed persistence as a successful admission or
acknowledgement. This is filesystem fault-contract evidence, not a physical
power-loss qualification for every deployment filesystem.

A successful acknowledgement is not yet a typed retirement proof. An in-memory
bus can acknowledge without a spool, and steering can acknowledge at the
canonical-history handoff while a turn continues. The retention owner must
qualify the actual source and remaining obligations separately.

## Steering Caveat

If a message arrives while a turn for the same session is already active, the
agent accepts it into the in-memory steering queue but keeps the ingress spool
entry until the queued continuation is injected into the running turn context
and persisted into session history. If the process exits before that injection
happens, gateway startup replays the original inbound message from the spool.

The steering queue itself is still not a separately durable queue. Once a
continuation has been injected into the running turn, the ingress spool entry is
acked and removed because the message has crossed into the session/history
durability boundary. A future durable steering queue could close any remaining
gap between in-memory turn state and deeper execution checkpoints.
