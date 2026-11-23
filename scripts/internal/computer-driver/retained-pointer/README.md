# Private retained X11 pointer

The worker keeps one RECORD stream and one dedicated pointer on a fresh,
explicitly owned Xvfb display. Version 1 supports one vertical wheel-down step
per authorized request. Recorder readiness wakes bounded idle processing.
Independent fences verify the original target and input device before each
native edge. A recorder or hierarchy loss retires that authority.

Use the pinned Rust toolchain from the private computer driver workflow.

```sh
scripts/internal/computer-driver/build-linux-retained-pointer.sh \
  --output /absolute/private/artifact-directory
```

The build tests both the default and fixture-only configurations. It emits a
default release executable with source and executable SHA-256 receipts.
Fixture fault injection requires the explicit Cargo feature and is outside the
release build. Production Companion registration requires separate lifecycle,
operator stop, policy, and native qualification work.

The sealed keeper's private protocol uses inherited stdio. Its startup receipt
contains `protocol_version` and the original recorder and pointer identities.
Each bounded action line carries a monotonically increasing sequence, original
window and geometry, the lease token, and its window generation. Results carry
the same action sequence and native dispatch and recovery status. The caller
owns lease serialization, request and result durability, and independent
application effect verification. Closing the keeper must be coordinated with
the lifetime of the owned display.
