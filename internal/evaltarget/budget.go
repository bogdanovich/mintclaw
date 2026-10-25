// Package evaltarget defines the metadata-only protocol for a bounded owned
// evaluation child. It does not admit profiles or authorize tools/model access.
package evaltarget

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	Version      = 1
	Scope        = "owned_gateway_agent_providers"
	MaxJSONBytes = 4096
	FlagName     = "owned-eval-call-budget"
)

var ErrBudgetProtocol = errors.New("owned eval call-budget protocol unavailable or invalid")

type Spec struct {
	Version int                        `json:"version"`
	Nonce   string                     `json:"nonce"`
	Limits  providers.CallBudgetLimits `json:"limits"`
}

type Proof struct {
	Version      int                          `json:"version"`
	Scope        string                       `json:"scope"`
	Nonce        string                       `json:"nonce"`
	PID          int                          `json:"pid"`
	GatewayClean bool                         `json:"gateway_clean"`
	Snapshot     providers.CallBudgetSnapshot `json:"snapshot"`
}

func (spec Spec) Encode() (string, error) {
	if _, err := spec.budget(); err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil || len(data) > MaxJSONBytes {
		return "", ErrBudgetProtocol
	}
	return string(data), nil
}

func (spec Spec) budget() (*providers.CallBudget, error) {
	id, err := uuid.Parse(spec.Nonce)
	if err != nil || id.String() != spec.Nonce || id.Version() != 4 || id.Variant() != uuid.RFC4122 ||
		spec.Version != Version {
		return nil, ErrBudgetProtocol
	}
	budget, err := providers.NewCallBudget(spec.Limits)
	if err != nil {
		return nil, ErrBudgetProtocol
	}
	return budget, nil
}

// ParseSpec accepts only the canonical private-launch representation. This
// rejects duplicate/unknown fields, trailing data and repaired invalid UTF-8.
func ParseSpec(encoded string) (Spec, *providers.CallBudget, error) {
	var spec Spec
	if len(encoded) == 0 || len(encoded) > MaxJSONBytes || !utf8.ValidString(encoded) ||
		json.Unmarshal([]byte(encoded), &spec) != nil {
		return Spec{}, nil, ErrBudgetProtocol
	}
	canonical, err := spec.Encode()
	if err != nil || canonical != encoded {
		return Spec{}, nil, ErrBudgetProtocol
	}
	budget, err := spec.budget()
	return spec, budget, err
}

func (spec Spec) ReceiptName() (string, error) {
	if _, err := spec.budget(); err != nil {
		return "", err
	}
	return "eval-call-budget-" + spec.Nonce + ".json", nil
}

// DecodeProof validates the frozen bytes against caller-retained launch
// identity, not a current PID or filesystem. A valid proof need not be passing:
// callers must assess shutdown, stop reason, nonzero work and known usage.
func DecodeProof(data []byte, spec Spec, expectedPID int) (Proof, error) {
	var proof Proof
	if len(data) == 0 || len(data) > MaxJSONBytes || !utf8.Valid(data) || json.Unmarshal(data, &proof) != nil {
		return Proof{}, ErrBudgetProtocol
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(canonical, data) || proof.validate(spec, expectedPID) != nil {
		return Proof{}, ErrBudgetProtocol
	}
	return proof, nil
}

func (proof Proof) validate(spec Spec, expectedPID int) error {
	if _, err := spec.budget(); err != nil {
		return err
	}
	snapshot := proof.Snapshot
	if proof.Version != Version || proof.Scope != Scope || proof.Nonce != spec.Nonce ||
		proof.PID <= 1 || proof.PID != expectedPID || !snapshot.Sealed || snapshot.Limits != spec.Limits ||
		snapshot.Calls < 0 || snapshot.Calls > spec.Limits.MaxCalls || snapshot.Completed < 0 ||
		snapshot.Completed > snapshot.Calls || snapshot.InFlight != snapshot.Calls-snapshot.Completed ||
		snapshot.InFlight > spec.Limits.MaxInFlight || snapshot.UnknownUsage < 0 ||
		snapshot.UnknownUsage > snapshot.Completed || snapshot.ReportedTokens < 0 ||
		snapshot.UsageKnown != (snapshot.Completed == snapshot.Calls && snapshot.UnknownUsage == 0) ||
		(snapshot.UnknownUsage != 0 && snapshot.StoppedReason == "") {
		return ErrBudgetProtocol
	}
	switch snapshot.StoppedReason {
	case "", "sealed", "calls", "concurrency", "output_limit", "input_limit", "media_unsupported",
		"unknown_usage", "reported_tokens":
		return nil
	default:
		return ErrBudgetProtocol
	}
}

// Recorder owns one anchored private destination for one child lifetime. It is
// single-use and caller-serialized; it never removes or adopts an old receipt.
type Recorder struct {
	root *os.Root
	spec Spec
	name string
	used bool
}

func NewRecorder(home string, spec Spec) (*Recorder, error) {
	home = filepath.Clean(home)
	name, err := spec.ReceiptName()
	if err != nil || !filepath.IsAbs(home) || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, ErrBudgetProtocol
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrBudgetProtocol
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, ErrBudgetProtocol
	}
	anchored, statErr := root.Stat(".")
	_, existingErr := root.Lstat(name)
	if statErr != nil || anchored.Mode().Perm() != 0o700 || !os.SameFile(info, anchored) ||
		!errors.Is(existingErr, os.ErrNotExist) {
		_ = root.Close()
		return nil, ErrBudgetProtocol
	}
	return &Recorder{root: root, spec: spec, name: name}, nil
}

func (recorder *Recorder) Record(snapshot providers.CallBudgetSnapshot, gatewayClean bool) error {
	if recorder.used {
		return ErrBudgetProtocol
	}
	recorder.used = true
	proof := Proof{
		Version: Version, Scope: Scope, Nonce: recorder.spec.Nonce,
		PID: os.Getpid(), GatewayClean: gatewayClean, Snapshot: snapshot,
	}
	if proof.validate(recorder.spec, proof.PID) != nil {
		return ErrBudgetProtocol
	}
	data, err := json.Marshal(proof)
	if err != nil || len(data) > MaxJSONBytes {
		return ErrBudgetProtocol
	}
	file, err := recorder.root.OpenFile(recorder.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrBudgetProtocol
	}
	written, writeErr := file.Write(data)
	closeErr := errors.Join(file.Sync(), file.Close())
	if writeErr != nil || closeErr != nil || written != len(data) {
		return ErrBudgetProtocol
	}
	return nil
}

func (recorder *Recorder) Close() error {
	if err := recorder.root.Close(); err != nil {
		return ErrBudgetProtocol
	}
	return nil
}
