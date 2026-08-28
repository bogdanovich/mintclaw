# Channel package

This package connects platform adapters to MintClaw's current message, delivery,
and lifecycle contracts. It is a navigation and extension guide for the code on
`main`; migration history and abandoned refactor designs do not belong here.

## Runtime shape

```text
platform update -> channel adapter -> bus.MessageBus -> agent runtime
agent result     -> bus.MessageBus -> DeliveryRuntime -> channel adapter -> platform
```

`Manager` composes three owners instead of keeping parallel maps for the same
runtime state:

| Owner | Responsibility |
| --- | --- |
| `ChannelLifecycle` | Channel/config registry, serialized start/stop/reload transitions, shared HTTP listeners and handlers, and restart-required state. |
| `DeliveryRuntime` | Per-channel delivery owners, outbound dispatch lifetime, queue admission, ordering, retries, and typed delivery outcomes. |
| `StreamCoordinator` | Streams, placeholders, typing/reaction cleanup, and tool-feedback lifecycle. |

The manager retains the message bus, runtime-event bus, and optional durable
outbox coordinator, then adapts calls between those owners. Channel adapters
own platform API translation and transport-local reconnect behavior. They do
not own a second copy of manager delivery, retry, or lifecycle state.

## Current contracts

- A `channel_list` map key is the instance name. Its `type` is required and
  selects both the settings decoder and channel factory. New code must consume
  that validated type instead of inferring it from the instance name.
- Adapters normalize inbound platform updates into `bus.InboundMessage` or
  `bus.ObservedMessage`. `bus.InboundContext` is the routing and session source
  of truth.
- `MessageBus.PublishInbound` is the common durable-ingress boundary when the
  gateway spool is enabled. Adapters do not implement independent normalized
  ingress spools.
- Every channel implements the small `Channel` interface. Media, webhooks,
  health endpoints, streaming, placeholders, editing, reactions, typing, and
  command menus use optional capability interfaces only when supported.
- Outbound adapters return `DeliveryResult` with confirmed platform IDs and an
  explicit complete, partial, or failed outcome. A failed operation is marked
  rejected only when the adapter knows it happened before remote acceptance;
  uncertain acceptance is ambiguous and must not be blindly retried.
- Messages carrying a delivery ID participate in the durable outbox contract.
  Delivery state is persisted before completion events are published.
- Runtime events are observational. They must not become a second source of
  lifecycle or delivery truth.
- The package has one current internal bus and persisted-state shape. Bounded
  additive compatibility belongs at a first-party wire boundary, not in
  channel-manager maps, alternate metadata keys, or historical readers.

## Where to look

| Concern | Current source |
| --- | --- |
| Required channel interface and shared inbound helpers | `base.go` |
| Optional channel capabilities | `interfaces.go`, `media.go`, `webhook.go` |
| Factory registration | `registry.go` and each adapter's `init.go` |
| Manager construction and dependency wiring | `manager_runtime.go` |
| Lifecycle and shared HTTP ownership | `channel_lifecycle.go`, `manager_lifecycle.go` |
| Outbound ownership and dispatch | `delivery_runtime.go`, `manager_delivery.go`, `manager_outbound.go` |
| Typed delivery and retry semantics | `delivery.go`, `durable_outbound.go` |
| Streaming and transient UI state | `stream_coordinator.go`, `manager_streamer.go`, `manager_presend.go` |
| Current bus payloads | `../bus/types.go` |
| Channel configuration and settings registration | `../config/config_channel.go` |
| First-party adapter bootstrap | `../gateway/gateway.go` and platform-specific gateway files |

The detailed runtime guarantees live in the architecture documents:

- [Channel lifecycle](../../docs/architecture/channel-lifecycle.md)
- [Durable ingress](../../docs/architecture/durable-ingress.md)
- [Durable outbound delivery](../../docs/architecture/durable-outbound-delivery.md)
- [Runtime events](../../docs/architecture/runtime-events.md)

Platform-specific setup belongs under [`docs/channels`](../../docs/channels),
not in this package guide.

## Adding or changing a channel

1. Define the in-tree channel type and typed settings in `pkg/config`. Register
   the settings prototype so configuration validation can reject unknown fields
   and decode `settings` once. Use `SecureString` or `SecureStrings` for secrets.
2. Add an adapter package under `pkg/channels/<type>`. Embed `BaseChannel` when
   its allow-list, group-trigger, media-store, and inbound helpers fit the
   transport. Implement `Name`, `Start`, `Stop`, `DeliverText`, `IsRunning`, and
   `ReasoningChannelID` through the `Channel` contract.
3. On inbound, build complete `bus.InboundContext` and `bus.SenderInfo` values,
   then publish through `HandleMessageWithContext` or
   `ObserveMessageWithContext`. Do not add fallback parsing for old internal
   payload fields.
4. On outbound, translate the current `bus.OutboundMessage` into platform API
   calls and return a typed `DeliveryResult`. Preserve confirmed message IDs
   and a known unsent remainder when partial delivery makes that possible.
5. Implement only the optional capabilities the platform actually supports.
   Keep deterministic media constraints in `MediaPreflighter`; it must not make
   remote writes.
6. Register the factory in the adapter's `init.go`, preferably with
   `RegisterSafeFactory`. Add the first-party blank import to `pkg/gateway` so
   the factory is registered in the gateway build. Platform-specific build
   constraints may require a separate gateway file.
7. Add focused adapter tests for normalization, allow-list/group behavior,
   lifecycle, delivery classification, and each advertised capability. Update
   the config example and platform guide when users need new settings.

Keep transport reconnects inside the adapter unless a demonstrated failure
mode cannot be modeled there. Same-name configuration replacement is currently
restart-required; do not add a generic supervisor or hot-swap layer without a
concrete requirement and tests for ordering, admission, drain, and handler
availability.

## Validation

From the repository root:

```bash
make fmt
go test ./pkg/channels/...
scripts/pre-push-lint.sh --changed
```

Run focused race tests for concurrency-sensitive lifecycle, delivery, or
streaming changes. Repository CI also checks formatting and cross-platform
compilation.
