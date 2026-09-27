# PDF Runtime Reliability Mini-Roadmap

## Status

Completed on 2026-09-27. This roadmap recorded concrete reliability findings
in the PDF and release runtime without reopening document authority, protected
form state, approval, journaling, artifact validation, or delivery ownership.

| Packet | Pull request | Merge commit |
| --- | --- | --- |
| PRR1 release-matrix truth | `#1353` | `9cdff2f33870b72bdc13741d66b6404c8b2a4190` |
| PRR2 native provenance | `#1357` | `64e3309f8ded3f81981946a54cbd5d192e742f91` |
| PRR3 native confinement | `#1365` | `2996ea4fa39c9e1d1f3b5f34de21e47f6401f70d` |
| PRR4 process capacity | `#1374` | `6171595a6a1b7b6f69455aad2e9f4ba967e64f23` |

Baseline: `origin/main` at `c48a19713` on 2026-09-26.

## Objective

Keep the admitted Linux PDF runtime secure and operable while its dependencies
and supported release targets evolve. The work is complete when every shipped
target builds before merge, native PDF executables have current and inspectable
provenance, untrusted native parsing runs inside a qualified host boundary, and
document work cannot consume unbounded process-level capacity.

This is a reliability program, not a general document refactor. Large files,
backend count, or the existence of platform-specific code are not independent
reasons to redesign `pkg/document`.

## Confirmed findings

### Release coverage admits a broken target

`make build-all` currently fails for `linux/arm` because several unsigned
filesystem magic constants overflow the target's `int32` `Statfs_t.Type`.
Pre-merge platform compilation covers Darwin ARM64 and Windows AMD64, but not
the Linux ARM target shipped by `build-all`. Main-branch builds therefore find
the defect only after merge.

### Ghostscript identity is stale after a security update

The hybrid-form verifier admits one `/usr/bin/gs` SHA-256 while its reported
backend identity contains only upstream version `10.02.1`. Ubuntu security
notice USN-8791-1 qualifies Noble package
`10.02.1~dfsg1-0ubuntu7.9` for CVE-2026-39919. That package's executable no
longer matches the admitted digest. An updated host safely loses hybrid-form
verification; an unupdated host may retain the vulnerable executable.

The fail-closed digest check is correct. The missing piece is an explicit
package-revision and requalification lifecycle.

### Process separation is not host confinement

The one-shot document worker has a path-free protocol, immutable input
descriptor, private scratch, scrubbed environment, bounded output, deadline,
process-group cancellation, and parent-death handling. It still runs under the
gateway UID with the gateway's filesystem and network authority. A native
parser compromise can therefore outlive the intended document-data boundary
even though it cannot corrupt the parent process directly.

### Document capacity has no dedicated owner

Input, page, decoded-content, pixel, artifact, and wall-clock limits are
bounded. Concurrent PDF processes are bounded only indirectly by turn
parallelism, whose default is one but which is configurable and does not cover
multiple MintClaw processes. There is no explicit document worker capacity or
host memory budget.

## Delivery packets

### PRR1: restore release-matrix truth

Scope:

- make denied-filesystem comparison correct on 32-bit and 64-bit Linux;
- add Linux ARM compilation to the pre-merge platform matrix, or run the exact
  release compile graph before merge;
- retain the full post-merge `build-all` job as a release assertion.

Completion gate:

- `linux/amd64`, `linux/arm`, and `linux/arm64` compile from the same head;
- the regression is exercised by required PR CI rather than discovered after
  merge;
- no filesystem type is silently truncated or removed from the deny set.

### PRR2: make native backend provenance maintainable

Scope:

- qualify the current Ubuntu Ghostscript security revision and replace the
  stale executable digest;
- include distro package revision, executable digest, and functional role in
  one backend manifest used by capabilities and diagnostics;
- apply the same manifest shape to Poppler without changing its admitted
  behavior;
- keep exact identity checks and fail closed on unknown bytes;
- add a reproducible qualification command that reports package, digest,
  fixture, and rollback evidence without downloading during an operation.

Completion gate:

- a fully updated supported host advertises the expected operations;
- an old or unknown executable is rejected with a reason naming the expected
  package revision rather than only the upstream version;
- hybrid fixtures pass dual-render verification on the admitted package;
- rollback restores a documented package-and-MintClaw pair.

### PRR3: confine native PDF descendants

Dependencies: PRR2 and the existing `pkg/isolation` Linux implementation.

Scope:

- define one document-specific Bubblewrap policy instead of a second sandbox
  framework;
- expose only the immutable input descriptor, private writable scratch,
  verified executable descriptors, required read-only dynamic libraries and
  fonts, and a minimal `/proc` and `/dev` view;
- unshare network and IPC and preserve process-group cancellation;
- make native Poppler and Ghostscript capabilities unavailable when the
  required boundary cannot be established;
- report the effective isolation mode in backend identity and `doctor` output.

Completion gate:

- a probe cannot read an unmounted gateway-owned file or reach the network;
- normal extraction, rendering, standard-form verification, and hybrid-form
  verification pass under the boundary;
- missing Bubblewrap never silently starts an unconstrained native parser;
- WASM-only operations remain independent of Bubblewrap availability.

### PRR4: own document process capacity

Dependencies: measured portable-backend evidence, if that backend has landed.

Scope:

- add one process-owned document execution budget shared by document
  operations in that process;
- default to one active heavy document operation and a bounded wait that
  respects cancellation;
- keep per-operation byte, page, pixel, output, and time limits;
- set or document service-level memory and process limits for deployments with
  multiple MintClaw processes;
- expose saturation and timeout diagnostics without document content.

Completion gate:

- concurrent turns cannot exceed the configured document capacity;
- cancellation releases queued and active capacity deterministically;
- load tests prove bounded process count and memory under malformed and
  maximum-size fixtures;
- ordinary single-document latency does not regress materially.

## Sequence and coordination

```text
PRR1 release truth
  |
  v
PRR2 backend provenance -----> PRR3 native confinement
                                  |
portable backend measurements ---+----> PRR4 capacity ownership
```

PRR1 and PRR2 should proceed regardless of the portable-backend decision.
PRR3 remains necessary for every retained native parser. PRR4 must use measured
native and WASM costs rather than guessing a universal worker count.

## Stop conditions

The roadmap is complete after PRR1-PRR4 have merged evidence. Stop earlier and
record a decision when a native backend is removed before its packet; do not
build isolation or lifecycle machinery for a backend no longer used. Do not
introduce a daemon, generic supervisor, dependency-injection container, or
repository-wide backend framework to complete these packets.
