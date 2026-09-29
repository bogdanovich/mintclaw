package runtimecap

import (
	"sort"
	"strings"
)

type CapabilityID string

const (
	CapabilityRuntimePrincipal CapabilityID = "runtime.principal"
	CapabilityArtifactRead     CapabilityID = "artifact.read"
	CapabilityArtifactWrite    CapabilityID = "artifact.write"
	CapabilityChannelDelivery  CapabilityID = "delivery.channel"
	CapabilityBrowserClient    CapabilityID = "browser.client"

	CapabilityDocumentInspect CapabilityID = "document.inspect"
	CapabilityDocumentExtract CapabilityID = "document.extract"
	CapabilityDocumentRender  CapabilityID = "document.render"
	CapabilityDocumentFields  CapabilityID = "document.fields"
	CapabilityDocumentFill    CapabilityID = "document.fill"
	CapabilityDocumentVerify  CapabilityID = "document.verify"
	CapabilityDocumentForm    CapabilityID = "document.form"
	CapabilityBrowserObserve  CapabilityID = "browser.observe"
	CapabilityBrowserAct      CapabilityID = "browser.act"
	CapabilityBrowserWorkflow CapabilityID = "browser.workflow"
	CapabilityBrowserCapture  CapabilityID = "browser.capture"
	CapabilityBrowserDownload CapabilityID = "browser.download"
)

func (capability CapabilityID) Valid() bool {
	switch capability {
	case CapabilityRuntimePrincipal,
		CapabilityArtifactRead,
		CapabilityArtifactWrite,
		CapabilityChannelDelivery,
		CapabilityBrowserClient,
		CapabilityDocumentInspect,
		CapabilityDocumentExtract,
		CapabilityDocumentRender,
		CapabilityDocumentFields,
		CapabilityDocumentFill,
		CapabilityDocumentVerify,
		CapabilityDocumentForm,
		CapabilityBrowserObserve,
		CapabilityBrowserAct,
		CapabilityBrowserWorkflow,
		CapabilityBrowserCapture,
		CapabilityBrowserDownload:
		return true
	default:
		return false
	}
}

// ParseCapabilityID accepts only the bounded capability vocabulary owned by
// the runtime. Manifests may require these identifiers but cannot invent new
// authority by naming an arbitrary string.
func ParseCapabilityID(value string) (CapabilityID, bool) {
	capability := CapabilityID(strings.ToLower(strings.TrimSpace(value)))
	return capability, capability.Valid()
}

type UnavailableReasonCode string

const (
	ReasonNotConfigured      UnavailableReasonCode = "not_configured"
	ReasonPolicyDisabled     UnavailableReasonCode = "policy_disabled"
	ReasonDependencyMissing  UnavailableReasonCode = "dependency_missing"
	ReasonServiceUnavailable UnavailableReasonCode = "service_unavailable"
	ReasonIdentityIncomplete UnavailableReasonCode = "identity_incomplete"
	ReasonRuntimeUnsupported UnavailableReasonCode = "runtime_unsupported"
)

func (reason UnavailableReasonCode) Valid() bool {
	switch reason {
	case ReasonNotConfigured,
		ReasonPolicyDisabled,
		ReasonDependencyMissing,
		ReasonServiceUnavailable,
		ReasonIdentityIncomplete,
		ReasonRuntimeUnsupported:
		return true
	default:
		return false
	}
}

type UnavailableReason struct {
	Code       UnavailableReasonCode `json:"code"`
	Dependency CapabilityID          `json:"dependency,omitempty"`
}

func (reason UnavailableReason) Valid() bool {
	if !reason.Code.Valid() {
		return false
	}
	if reason.Code == ReasonDependencyMissing {
		return reason.Dependency.Valid()
	}
	return reason.Dependency == ""
}

type Availability struct {
	Capability CapabilityID       `json:"capability"`
	Available  bool               `json:"available"`
	Reason     *UnavailableReason `json:"reason,omitempty"`
}

// ToolAvailability records one candidate tool after runtime policy has been
// applied. Missing tools are absent; denied candidates remain inspectable with
// a structured reason so compatibility diagnostics can distinguish policy
// from an unavailable implementation.
type ToolAvailability struct {
	Name      string             `json:"name"`
	Available bool               `json:"available"`
	Reason    *UnavailableReason `json:"reason,omitempty"`
}

func Available(capability CapabilityID) Availability {
	return Availability{Capability: capability, Available: capability.Valid()}
}

func Unavailable(capability CapabilityID, reason UnavailableReasonCode) Availability {
	if !capability.Valid() || !reason.Valid() {
		return Availability{}
	}
	return Availability{Capability: capability, Reason: &UnavailableReason{Code: reason}}
}

func DependencyUnavailable(capability, dependency CapabilityID) Availability {
	if !capability.Valid() || !dependency.Valid() {
		return Availability{}
	}
	return Availability{
		Capability: capability,
		Reason: &UnavailableReason{
			Code:       ReasonDependencyMissing,
			Dependency: dependency,
		},
	}
}

func ToolAvailable(name string) ToolAvailability {
	name = strings.TrimSpace(name)
	return ToolAvailability{Name: name, Available: name != ""}
}

func ToolUnavailable(name string, reason UnavailableReasonCode) ToolAvailability {
	name = strings.TrimSpace(name)
	if name == "" || !reason.Valid() || reason == ReasonDependencyMissing {
		return ToolAvailability{}
	}
	return ToolAvailability{Name: name, Reason: &UnavailableReason{Code: reason}}
}

type Report struct {
	Runtime      Kind               `json:"runtime"`
	Capabilities []Availability     `json:"capabilities"`
	Tools        []ToolAvailability `json:"tools,omitempty"`
}

func NewReport(kind Kind, availability ...Availability) Report {
	return NewAdmissionReport(kind, availability, nil)
}

// NewAdmissionReport canonicalizes feature capabilities and final tool
// admission into one immutable, deterministic runtime report.
func NewAdmissionReport(
	kind Kind,
	availability []Availability,
	tools []ToolAvailability,
) Report {
	byCapability := make(map[CapabilityID]Availability, len(availability))
	for _, entry := range availability {
		if !entry.Capability.Valid() || (!entry.Available && (entry.Reason == nil || !entry.Reason.Valid())) {
			continue
		}
		if entry.Available {
			entry.Reason = nil
		} else {
			reason := *entry.Reason
			entry.Reason = &reason
		}
		byCapability[entry.Capability] = entry
	}
	capabilities := make([]Availability, 0, len(byCapability))
	for _, entry := range byCapability {
		capabilities = append(capabilities, entry)
	}
	sort.Slice(capabilities, func(left, right int) bool {
		return capabilities[left].Capability < capabilities[right].Capability
	})
	byTool := make(map[string]ToolAvailability, len(tools))
	for _, entry := range tools {
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" || !entry.Available && (entry.Reason == nil || !entry.Reason.Valid()) {
			continue
		}
		if entry.Available {
			entry.Reason = nil
		} else {
			reason := *entry.Reason
			entry.Reason = &reason
		}
		byTool[entry.Name] = entry
	}
	toolAvailability := make([]ToolAvailability, 0, len(byTool))
	for _, entry := range byTool {
		toolAvailability = append(toolAvailability, entry)
	}
	sort.Slice(toolAvailability, func(left, right int) bool {
		return toolAvailability[left].Name < toolAvailability[right].Name
	})
	return Report{Runtime: kind, Capabilities: capabilities, Tools: toolAvailability}
}

func (report Report) Lookup(capability CapabilityID) (Availability, bool) {
	index := sort.Search(len(report.Capabilities), func(index int) bool {
		return report.Capabilities[index].Capability >= capability
	})
	if index >= len(report.Capabilities) || report.Capabilities[index].Capability != capability {
		return Availability{}, false
	}
	return report.Capabilities[index], true
}

func (report Report) LookupTool(name string) (ToolAvailability, bool) {
	name = strings.TrimSpace(name)
	index := sort.Search(len(report.Tools), func(index int) bool {
		return report.Tools[index].Name >= name
	})
	if index >= len(report.Tools) || report.Tools[index].Name != name {
		return ToolAvailability{}, false
	}
	entry := report.Tools[index]
	if entry.Reason != nil {
		reason := *entry.Reason
		entry.Reason = &reason
	}
	return entry, true
}

func (report Report) Clone() Report {
	return NewAdmissionReport(report.Runtime, report.Capabilities, report.Tools)
}
