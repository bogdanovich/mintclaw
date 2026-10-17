package nodes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

func computerTestProfile() ComputerProfileDescriptor {
	return ComputerProfileDescriptor{
		Alias: "desktop", Revision: strings.Repeat("a", 64), OS: computer.OSLinux,
		Mode: computer.ModeAttached, Isolation: computer.IsolationNone, Backend: "fixture",
	}
}

func computerTestDescriptors(t *testing.T) []CommandDescriptor {
	t.Helper()
	descriptors, err := ComputerCommandDescriptors([]ComputerProfileDescriptor{computerTestProfile()})
	if err != nil {
		t.Fatal(err)
	}
	return descriptors
}

func computerTestObservation() ComputerObservationResult {
	profile := computerTestProfile()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	binding := computer.Binding{
		Principal: runtimecap.Principal{
			Runtime:     runtimecap.KindGateway,
			ActorID:     "actor",
			AgentID:     "agent",
			SessionID:   "thread",
			ExecutionID: "turn",
		},
		SessionID: "session", WorkerID: "worker", PolicyRevision: profile.Revision, Generation: 1,
		Target: computer.Target{
			Alias: profile.Alias, Placement: computer.PlacementNode, Node: "node_test", OS: profile.OS,
			Mode: profile.Mode, Isolation: profile.Isolation, Backend: profile.Backend,
			Scope: computer.Scope{DesktopID: "desktop", DisplayID: "display", WindowID: "window", WindowGeneration: 7},
		},
	}
	return ComputerObservationResult{
		Session: computer.Session{Binding: binding, Control: computer.ControlAgent, ExpiresAt: now.Add(time.Hour)},
		Observation: computer.Observation{
			ID: "observation", Binding: binding, Geometry: computer.Geometry{Width: 10, Height: 20, Scale: 1},
			CapturedAt: now, ExpiresAt: now.Add(time.Minute),
		},
		ObservedAt: now, AccessibilityText: "private synthetic observation canary",
	}
}

func TestComputerDescriptorsAreClosedInternalAndComplete(t *testing.T) {
	descriptors := computerTestDescriptors(t)
	if len(descriptors) != 8 {
		t.Fatal("incomplete computer command family")
	}
	if err := (CapabilityCatalog{Commands: descriptors}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		if err := descriptor.Validate(); err != nil || descriptor.ModelContract != nil {
			t.Fatal(descriptor.Name, err)
		}
		for _, mutation := range []func(*CommandDescriptor){
			func(d *CommandDescriptor) { d.ModelContract = &CommandModelContract{} },
			func(d *CommandDescriptor) { d.InputSchema = json.RawMessage(`{"type":"object"}`) },
			func(d *CommandDescriptor) { d.OutputSchema = json.RawMessage(`{"type":"object"}`) },
			func(d *CommandDescriptor) { d.ComputerProfiles = nil },
			func(d *CommandDescriptor) { d.SupportsCancel = false },
			func(d *CommandDescriptor) { d.SupportsProgress = true },
		} {
			changed := descriptor
			mutation(&changed)
			if changed.Validate() == nil {
				t.Fatal("forged computer descriptor admitted", descriptor.Name)
			}
		}
	}
	if err := (CapabilityCatalog{Commands: descriptors[:7]}).Validate(); err == nil {
		t.Fatal("partial computer family admitted")
	}
	changed := descriptors[0]
	changed.Name = "computer.click.v1"
	changed.ComputerProfiles = nil
	if changed.Validate() == nil {
		t.Fatal("unknown raw computer command admitted")
	}
	changed.Name = "node.info.v1"
	changed.ComputerProfiles = []ComputerProfileDescriptor{computerTestProfile()}
	if changed.Validate() == nil {
		t.Fatal("computer metadata on unrelated command admitted")
	}
}

func TestComputerSchemaCacheCannotBeChangedByCallers(t *testing.T) {
	for _, command := range computerCommands {
		input := ComputerCommandInputSchema(command.name)
		output := ComputerCommandOutputSchema(command.name)
		input[0], output[0] = '!', '!'
		if !json.Valid(ComputerCommandInputSchema(command.name)) ||
			!json.Valid(ComputerCommandOutputSchema(command.name)) {
			t.Fatal("caller modified canonical computer schema", command.name)
		}
	}
}

func TestComputerInputDoesNotAcceptNativeAuthorityOrForgedProfileRevision(t *testing.T) {
	descriptors := computerTestDescriptors(t)
	for _, descriptor := range descriptors {
		input := map[string]any{"runtime": "gateway", "execution_id": "turn"}
		switch descriptor.Name {
		case ComputerCommandOpen:
			input["profile"], input["profile_revision"] = "desktop", computerTestProfile().Revision
		case ComputerCommandTargets:
		case ComputerCommandStatus:
			input["session_id"] = "session"
		default:
			input["session_id"], input["generation"] = "session", 1
			if descriptor.Name == ComputerCommandObserve {
				input["workspace_id"] = "workspace"
			}
		}
		if err := validateDescriptorInvocationInput(descriptor, input); err != nil {
			t.Fatal(descriptor.Name, err)
		}
		for _, field := range []string{"pid", "window_id", "display", "xauthority", "actor_id", "agent_id", "principal", "event", "operation", "argv"} {
			input[field] = "not-authority"
			if validateDescriptorInvocationInput(descriptor, input) == nil {
				t.Fatal("native/model owner authority admitted", field)
			}
			delete(input, field)
		}
		input["runtime"] = "unknown"
		if validateDescriptorInvocationInput(descriptor, input) == nil {
			t.Fatal("unknown runtime admitted")
		}
		input["runtime"] = "gateway"
		input["execution_id"] = "bad\nturn"
		if validateDescriptorInvocationInput(descriptor, input) == nil {
			t.Fatal("malformed turn admitted")
		}
		input["execution_id"] = "turn"
		if descriptor.Name == ComputerCommandOpen {
			input["profile_revision"] = strings.Repeat("b", 64)
			if validateDescriptorInvocationInput(descriptor, input) == nil {
				t.Fatal("unadvertised profile revision admitted")
			}
		}
	}
}

func TestComputerObservationWireIsScopedLiveOnlyAndClockExplicit(t *testing.T) {
	var descriptor CommandDescriptor
	for _, candidate := range computerTestDescriptors(t) {
		if candidate.Name == ComputerCommandObserve {
			descriptor = candidate
		}
	}
	result := computerTestObservation()
	// This is a host-clock shape check, deliberately not proof of gateway
	// freshness: an authenticated companion's clock may differ from this clock.
	data := mustJSON(result)
	if _, err := ValidateInvocationOutput(descriptor, data, MaxInvocationOutput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), "canary") {
		t.Fatal("ordinary diagnostics exposed protected data")
	}
	if _, err := ValidateInvocationOutput(
		descriptor,
		json.RawMessage(`{"protected_result":true}`),
		MaxInvocationOutput,
	); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ComputerObservationResult){
		func(r *ComputerObservationResult) { r.Observation.Binding.Generation++ },
		func(r *ComputerObservationResult) { r.Session.Control = computer.ControlHuman },
		func(r *ComputerObservationResult) { r.Observation.Geometry.Scale = 0 },
		func(r *ComputerObservationResult) { r.Observation.ExpiresAt = r.ObservedAt },
		func(r *ComputerObservationResult) { r.Observation.CapturedAt = r.ObservedAt.Add(time.Second) },
		func(r *ComputerObservationResult) { r.Observation.ExpiresAt = r.ObservedAt.Add(2 * time.Minute) },
		func(r *ComputerObservationResult) {
			r.AccessibilityText = strings.Repeat("x", computer.MaxAccessibilityBytes+1)
		},
		func(r *ComputerObservationResult) { r.Observation.Binding.Target.Scope.WindowID = "" },
	} {
		changed := result
		mutate(&changed)
		if _, err := ValidateInvocationOutput(descriptor, mustJSON(changed), MaxInvocationOutput); err == nil {
			t.Fatal("invalid observation admitted")
		}
	}
	result.Observation.ContentType = "image/png"
	result.Output = &ComputerOutputDescriptor{
		TransferID: "transfer", Binding: result.Observation.Binding, ObservationID: result.Observation.ID,
		InvocationID: "invocation", WorkspaceID: "workspace", ContentType: "image/png", Size: 100,
		SHA256: strings.Repeat("b", 64), ExpiresAt: result.Observation.ExpiresAt,
	}
	if _, err := ValidateInvocationOutput(descriptor, mustJSON(result), MaxInvocationOutput); err != nil {
		t.Fatal(err)
	}
	result.Output.Binding.Target.Scope.WindowGeneration++
	if _, err := ValidateInvocationOutput(descriptor, mustJSON(result), MaxInvocationOutput); err == nil {
		t.Fatal("image bound to replacement window admitted")
	}
}
