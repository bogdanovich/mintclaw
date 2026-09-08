package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func TestBindInboundMediaOwnerUsesExactActorAndRoute(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "inbound.bin")
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Source: "telegram"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent:      &AgentInstance{ID: "main", Workspace: "/workspace/main", Tools: tools.NewToolRegistry()},
		Allocation: session.Allocation{RouteScopeKey: "telegram:chat-1:topic-1"},
		SessionKey: "session-1",
	}
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "chat-1", TopicID: "topic-1", ActorID: "actor-a",
		},
		Media: []string{ref},
	}
	if bindErr := bindInboundMediaOwnerForTarget(store, target, msg); bindErr != nil {
		t.Fatal(bindErr)
	}
	ownerA, err := inboundMediaOwnerForTarget(target, msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, resolveErr := store.ResolveOwnedWithMeta(ref, ownerA); resolveErr != nil {
		t.Fatalf("exact owner failed to resolve: %v", resolveErr)
	}
	msg.Context.ActorID = "actor-b"
	ownerB, err := inboundMediaOwnerForTarget(target, msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, resolveErr := store.ResolveOwnedWithMeta(ref, ownerB); resolveErr == nil {
		t.Fatal("other actor resolved bound inbound media")
	}
}

func TestInboundMediaOwnerSeparatesEffectiveSessionsOnOneRoute(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "inbound.bin")
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Source: "telegram"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	msg := bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram", ChatID: "chat-1", ActorID: "actor-a"},
		Media:   []string{ref},
	}
	first := &inboundDispatchTarget{
		Agent:      &AgentInstance{ID: "main", Workspace: "/workspace/main"},
		Allocation: session.Allocation{RouteScopeKey: "telegram:chat-1"},
		SessionKey: "session-1",
	}
	second := *first
	second.SessionKey = "session-2"
	ownerA, err := inboundMediaOwnerForTarget(first, msg)
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := inboundMediaOwnerForTarget(&second, msg)
	if err != nil {
		t.Fatal(err)
	}
	if ownerA.RouteID != ownerB.RouteID || ownerA.SessionID == ownerB.SessionID {
		t.Fatalf("owners do not isolate effective session: first=%#v second=%#v", ownerA, ownerB)
	}
	if err := store.BindOwner(ref, ownerA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ResolveOwnedWithMeta(ref, ownerB); err == nil {
		t.Fatal("second effective session resolved first session media")
	}
}

func TestBindInboundMediaOwnerDoesNotDependOnNodeUploadAuthority(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "inbound.bin")
	if err := os.WriteFile(path, []byte("unbound"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Source: "telegram"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	target := &inboundDispatchTarget{
		Agent:      &AgentInstance{ID: "main", Workspace: "/workspace/main", Tools: tools.NewToolRegistry()},
		Allocation: session.Allocation{RouteScopeKey: "telegram:chat-1"},
		SessionKey: "session-1",
	}
	msg := bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram", ChatID: "chat-1", ActorID: "actor-a"},
		Media:   []string{ref},
	}
	if bindErr := bindInboundMediaOwnerForTarget(store, target, msg); bindErr != nil {
		t.Fatal(bindErr)
	}
	owner, err := inboundMediaOwnerForTarget(target, msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, resolveErr := store.ResolveOwnedWithMeta(ref, owner); resolveErr != nil {
		t.Fatalf("ordinary inbound media was not owner-bound: %v", resolveErr)
	}
}

func TestBindNodeFileMediaOwnerStillRequiresUploadAuthority(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "tool-result.bin")
	if err := os.WriteFile(path, []byte("unbound"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Source: "tool"}, "result")
	if err != nil {
		t.Fatal(err)
	}
	ts := &turnState{
		agent:     &AgentInstance{ID: "main", Tools: tools.NewToolRegistry()},
		workspace: "/workspace/main",
		channel:   "telegram",
		chatID:    "chat-1",
		opts: freezeTurnInput(turnSpec{Dispatch: DispatchRequest{
			RouteSessionKey: "telegram:chat-1",
			SessionKey:      "session-1",
			InboundContext: &bus.InboundContext{
				Channel: "telegram", ChatID: "chat-1", ActorID: "actor-a",
			},
		}}),
	}
	if bindErr := bindNodeFileMediaOwner(store, ts, []string{ref}); bindErr != nil {
		t.Fatal(bindErr)
	}
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, resolveErr := store.ResolveOwnedWithMeta(ref, owner); resolveErr == nil {
		t.Fatal("tool media was bound without nodes_upload authority")
	}
}

func TestProjectNodeFileMediaAttachmentsExposesOpaqueRefWithoutGatewayPath(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "inbound.png")
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename: "inbound.png", ContentType: "image/png",
	}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewToolRegistry()
	registry.Register(tools.NewNodeUploadTool(tools.NewNodeToolOptions(nil), nil))
	ts := &turnState{agent: &AgentInstance{ID: "main", Tools: registry}}
	messages := projectNodeFileMediaAttachments(
		[]providers.Message{{Role: "user", Content: "upload this", Media: []string{ref}}},
		ts,
		[]string{ref},
		store,
	)
	resolved := resolveMediaRefs(messages, store, nil, 1024)
	if len(resolved) != 1 || len(resolved[0].Attachments) != 1 ||
		resolved[0].Attachments[0].Ref != ref || resolved[0].Attachments[0].Filename != "inbound.png" {
		t.Fatalf("projected messages = %#v", resolved)
	}
	if len(resolved[0].Media) != 0 || strings.Contains(resolved[0].Content, path) {
		t.Fatalf("projected provider content leaked gateway path: %#v", resolved[0])
	}
}
