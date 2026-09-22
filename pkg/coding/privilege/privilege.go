// Package privilege defines the private, node-local privileged execution
// contract for a coding worker. It deliberately contains no gateway or
// configuration types: privileged authority is resolved by the companion
// before the worker starts and never crosses the gateway task record.
package privilege

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	BackendAuthorityBroker = "authority-broker"
	MaxScriptBytes         = 64 * 1024
	MaxTimeoutSeconds      = 60 * 60
	MaxOutputBytes         = 128 * 1024
	MaxIdentityBytes       = 128
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

var (
	// ErrCancellationConfirmed means the privileged backend proved that its
	// complete process domain is empty after cancellation.
	ErrCancellationConfirmed = errors.New("privileged process-domain termination confirmed")
	// ErrOutcomeUnknown means execution may have started but the backend could
	// not authenticate a terminal outcome. Callers must not retry blindly.
	ErrOutcomeUnknown = errors.New("privileged execution outcome is unknown")
)

// Binding is immutable worker-local authority. Endpoint is a local Unix
// socket path and is never copied into a durable task or gateway projection.
type Binding struct {
	Backend           string `json:"backend"`
	Endpoint          string `json:"endpoint"`
	BrokerRevision    string `json:"broker_revision"`
	Profile           string `json:"profile"`
	ProfileRevision   string `json:"profile_revision"`
	WorkingScope      string `json:"working_scope"`
	TimeoutSecondsMax int    `json:"timeout_seconds_max"`
	OutputBytesMax    int    `json:"output_bytes_max"`
}

func (binding Binding) Validate() error {
	endpoint := strings.TrimSpace(binding.Endpoint)
	if binding.Backend != BackendAuthorityBroker || endpoint == "" ||
		!filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint ||
		strings.ContainsRune(endpoint, 0) ||
		!validAlias(binding.BrokerRevision) || !validAlias(binding.ProfileRevision) ||
		!validAlias(binding.Profile) || !validAlias(binding.WorkingScope) ||
		binding.TimeoutSecondsMax <= 0 || binding.TimeoutSecondsMax > MaxTimeoutSeconds ||
		binding.OutputBytesMax <= 0 || binding.OutputBytesMax > MaxOutputBytes {
		return errors.New("privileged coding binding is invalid")
	}
	return nil
}

func validAlias(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxIdentityBytes {
		return false
	}
	return identityPattern.MatchString(value)
}

// Request contains only one model-authored shell script and bounded runtime
// choices. Profile, working directory, environment, backend, and endpoint are
// fixed by Binding and cannot be selected by the model.
type Request struct {
	InvocationID   string
	Script         string
	TimeoutSeconds int
}

func (request Request) Validate(binding Binding) error {
	if binding.Validate() != nil || !validAlias(request.InvocationID) ||
		len(request.Script) == 0 || len(request.Script) > MaxScriptBytes ||
		strings.ContainsRune(request.Script, 0) || request.TimeoutSeconds <= 0 ||
		request.TimeoutSeconds > binding.TimeoutSecondsMax {
		return errors.New("privileged coding request is invalid")
	}
	return nil
}

type Result struct {
	ExitCode    int
	Stdout      string
	Stderr      string
	Signal      string
	Truncated   bool
	StartedAt   int64
	CompletedAt int64
}

func (result Result) Validate(binding Binding) error {
	if binding.Validate() != nil || result.StartedAt <= 0 || result.CompletedAt < result.StartedAt ||
		len(result.Stdout)+len(result.Stderr) > binding.OutputBytesMax || len(result.Signal) > 32 ||
		strings.ContainsAny(result.Signal, "\r\n\x00") {
		return errors.New("privileged coding result is invalid")
	}
	return nil
}

// Executor owns backend revalidation and execution. Implementations must
// compare the current backend snapshot with Binding before every invocation.
type Executor interface {
	Binding() Binding
	Execute(context.Context, Request) (Result, error)
}

func Require(executor Executor) (Binding, error) {
	if executor == nil {
		return Binding{}, errors.New("privileged coding executor is required")
	}
	binding := executor.Binding()
	if err := binding.Validate(); err != nil {
		return Binding{}, fmt.Errorf("validate privileged coding executor: %w", err)
	}
	return binding, nil
}
