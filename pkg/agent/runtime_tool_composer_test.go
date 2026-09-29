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
}

func (tool *runtimeComposerTestTool) Name() string          { return tool.name }
func (tool *runtimeComposerTestTool) Description() string   { return tool.value }
func (*runtimeComposerTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (tool *runtimeComposerTestTool) Execute(context.Context, map[string]any) *toolshared.ToolResult {
	return toolshared.NewToolResult(tool.value)
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
