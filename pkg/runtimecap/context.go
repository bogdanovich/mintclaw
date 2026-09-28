// Package runtimecap defines runtime-neutral capability inputs and diagnostics.
package runtimecap

import (
	"context"
	"reflect"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

// ArtifactAccess is the runtime-owned store available to feature tools. It is
// intentionally the existing media lifecycle contract until document-specific
// artifacts are separated in C3.
type ArtifactAccess interface {
	media.MediaStore
}

// Delivery is the optional channel delivery boundary. Feature tools must not
// infer delivery authority merely because a message bus exists.
type Delivery interface {
	SendMessage(context.Context, bus.OutboundMessage) error
	SendMedia(context.Context, bus.OutboundMediaMessage) error
}

// BrowserClient is the minimum availability boundary required by capability
// composition. Browser actions remain on the feature-owned source until C4.
type BrowserClient interface {
	Available() bool
}

// Inputs are supplied by a trusted runtime composition root. Project
// instructions and skills are prompt inputs and never participate here.
type Inputs struct {
	Kind      Kind
	Artifacts ArtifactAccess
	Delivery  Delivery
	Browser   BrowserClient
}

// Context is an immutable runtime-generation snapshot. BindPrincipal returns a
// copy for one turn and never mutates the construction-time service inputs.
type Context struct {
	kind      Kind
	principal *Principal
	artifacts ArtifactAccess
	delivery  Delivery
	browser   BrowserClient
}

func NewContext(inputs Inputs) Context {
	return Context{
		kind:      inputs.Kind,
		artifacts: nonNilArtifactAccess(inputs.Artifacts),
		delivery:  nonNilDelivery(inputs.Delivery),
		browser:   nonNilBrowserClient(inputs.Browser),
	}
}

func (runtime Context) Kind() Kind {
	return runtime.kind
}

func (runtime Context) BindPrincipal(principal Principal) Context {
	bound := runtime
	bound.principal = nil
	if principal.Runtime != runtime.kind || principal.Validate() != nil {
		return bound
	}
	cloned := principal
	bound.principal = &cloned
	return bound
}

func (runtime Context) Principal() (Principal, bool) {
	if runtime.principal == nil {
		return Principal{}, false
	}
	return *runtime.principal, true
}

func (runtime Context) ArtifactAccess() (ArtifactAccess, bool) {
	return runtime.artifacts, runtime.artifacts != nil
}

func (runtime Context) Delivery() (Delivery, bool) {
	return runtime.delivery, runtime.delivery != nil
}

func (runtime Context) BrowserClient() (BrowserClient, bool) {
	return runtime.browser, runtime.browser != nil
}

func (runtime Context) Report() Report {
	principalAvailability := Unavailable(CapabilityRuntimePrincipal, ReasonIdentityIncomplete)
	if _, ok := runtime.Principal(); ok {
		principalAvailability = Available(CapabilityRuntimePrincipal)
	}

	artifactRead := Unavailable(CapabilityArtifactRead, ReasonNotConfigured)
	artifactWrite := Unavailable(CapabilityArtifactWrite, ReasonNotConfigured)
	if runtime.artifacts != nil {
		artifactRead = Available(CapabilityArtifactRead)
		artifactWrite = Available(CapabilityArtifactWrite)
	}

	delivery := Unavailable(CapabilityChannelDelivery, ReasonNotConfigured)
	if runtime.delivery != nil {
		delivery = Available(CapabilityChannelDelivery)
	}

	browser := Unavailable(CapabilityBrowserClient, ReasonNotConfigured)
	if runtime.browser != nil {
		if runtime.browser.Available() {
			browser = Available(CapabilityBrowserClient)
		} else {
			browser = Unavailable(CapabilityBrowserClient, ReasonServiceUnavailable)
		}
	}

	return NewReport(
		runtime.kind,
		principalAvailability,
		artifactRead,
		artifactWrite,
		delivery,
		browser,
	)
}

func nonNilArtifactAccess(value ArtifactAccess) ArtifactAccess {
	if interfaceNil(value) {
		return nil
	}
	return value
}

func nonNilDelivery(value Delivery) Delivery {
	if interfaceNil(value) {
		return nil
	}
	return value
}

func nonNilBrowserClient(value BrowserClient) BrowserClient {
	if interfaceNil(value) {
		return nil
	}
	return value
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
