package runtimecap

import "sort"

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
	CapabilityDocumentForm    CapabilityID = "document.form"
	CapabilityBrowserObserve  CapabilityID = "browser.observe"
	CapabilityBrowserAct      CapabilityID = "browser.act"
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
		CapabilityDocumentForm,
		CapabilityBrowserObserve,
		CapabilityBrowserAct,
		CapabilityBrowserCapture,
		CapabilityBrowserDownload:
		return true
	default:
		return false
	}
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

type Report struct {
	Runtime      Kind           `json:"runtime"`
	Capabilities []Availability `json:"capabilities"`
}

func NewReport(kind Kind, availability ...Availability) Report {
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
	return Report{Runtime: kind, Capabilities: capabilities}
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
