package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type runtimeComposerTestTool struct {
	name  string
	value string
	caps  []runtimecap.CapabilityID
}

func (tool *runtimeComposerTestTool) Name() string          { return tool.name }
func (tool *runtimeComposerTestTool) Description() string   { return tool.value }
func (*runtimeComposerTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (tool *runtimeComposerTestTool) RuntimeCapabilities() []runtimecap.CapabilityID {
	return append([]runtimecap.CapabilityID(nil), tool.caps...)
}

func (tool *runtimeComposerTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.NewToolResult(tool.value)
}

func TestRuntimeToolComposerPublishesToolOwnedCapabilities(t *testing.T) {
	documentTool := &runtimeComposerTestTool{
		name: "document", value: "read-only",
		caps: []runtimecap.CapabilityID{
			runtimecap.CapabilityDocumentInspect,
			runtimecap.CapabilityDocumentExtract,
		},
	}
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		func(name string) bool { return name != "document" },
		newRuntimeToolSetContributor("document.feature", runtimeToolCandidate{tool: documentTool}),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range documentTool.caps {
		availability, found := composer.CapabilityReport().Lookup(capability)
		if !found || availability.Available || availability.Reason == nil ||
			availability.Reason.Code != runtimecap.ReasonPolicyDisabled {
			t.Fatalf("tool-owned capability %s = %#v, found=%t", capability, availability, found)
		}
	}
}

func TestRuntimeToolComposerPublishesAtomicallyIntoStableRegistry(t *testing.T) {
	hidden := &runtimeComposerTestTool{name: "document", value: "document"}
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		nil,
		newRuntimeToolSetContributor(
			"gateway.base",
			runtimeToolCandidate{tool: &runtimeComposerTestTool{name: "read_file", value: "read"}},
			runtimeToolCandidate{tool: hidden, hidden: true},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := composer.Registry()
	registry.PromoteTools([]string{"document"}, 3)
	if err = composer.PutTool(
		"gateway.runtime.status",
		&runtimeComposerTestTool{name: "runtime_status", value: "status"},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if composer.Registry() != registry {
		t.Fatal("composer replaced the stable registry identity")
	}
	if _, ok := registry.Get("document"); !ok {
		t.Fatal("recomposition dropped the hidden tool's live TTL")
	}
	if _, ok := registry.Get("runtime_status"); !ok {
		t.Fatal("late-bound runtime tool is unavailable")
	}
	status, ok := composer.CapabilityReport().LookupTool("runtime_status")
	if !ok || !status.Available {
		t.Fatalf("late-bound runtime tool report = %#v, %t", status, ok)
	}
}

func TestRuntimeToolComposerRejectsCrossOwnerCollisionWithoutPublishing(t *testing.T) {
	base := &runtimeComposerTestTool{name: "read_file", value: "base"}
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		nil,
		newRuntimeToolSetContributor("gateway.base", runtimeToolCandidate{tool: base}),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := composer.Registry()
	if err = composer.PutTool(
		"gateway.runtime.override",
		&runtimeComposerTestTool{name: "read_file", value: "override"},
		false,
	); err == nil {
		t.Fatal("PutTool() collision error = nil")
	}
	registered, ok := registry.Get("read_file")
	if !ok || registered != base {
		t.Fatalf("registry changed after rejected plan: %#v", registered)
	}
}

func TestRuntimeToolComposerPolicyCannotHideCandidateCollision(t *testing.T) {
	base := &runtimeComposerTestTool{name: "read_file", value: "base"}
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		func(string) bool { return false },
		newRuntimeToolSetContributor("gateway.base", runtimeToolCandidate{tool: base}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if composer.Registry().HasRegistered("read_file") {
		t.Fatal("policy-disabled base tool was admitted")
	}
	if err = composer.PutTool(
		"gateway.runtime.override",
		&runtimeComposerTestTool{name: "read_file", value: "override"},
		false,
	); err == nil {
		t.Fatal("policy-disabled candidate hid a cross-owner collision")
	}
	if composer.Registry().HasRegistered("read_file") {
		t.Fatal("rejected collision changed the admitted registry")
	}
}

func TestRuntimeToolComposerReplacesItsOwnedLateBoundTool(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	first := &runtimeComposerTestTool{name: "coding_attachment", value: "first"}
	second := &runtimeComposerTestTool{name: "coding_attachment", value: "second"}
	if err = composer.PutTool("coding.attachment", first, false); err != nil {
		t.Fatal(err)
	}
	if err = composer.PutTool("coding.attachment", second, false); err != nil {
		t.Fatal(err)
	}
	registered, ok := composer.Registry().Get("coding_attachment")
	if !ok || registered != second {
		t.Fatalf("late-bound replacement = %#v", registered)
	}
}

func TestRuntimeToolComposerCannotMutateSealedCodingCatalog(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		nil,
		newRuntimeToolSetContributor(
			"coding.base",
			runtimeToolCandidate{tool: &runtimeComposerTestTool{name: "read_file", value: "read"}},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	composer.Registry().Seal()
	if err = composer.PutTool(
		"coding.attachment",
		&runtimeComposerTestTool{name: "coding_attachment", value: "attachment"},
		false,
	); err == nil {
		t.Fatal("PutTool() error = nil for sealed coding registry")
	}
	if composer.Registry().HasRegistered("coding_attachment") {
		t.Fatal("sealed coding registry admitted a late tool")
	}
}

func TestRuntimeToolComposerRetainsFeatureCapabilityDiagnostics(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		nil,
		newRuntimeToolSetContributor("document.feature").withCapabilityReport(
			runtimecap.Unavailable(
				runtimecap.CapabilityDocumentInspect,
				runtimecap.ReasonRuntimeUnsupported,
			),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = composer.PutTool(
		"coding.runtime.status",
		&runtimeComposerTestTool{name: "runtime_status", value: "status"},
		false,
	); err != nil {
		t.Fatal(err)
	}
	availability, ok := composer.CapabilityReport().Lookup(runtimecap.CapabilityDocumentInspect)
	if !ok || availability.Available || availability.Reason == nil ||
		availability.Reason.Code != runtimecap.ReasonRuntimeUnsupported {
		t.Fatalf("retained document capability = %#v, %t", availability, ok)
	}
}

func TestRuntimeToolComposerPublishesLateFeatureToolAndCapabilitiesAtomically(t *testing.T) {
	composer, err := newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		nil,
		newRuntimeToolSetContributor("coding.base"),
	)
	if err != nil {
		t.Fatal(err)
	}
	documentTool := &runtimeComposerTestTool{name: "document", value: "read-only"}
	contributor := newRuntimeToolSetContributor(
		"coding.media",
		runtimeToolCandidate{tool: documentTool},
	).withCapability(
		runtimecap.CapabilityDocumentInspect,
		documentTool.Name(),
	).withCapability(
		runtimecap.CapabilityDocumentExtract,
		documentTool.Name(),
	)
	if err = composer.PutContributor(contributor); err != nil {
		t.Fatal(err)
	}
	registered, ok := composer.Registry().Get(documentTool.Name())
	if !ok || registered != documentTool {
		t.Fatalf("late feature tool = %#v, %t", registered, ok)
	}
	for _, capability := range []runtimecap.CapabilityID{
		runtimecap.CapabilityDocumentInspect,
		runtimecap.CapabilityDocumentExtract,
	} {
		availability, found := composer.CapabilityReport().Lookup(capability)
		if !found || !availability.Available {
			t.Fatalf("late feature capability %s = %#v, %t", capability, availability, found)
		}
	}

	unavailable := newRuntimeToolSetContributor("coding.media").withCapabilityReport(
		runtimecap.Unavailable(
			runtimecap.CapabilityDocumentInspect,
			runtimecap.ReasonServiceUnavailable,
		),
	)
	if err = composer.PutContributor(unavailable); err != nil {
		t.Fatal(err)
	}
	if composer.Registry().HasRegistered(documentTool.Name()) {
		t.Fatal("replaced feature contributor left a stale tool")
	}
	availability, found := composer.CapabilityReport().Lookup(runtimecap.CapabilityDocumentInspect)
	if !found || availability.Available || availability.Reason == nil ||
		availability.Reason.Code != runtimecap.ReasonServiceUnavailable {
		t.Fatalf("replaced feature capability = %#v, %t", availability, found)
	}
}
