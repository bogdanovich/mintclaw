# Document Process Capacity

MintClaw admits document workers through one execution budget shared by every
agent in the process. The default allows one active document worker and waits
up to 30 seconds for capacity. Input, page, decoded-content, pixel, artifact,
output, and worker-runtime limits continue to apply after admission.

```json
{
  "tools": {
    "document": {
      "enabled": true,
      "max_concurrent_operations": 1,
      "queue_timeout_seconds": 30
    }
  }
}
```

Config reload changes admission for new work. It never cancels a worker that
already holds capacity. A caller canceled while queued is removed immediately;
an active cancellation releases capacity only after the worker process group
has been terminated and reaped.

## Diagnostics

A saturated budget emits `Document execution capacity saturated` with only the
operation kind, active count, configured capacity, waiting count, and queue
timeout. A wait that reaches its limit emits `Document execution capacity wait
timed out` with the same content-free fields and returns the retryable typed
failure `document_capacity_timeout`. Document paths, refs, digests, field
values, and extracted content are never logged by these events.

```bash
journalctl --user -u mintclaw-main.service \
  --grep='Document execution capacity' --since='1 hour ago'
```

Repeated saturation with few timeouts means the budget is smoothing bursts.
Repeated timeouts require either reducing inbound parallelism, increasing the
queue timeout, or requalifying a larger capacity against the service envelope.
Do not raise capacity based only on CPU count.

## Service Envelope

Budgets are process-local. Every gateway or CLI process has an independent
budget, so deployments running several MintClaw services must also constrain
the service manager or container. Size memory from measured peak values:

Each one-shot document worker starts with a fixed 320 MiB Go runtime soft
memory limit. This stabilizes garbage-collection pressure across hosts but is
not a hard containment boundary and does not replace `MemoryMax` or a container
memory limit.

```text
MemoryMax >= gateway peak RSS
           + max_concurrent_operations * qualified worker peak RSS
           + 20 percent operating margin
```

Set `TasksMax` above the measured gateway baseline plus the maximum descendants
of every admitted worker, with margin for Go runtime threads. The following is
an example systemd user-service drop-in, not a universal sizing value:

```ini
[Service]
MemoryHigh=1536M
MemoryMax=2G
TasksMax=128
```

Apply and verify a user-service drop-in with:

```bash
systemctl --user daemon-reload
systemd-analyze --user verify ~/.config/systemd/user/mintclaw-main.service
systemctl --user restart mintclaw-main.service
systemctl --user show mintclaw-main.service \
  -p MemoryCurrent -p MemoryPeak -p MemoryHigh -p MemoryMax \
  -p TasksCurrent -p TasksMax
```

For containers, set both a memory limit and a PID limit. The host aggregate
must cover all configured MintClaw processes; multiplying process-local
capacity without updating the host envelope defeats the resource guarantee.

## Qualification

The Linux load test uses a maximum-size malformed snapshot and a separate
malformed snapshot. It observes worker PIDs and `/proc` RSS while both requests
compete for a capacity of one, then cancels each process and proves that
capacity returns to zero:

```bash
go test ./pkg/document \
  -run '^TestDocumentExecutionBudgetBoundsMalformedMaximumSizeLoad$' \
  -count=1 -v
```

Measure uncontended admission overhead separately from PDF backend work:

```bash
go test ./pkg/document \
  -run '^$' -bench '^BenchmarkExecutionBudgetUncontended$' \
  -benchmem -count=5
```

Re-run both commands on every host profile before increasing capacity. Treat an
RSS ceiling failure, worker-count violation, cancellation leak, or meaningful
single-document latency regression as a failed qualification.
