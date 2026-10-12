# Runtime Credential Output Privacy

## Scope

The [trusted-runtime credential design](credential-trusted-runtime-design.md)
requires values to stay outside model-facing and durable outputs while aliases
and safe outcomes remain visible. `internal/secretvalues` supplies a process-local
backup guard for values retrieved dynamically after configuration is loaded.
Trusted credential code must still suppress sensitive output at its source and
emit typed safe outcomes instead of raw provider or browser diagnostics.

## Registration and lifetime

Register a nonempty bounded UTF-8 value before using it in a browser or connector.
Registration returns an idempotent release function. Retain it until every
session and operation that may still contain the value has ended. Overlapping
registrations share protection; one owner cannot withdraw another's guard.
No registration or value is persisted or logged by this package.

The registry admits at most 256 distinct values, each at most 32 KiB, with an
8 MiB aggregate stored-variant limit. Capacity failure returns a fixed error
and preserves existing registrations. It never evicts a live secret to make
room for another one. A consumer must refuse use if registration fails.

Redaction covers exact values, browser text-input CR/LF normalization, JSON/Go
string escaping, HTML escaping, URL query/path encoding, hex, and common base64
forms. Longest values match first. These are accidental-output protections, not
a guarantee against arbitrary malicious transformations or binary/image output.

## Integrated sinks

- `Config.FilterSensitiveData` always applies registered runtime values,
  including when ordinary configured-secret filtering is disabled, when content
  is shorter than its threshold, and when the config receiver is nil.
- The agent's cached turn policy also consults the live registry for tool and
  pending-result text. Values registered after a turn snapshot are still guarded
  when ordinary configured-secret filtering is disabled.
- Diagnostic text previews apply the guard before truncating input, then again
  after a caller-supplied filter. Diagnostic JSON previews also guard serialized
  numeric and unclassified values and suppress structurally invalid replacements.
- The shared logger guards each complete serialized event before console/file
  fanout. Logger output reconfiguration preserves that wrapper. If a structural
  or numeric replacement would invalidate JSON, it writes a fixed safe record.

Unregistered aliases and operation/outcome metadata are preserved. Registering
values is mandatory for the future credential coordinator; this package does
not fetch a secret, activate browser login, enroll a phone, or load a real token.
It does not cover arbitrary direct stdout/stderr writers, screenshots, opaque
artifacts, companion JavaScript processes, or every persistence path by itself.
Those still require source suppression or explicit integration and the TR6
serialized-output canary audit. A passing guard test is not full qualification.

## Validation

Focused tests exercise shared/overlapping lifetimes, bounded denial without
eviction, concurrent registrations, ordinary encoding/normalization echoes,
short OTPs with disabled filtering, bounded trace previews, numeric JSON,
and actual console plus file logger output with aliases retained. Fixtures use
synthetic values and report only invariant failures, never captured payloads.
