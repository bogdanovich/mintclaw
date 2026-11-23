package computerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

// InputAuthorization borrows the exact native lease and the host's current
// observation for one synchronous action. An executor must fence Check before
// dispatch, then verify its native target inside its own transaction. The
// request can contain ephemeral text. Neither it nor the lease may be retained.
type InputAuthorization struct {
	Request  computer.InputRequest
	Evidence computer.InputEvidence
	Window   desktop.WindowIdentity
	Lease    *desktop.InputLease
	Check    func(context.Context) error
}

type InputDisposition uint8

const (
	// The zero result includes a lost transport or missing cleanup evidence.
	InputUnverified InputDisposition = iota
	// The executor independently established that nothing was dispatched.
	InputNotDispatched
	// The action may have taken effect. The original held state is neutral.
	InputUncertainQuiescent
	// The original action and its effect were independently verified.
	InputEffectVerified
)

// InputExecutor is a trusted local backend for one explicitly bound profile.
// It must react to cancellation and complete any bounded native cleanup before
// returning. It may report an unverified result if that cleanup fails.
type InputExecutor func(context.Context, InputAuthorization) InputDisposition

// InputBackend commits the profile to a pinned executor and its input policy.
// Revision changes when the native executable or its admission policy changes.
type InputBackend struct {
	Revision [sha256.Size]byte
	Execute  InputExecutor
}

// InputHost shares the read-only host's guard, session owner and control gate.
// Construction requires an explicit native executor for every input profile.
type InputHost struct {
	*Host
}

var _ companion.ComputerInputCommandHost = (*InputHost)(nil)

func NewInputHost(
	nodeID nodes.ID,
	profiles map[string]companion.ComputerProfilePolicy,
	backends map[string]InputBackend,
) (*InputHost, error) {
	if runtime.GOOS != "linux" {
		return nil, desktop.ErrUnsupported
	}
	host, err := New(nodeID, profiles)
	if err != nil {
		return nil, err
	}
	return withInputBackends(host, backends)
}

func withInputBackends(host *Host, backends map[string]InputBackend) (*InputHost, error) {
	if len(backends) == 0 {
		return nil, computer.ErrInvalid
	}
	for alias, backend := range backends {
		if !host.profiles[alias].Enabled || backend.Execute == nil || backend.Revision == [sha256.Size]byte{} {
			return nil, computer.ErrInvalid
		}
	}
	host.executors = make(map[string]InputExecutor, len(backends))
	for index := range host.metadata {
		descriptor := &host.metadata[index]
		backend, ok := backends[descriptor.Alias]
		if !ok {
			continue
		}
		profile := host.profiles[descriptor.Alias]
		revision := sha256.Sum256([]byte("computer-input-v1\x00" + profile.Revision + string(backend.Revision[:])))
		profile.Revision = hex.EncodeToString(revision[:])
		descriptor.Revision = profile.Revision
		host.profiles[descriptor.Alias] = profile
		host.executors[descriptor.Alias] = backend.Execute
	}
	return &InputHost{Host: host}, nil
}

func (host *InputHost) ComputerInputProfiles() []nodes.ComputerProfileDescriptor {
	result := make([]nodes.ComputerProfileDescriptor, 0, len(host.executors))
	for _, profile := range host.metadata {
		if host.executors[profile.Alias] != nil {
			result = append(result, profile)
		}
	}
	return result
}

type observedInput struct {
	evidence  computer.InputEvidence
	workspace string
}

type sessionInput struct {
	// These fields are serialized by the existing session gate.
	execute  InputExecutor
	observed observedInput
	lease    *desktop.InputLease
	faulted  bool

	// Revocation can cancel the in-flight executor before acquiring the gate.
	ctx    context.Context
	cancel context.CancelFunc
}

func newSessionInput(execute InputExecutor) *sessionInput {
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionInput{execute: execute, ctx: ctx, cancel: cancel}
}

func (entry *sessionEntry) cancelInput() {
	if entry.input != nil {
		entry.input.cancel()
	}
}

func (host *InputHost) Act(ctx context.Context, action nodes.ComputerHostActRequest) (nodes.ComputerActResult, error) {
	request := action.Request
	if request.ValidateEnvelope() != nil || nodes.ID(action.WorkspaceID).Validate() != nil {
		return nodes.ComputerActResult{}, computer.ErrInvalid
	}
	status := nodes.ComputerHostStatusRequest{
		Principal: request.Binding.Principal,
		SessionID: request.Binding.SessionID,
	}
	entry, err := host.locked(ctx, status)
	if err != nil {
		return nodes.ComputerActResult{}, err
	}
	defer relinquish(entry.gate)
	input := entry.input
	if input == nil {
		return nodes.ComputerActResult{}, computer.ErrDenied
	}
	if input.faulted || input.lease != nil {
		return nodes.ComputerActResult{}, computer.ErrBusy
	}
	current, err := host.refresh(ctx, entry)
	if err != nil {
		return nodes.ComputerActResult{}, safeFailure(err)
	}
	witness := input.observed
	if witness.workspace != action.WorkspaceID || request.Binding != current.Binding {
		return nodes.ComputerActResult{}, computer.ErrStale
	}
	if err = request.Validate(current, witness.evidence, time.Now()); err != nil {
		return nodes.ComputerActResult{}, err
	}
	guard, ok := entry.driver.guard.(*desktop.WindowGuard)
	if !ok {
		return nodes.ComputerActResult{}, computer.ErrDenied
	}
	dispatch, cancel := context.WithDeadline(ctx, witness.evidence.ExpiresAt)
	defer cancel()
	stop := context.AfterFunc(input.ctx, cancel)
	defer stop()
	lease, err := guard.AcquireInputLease(dispatch)
	if err != nil {
		return nodes.ComputerActResult{}, safeFailure(err)
	}
	input.lease = lease
	go func() {
		select {
		case <-lease.Done():
			cancel()
		case <-dispatch.Done():
		}
	}()
	check := func(checkCtx context.Context) error {
		if dispatch.Err() != nil || input.ctx.Err() != nil || entry.epoch != host.epoch.Load() {
			return computer.ErrStale
		}
		return lease.Check(checkCtx)
	}
	if err = check(dispatch); err != nil {
		input.lease = nil
		_ = lease.Close()
		return nodes.ComputerActResult{}, safeFailure(err)
	}
	input.observed = observedInput{}
	host.outputs.removeSession(status.SessionID)
	disposition := input.execute(dispatch, InputAuthorization{
		Request: request, Evidence: witness.evidence, Window: guard.Identity(), Lease: lease, Check: check,
	})
	var observed nodes.ComputerObservationResult
	if disposition == InputEffectVerified && check(dispatch) == nil {
		observed, err = host.observeLocked(dispatch, entry, current, nodes.ComputerHostObserveRequest{
			ComputerHostStatusRequest: status, Generation: current.Binding.Generation,
			WorkspaceID: action.WorkspaceID, InvocationID: request.ID,
		})
	} else {
		err = computer.ErrUnavailable
	}
	switch disposition {
	case InputNotDispatched, InputUncertainQuiescent, InputEffectVerified:
		if closeErr := lease.Close(); closeErr == nil {
			input.lease = nil
			switch disposition {
			case InputNotDispatched:
				if ctx.Err() != nil {
					return nodes.ComputerActResult{}, companion.ErrComputerInputCancellationConfirmed
				}
				return nodes.ComputerActResult{}, computer.ErrDenied
			case InputEffectVerified:
				if err == nil && ctx.Err() == nil && entry.epoch == host.epoch.Load() {
					return nodes.ComputerActResult{ActionID: request.ID, Observation: observed}, nil
				}
			}
		}
	}
	input.faulted = true
	input.observed = observedInput{}
	host.outputs.removeSession(status.SessionID)
	return nodes.ComputerActResult{}, companion.ErrInvocationOutcomeUnknown
}
