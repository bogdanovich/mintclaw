# Android Companion A0-N Node-Role Admission

## Status And Decision

Status: admitted for implementation as a bounded personal device-node
foundation

A0-N admits the Android node role and the minimum gateway changes required to
pair it safely. It does not admit the Android operator/chat plane, location,
notifications, SMS, media capture, voice, background presence, or any other
device capability.

The first supported application is a standalone personal build for Android 12
through Android 16 (`minSdk 31`, `compileSdk 36`, and `targetSdk 36`). It is a
native Kotlin application with a small Jetpack Compose status and enrollment
UI. The production source lives under `android/companion` in this repository
with its own pinned Gradle wrapper and Android CI job. Kotlin/JVM code requires
no NDK or ABI-specific library in A0-N.

The application initially acts only as a node. An operator controls it through
an existing authorized MintClaw channel such as Telegram. The future Android
operator client remains governed by A0-O and A1 through A3 in the
[Android Companion Roadmap](android-companion-roadmap.md).

## Why This Slice Is Admitted

MintClaw already has the reusable node foundation:

- outbound WSS transport, challenges, pairing approval, revocation, and
  liveness;
- operator-owned target aliases and per-agent target policy;
- versioned capability descriptors, catalog approval, and freshness;
- durable invocation identity, status, cancellation, and explicit unknown
  outcomes;
- node-local policy and redacted runtime events; and
- Linux and macOS real-process protocol evidence.

Android needs a native client because the Go companion cannot safely or
portably own Android Keystore keys, lifecycle, runtime permissions, content
providers, notification actions, or user-visible consent.

Two bounded gateway gaps must be closed before the native client can pair:

1. node authentication currently assumes an exportable Ed25519 seed and fixes
   Ed25519 key and signature lengths in the protocol; Android Keystore provides
   a well-supported non-exportable P-256 ECDSA signing path; and
2. an unknown node can currently create a bounded pending-pairing record merely
   by reaching the WSS endpoint. A phone QR flow needs one short-lived,
   single-use enrollment offer without turning that offer into durable node
   authority.

These are extensions of existing authentication and admission ownership. They
do not justify a second transport, pairing registry, device broker, generic
mobile framework, or protocol version.

## Concrete Operator Outcome

The A4 foundation admitted by this document proves one small flow:

1. an authenticated operator asks the gateway CLI to create a five-minute
   Android node enrollment offer through a focused gateway operator endpoint;
2. the CLI prints a bounded QR payload containing the exact WSS endpoint,
   endpoint trust material, and one opaque offer;
3. the Android app scans the QR, creates a non-exportable P-256 signing key in
   Android Keystore, and proves possession over WSS;
4. the gateway atomically consumes the offer and creates one pending pairing,
   but grants no command authority;
5. the operator approves the node and assigns one target alias through the
   existing node administration path;
6. the app reconnects with the same key and advertises only
   `device.info.v1` and `device.permission_status.v1`;
7. an authorized Telegram agent discovers and invokes those commands; and
8. status recovers the same durable result after app process death or reports
   explicit `unknown` without executing a second invocation.

The operator may disconnect, revoke, clear, or reinstall the application.
Clearing app data or reinstalling creates a new device identity and requires a
new enrollment and pairing approval. The gateway never silently transfers the
old target authority to the new key.

## Frozen Scope

A0-N and its first A4 implementation series include only:

- algorithm-agile node authentication with legacy Ed25519 preservation;
- one short-lived Android enrollment-offer flow;
- one node-only Kotlin/Compose application;
- Android Keystore identity;
- WSS challenge, authentication, reconnect, heartbeat, and revocation;
- the existing node request/response envelope and strict JSON validation;
- a bounded durable invocation ledger;
- `device.info.v1` and `device.permission_status.v1`;
- visible connection, pairing, permission, and diagnostic state; and
- language-neutral protocol fixtures plus emulator and real-device evidence.

Explicitly excluded are:

- the Android operator/chat connection, session catalogue, message outbox, or
  mobile history;
- location, notification access, SMS, contacts, calendar, media, microphone,
  camera, sensors, files, shell, terminal, jobs, browser, MCP, and update;
- Google Play distribution, default SMS or assistant roles, push messaging,
  boot-start, an always-running foreground service, or battery-optimization
  exemptions;
- key attestation, StrongBox as a requirement, device integrity scoring,
  hardware identity, MDM, accessibility, or device administration;
- a new node protocol version, mTLS, an inbound phone listener, or a second
  gateway registry;
- automatic pairing approval, authority copied from a QR code, or model-chosen
  endpoint, certificate, alias, policy, permission, or command; and
- A5 or later work hidden in an A4 review fix.

Location begins only after a separate A5 admission. Notification messaging and
full SMS remain A6 and A7. Background presence requires its own Android
lifecycle evidence after visible A4 operation is healthy.

## Whole-System Model

```text
authenticated operator
        |
        v
gateway CLI: create bounded enrollment offer
        |
        v
QR: endpoint + trust + one-time offer
        |
        v
Android app
  +-- P-256 key in Android Keystore
  +-- app-private config and invocation ledger
  +-- visible node connection lifecycle
        |
        v
existing /nodes/v1/ws admission
  +-- consume enrollment offer for an unknown Android key
  +-- existing pending pairing and explicit operator approval
  +-- existing registry, target policy, invocation, and audit
        |
        v
Telegram agent -> nodes discovery/invoke/status
```

The enrollment-offer manager owns only unconsumed short-lived offers. The
existing pairing registry remains identity and approval authority. The Android
ledger remains accepted-invocation authority. Runtime events and diagnostics
observe those sources of truth and cannot advance them.

## Identity And Signature Contract

### Supported algorithms

Node authentication v1 becomes algorithm-agile with exactly two admitted
values:

- `ed25519`, preserving the current Go companion behavior; and
- `ecdsa-p256-sha256`, for Android Keystore signing.

`IdentityProof` adds `key_algorithm`. Its absence means legacy `ed25519` so
existing paired Linux and macOS companions retain their identities and require
no state rewrite. New implementations always send the field. Unknown values,
invalid encodings, unsupported curves, non-canonical signatures, and algorithm
or key-length mismatches fail before pending pairing is written.

Ed25519 continues to use its current 32-byte raw public key, 64-byte raw
signature, node-ID derivation, and signature transcript byte-for-byte. This is
a compatibility invariant.

P-256 uses:

- the SEC1 uncompressed 65-byte public point encoding;
- on-curve validation and rejection of the point at infinity;
- `SHA256withECDSA` in Android Keystore;
- a fixed 64-byte signature encoding `r || s`, with each integer unsigned and
  left-padded to 32 bytes;
- low-S normalization by the signer and rejection of zero, high-S,
  out-of-range, oversized, negative, or otherwise non-canonical signature
  integers; and
- a domain-separated transcript that includes `key_algorithm` and every claim
  currently bound by node authentication.

The Android node ID is derived from a domain-separated SHA-256 digest of the
algorithm name and canonical public key. It must not collide semantically with
legacy `sha256(ed25519_public_key)` derivation. Registry records persist the
algorithm beside the public key; a missing value in an existing record means
`ed25519`.

The protocol remains v1. This is an additive authentication representation,
not a second command or transport protocol. The machine-readable schema and
language-neutral fixtures become the contract for both Go and Kotlin.

### Android key use

The app generates one `secp256r1` key under an application-owned alias using
Android Keystore with signing purpose and SHA-256 digest. The private key is
never exported, serialized, backed up, logged, placed in the QR payload, or
sent to the gateway.

Background authentication is not admitted in A0-N, so the first key does not
require a per-signature biometric prompt. The phone's application sandbox and
Keystore protect the key from ordinary file extraction. Hardware backing and
StrongBox are reported as bounded diagnostics when available but are not
authority and are not required for pairing.

Android backup excludes node identity, enrollment material, configuration,
ledger, and diagnostics. Restore to another phone never clones a paired node.
Key invalidation produces an explicit local `identity_invalid` state and
requires revocation or re-enrollment; the app does not generate a replacement
under the old node ID.

Key attestation is deferred. A compromised application process can ask its own
Keystore key to sign and can exercise the node's configured policy. A0-N does
not claim to defend against arbitrary code execution inside the trusted app
UID. Node-local typed policy, revocation, and least privilege still limit the
resulting authority.

## Enrollment Offer And QR Contract

An authenticated operator creates an offer through a focused CLI command. The
CLI calls a narrow POST-only gateway operator endpoint authenticated by the
existing MintClaw operator credential; it does not instantiate another gateway
or write directly into gateway state. The endpoint creates the offer in the
running node-admission generation and has no list, read-back, approval, or
invocation operation. The first implementation may render both a terminal QR
and a copyable URI, but the URI is sensitive and must not enter shell history
automatically.

An offer contains:

- a random public offer ID;
- at least 256 bits of random secret material;
- the exact `wss://` node endpoint;
- system-PKI endpoint trust and an optional SHA-256 SPKI pin that narrows it;
- requested role `companion` and platform class `android`; and
- issue and expiry times with a default and maximum lifetime of five minutes.

The encoded payload is versioned, bounded to 4096 bytes, contains no gateway
bearer token, pairing approval, target alias, agent grant, command permission,
private key, SMS permission, or operator credential, and is never accepted from
model tool input.

The gateway keeps at most 64 live offers in memory. Restart invalidates them;
the operator creates another QR. Durable enrollment-offer storage is not
needed because losing an unused offer has no side effect.

The Android identity proof carries the public offer ID and an HMAC proof
computed as `HMAC-SHA256(secret, domain || offer_id || identity_transcript)`.
The domain string and field encoding are fixed by a language-neutral fixture;
the identity transcript already binds the one-time challenge and Android
public key. The secret itself is not sent or logged. The gateway atomically
verifies and consumes the offer while binding it to the first valid Android
public key. An expired, unknown, malformed, already consumed, wrong-endpoint,
wrong-platform, or mismatched proof is rejected without a pending-pairing
record.

Consumption grants only permission to request pairing. Existing explicit
operator approval still assigns aliases and approved commands. Possession of a
stolen QR therefore cannot approve a node, inspect another node, access chat,
or invoke a command. Reconnect after a valid pending request authenticates by
the device key and no longer uses the offer.

Legacy explicitly configured Go companions retain their existing pending
pairing behavior. Android proofs require a valid offer until a future admission
defines another bootstrap mode.

## Transport And Connection Lifecycle

The Android app connects only to the exact provisioned WSS origin. It rejects
plaintext, redirects, userinfo, fragments, endpoint changes, invalid system
trust, and pin mismatch. A QR-provided pin narrows platform trust; it never
disables hostname or certificate validation.

Private CAs and self-signed leaf certificates are not admitted in A0-N. They
need a separately designed Android trust-import flow; the initial deployed
gateway must present a certificate accepted by the Android system trust store.

Protocol limits remain those of the current node v1 schema: strict frame
shapes, one-megabyte maximum frame size, bounded identifiers, JSON objects for
params, and no unknown authentication fields beyond those explicitly admitted.
The Kotlin decoder rejects duplicate keys, trailing data, numeric overflow,
unknown enum values, and response IDs that do not match an outstanding request.

The visible A4 lifecycle is:

```text
unconfigured -> enrolling -> pending_pairing -> connected
      |             |                |              |
      +-> error <---+----------------+--------------+
                                      \
                                       -> revoked | incompatible
```

Only one connection generation owns requests at a time. Reconnect uses bounded
exponential backoff with jitter and resets only after a stable connection.
Network change, app backgrounding, process death, gateway restart, pairing
revocation, catalog change, and protocol mismatch have distinct UI states.

A0-N promises availability only while the application is visible. It does not
declare a foreground-service type or claim background reachability. The app
may recover when reopened, and the gateway reports the node disconnected while
Android is not running it. A later lifecycle admission may select an
appropriate foreground service, push wake-up, or scheduled recovery based on
real device and distribution evidence.

## A4 Command Surface And Policy

The A4 catalog contains only:

| Command | Input | Bounded result | Risk | Cancel |
| --- | --- | --- | --- | --- |
| `device.info.v1` | empty object | app version, Android API/release, manufacturer/model class, supported ABI list, lifecycle mode, and bounded Keystore facts | read | no |
| `device.permission_status.v1` | empty object | fixed known capability names with `not_requested`, `granted`, `denied`, `restricted`, or `not_supported` | read | no |

Outputs are limited to 8 KiB and ten seconds. They exclude serial numbers,
IMEI, IMSI, phone number, advertising ID, Android ID, account names, installed
application inventory, network address, Wi-Fi identifiers, location, contacts,
notification text, SMS content, certificate chains, and raw Keystore metadata.

Permission status is descriptive, not authority. It reports only the fixed
future MintClaw capability set and never requests a permission. Granting an
Android permission does not advertise a command until a later admitted handler
and node-local policy exist.

Defaults remain deny-all at the gateway target layer. Pairing approval binds
the exact A4 catalog, and an operator must separately grant the target and
commands to an agent. The model cannot enable the app, request a permission,
change the QR endpoint, rename the target, broaden the catalog, or select an
Android component.

## Durable Invocation Ledger

The app uses one app-private Room/SQLite ledger. A0-N does not add encrypted
SMS or media storage because it stores neither. The Android OS sandbox and
device encryption protect the database at rest; database encryption is
reconsidered only when a later capability would retain sensitive content.

Before executing a valid `node.invoke`, the app durably records:

- invocation and idempotency identity;
- canonical plan/input digest, command, catalog revision, and policy revision;
- accepted time and current state; and
- a bounded canonical result or stable error classification when known.

The ledger never stores enrollment secrets, private keys, raw approval data,
channel text, or unrestricted gateway frames. A duplicate identity with the
same canonical binding returns the existing record. A duplicate with changed
input, plan, command, revision, or idempotency binding fails closed.

The first implementation uses these observations:

```text
accepted -> running -> completed | failed
    |          |
    +----------+-> unknown
```

Process death after durable acceptance never causes automatic execution of a
second invocation. Startup reconciles an incomplete record to `unknown` unless
durable evidence proves a terminal result. `node.invoke.get` returns the same
record after reconnect. `node.invoke.cancel` reports unsupported or already
terminal for the A4 read commands and never changes a completed result.

Retention is bounded by record count, encoded bytes, and age. Cleanup deletes
only terminal records after the gateway recovery window and never rewrites an
accepted or running record into a retryable state. Exact initial limits are set
in the implementation PR and covered by capacity tests.

## Threat Model And Privacy

A0-N assumes a hostile network, malicious QR shown by another party, stolen or
lost phone, compromised gateway/model/prompt, malformed protocol peer,
duplicate/reordered frames, revoked permission, process death, full disk,
clock change, gateway restart, and replay at every boundary.

The design must prevent:

- a QR or model argument becoming pairing approval or command authority;
- unknown Android clients filling the durable pairing registry without a live
  enrollment offer;
- algorithm confusion, cross-algorithm node-ID ambiguity, signature
  malleability accepted as a second identity, or rewriting existing Ed25519
  registrations;
- TLS pin bypass, redirect to another gateway, or plaintext fallback;
- two live connection generations consuming one invocation;
- replay or changed-argument reuse after durable acceptance;
- permission state being treated as command authorization;
- diagnostics exposing endpoint secrets, enrollment offers, keys, user data,
  raw frames, or future sensitive capability results; and
- backup, reinstall, or app data restore cloning durable node authority.

Revocation takes effect on reconnect and terminates the active gateway session
through the existing registry/session behavior. A lost unlocked phone may use
the app until the OS or gateway revocation stops it; remote wipe, device lock,
and account recovery are platform/operator responsibilities. A0-N adds no
false remote-wipe guarantee.

## Events And Diagnostics

The gateway reuses existing typed node events. The app exposes a bounded local
diagnostic screen and explicit redacted export containing only:

- app/build version, Android API level, and protocol range;
- coarse connection/pairing state and transition timestamps;
- catalog and policy revision digests;
- bounded error codes, retry class, and latency;
- ledger counts/bytes and last reconciliation outcome; and
- boolean hardware-backed/StrongBox availability when safely observable.

It excludes endpoint query data, QR payloads, offer IDs and secrets, keys,
signatures, certificate chains, raw frames, node invocation input/output,
device identifiers, channel/session content, and future personal data.
Diagnostics are passive and cannot consume an offer, approve pairing, advance
an invocation, acknowledge a result, or reconnect a revoked identity.

## Delivery Sequence

Every implementation PR starts from fresh merged `main` and preserves the
existing Linux/macOS companion:

1. **Identity agility.** Add the bounded algorithm type, P-256 verification,
   domain-separated node ID, registry persistence, schema/fixture updates, and
   legacy Ed25519 compatibility tests. No Android project and no enrollment
   flow.
2. **Enrollment offers.** Add the in-memory offer manager, narrow authenticated
   operator endpoint, CLI QR generation, HMAC-bound Android proof, atomic
   consumption, limits, redaction, and WSS tests. No new durable store or
   pairing approval path.
3. **Android foundation.** Add the pinned Gradle project, Kotlin protocol
   models and language-neutral conformance tests, Compose enrollment/status UI,
   Keystore identity, backup exclusions, and local storage skeleton. No live
   command execution.
4. **A4 vertical slice.** Add WSS lifecycle, strict dispatch, Room invocation
   ledger, the two read-only commands, status recovery, and gateway-to-emulator
   tests.
5. **Proof and operations.** Validate merged main on an x86_64 emulator and one
   arm64 physical device, pair through a real QR with the deployed gateway,
   invoke through Telegram, kill/restart the app at lifecycle boundaries,
   record redacted evidence, and update roadmap status.

Do not combine identity agility, enrollment state, the Android application,
and command execution into one review unit. Do not introduce another
prerequisite PR unless a concrete failed invariant cannot be owned by these
boundaries.

## Validation Matrix

| Area | Required evidence |
| --- | --- |
| Legacy identity | Existing Ed25519 fixtures, node IDs, registration files, reconnect, approval, and revocation remain byte-compatible |
| P-256 identity | Go and Kotlin agree on public-key encoding, transcript, fixed signature form, node ID, valid proof, and every malformed boundary |
| Enrollment | expiry, single use, concurrent consume, gateway restart invalidation, wrong endpoint/platform/key, stolen replay, capacity, and redaction |
| TLS and protocol | public PKI, valid pin, pin mismatch, redirect denial, plaintext denial, malformed/oversized/duplicate-key JSON, and request correlation |
| Android storage | Keystore non-exportability, backup exclusion, reinstall identity change, full disk, corrupt ledger, and bounded retention |
| Invocation | changed input, duplicate/concurrent request, process death before/after acceptance/result commit, reconnect status, no replay, and unsupported cancel |
| Policy | unapproved node, stale catalog, missing target grant, revoked registration, hidden commands, and permission state without authority |
| Lifecycle | network transition, background/process kill, gateway restart, stable reconnect, incompatible protocol, and explicit disconnected state |
| Privacy | automated scan of gateway logs, app logs, database, and diagnostic export for offers, keys, raw frames, and prohibited device identifiers |
| Platforms | Android 12 and 16 emulator coverage plus one supported arm64 physical-device canary |

Go changes run focused race tests, tagged lint, and relevant broad repository
tests. Android changes run Gradle unit tests, lint, dependency verification,
instrumented emulator tests, and deterministic language-neutral fixtures.
Real SMS, location, or notification evidence is not part of A4.

## Definition Of Done And Mandatory Stop

A0-N is implemented only when:

- current Linux/macOS Ed25519 companions reconnect with unchanged node IDs;
- an Android P-256 proof is verified without exporting its private key;
- one short-lived QR offer creates at most one pending Android pairing and
  still requires explicit operator approval;
- the standalone app pairs and reconnects over verified WSS on Android 12
  through 16;
- only the two admitted read commands are discoverable and policy remains
  deny-all until configured;
- one authorized Telegram request obtains a model-visible device result;
- duplicate, changed, concurrent, restart, disconnect, and revocation cases
  preserve exactly one accepted invocation and no blind replay;
- emulator and arm64 physical-device evidence comes from merged main;
- logs, ledger, and diagnostic export pass the redaction audit;
- every A0-N/A4 PR is merged and the roadmap and operations docs match the
  deployed behavior; and
- existing Go gateway and node behavior remains healthy.

Immediately stop the A0-N/A4 program when these criteria are evidenced. Do not
begin location, notification access, SMS, operator chat, background services,
push, voice, Play distribution, attestation, or generic Android capability
abstractions. Advance to A5 only through a new admission based on the deployed
A4 evidence.

Perform an architecture checkpoint instead of continuing to patch when four
substantive review/fix cycles occur, production scope doubles from a PR's first
ready baseline, the same identity or lifecycle invariant is challenged three
times, Android requires a second pairing/invocation source of truth, or the
implementation cannot retain legacy Ed25519 identity without a migration.

## Current Platform References

Implementation must revalidate current platform rules rather than treating
this admission as permanent Android documentation:

- [Android Keystore](https://developer.android.com/privacy-and-security/keystore)
- [P-256 signing with KeyGenParameterSpec](https://developer.android.com/reference/android/security/keystore/KeyGenParameterSpec)
- [Android 16 SDK setup](https://developer.android.com/about/versions/16/setup-sdk)
- [Android background execution](https://developer.android.com/develop/background-work)
- [Foreground service types](https://developer.android.com/develop/background-work/services/fgs/service-types)
