//go:build linux && amd64 && integration

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "document" && os.Args[2] == "_worker" {
		input := os.NewFile(document.WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(1)
		}
		err := document.ServeWorker(os.Stdin, input, os.Stdout)
		_ = input.Close()
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDocumentPDFTelegramVerticalSlice(t *testing.T) {
	requireDocumentReadBackend(t)

	t.Run("bounded text answer keeps extracted content live only", func(t *testing.T) {
		workspace := documentE2EWorkspace(t)
		store, ref, digest, sourcePath := documentE2ESource(t, "text.pdf")
		provider := documentTextE2EProvider(ref, digest, sourcePath)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-text-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(t, fixture.Bus, ref, "Read the marker in page 1 and cite the page.")

		waitDocumentE2E(t, func() bool { return len(channel.messagesSnapshot()) == 1 })
		messages := channel.messagesSnapshot()
		if messages[0].Content != "The marker is MintClaw text fixture [page 1]." {
			t.Fatalf("final answer = %q", messages[0].Content)
		}
		if err := provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", "MintClaw text fixture")
		for _, sessionKey := range fixture.Agent.Sessions.ListSessions() {
			history := fixture.Agent.Sessions.GetHistory(sessionKey)
			for _, message := range history {
				if message.Role == "tool" && strings.Contains(message.Content, "MintClaw text fixture") {
					t.Fatalf("durable tool result retained extracted text: %#v", message)
				}
				if strings.Contains(message.Content, sourcePath) {
					t.Fatalf("durable history leaked source path: %#v", message)
				}
			}
		}
	})

	t.Run("retained render uses the durable Telegram outbox", func(t *testing.T) {
		workspace := documentE2EWorkspace(t)
		store, ref, digest, sourcePath := documentE2ESource(t, "rotated-crop.pdf")
		provider := documentRenderE2EProvider(ref, digest, sourcePath)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), true)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-render-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(t, fixture.Bus, ref, "Render page 1, send it to me, then confirm the page.")

		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			channel.mu.Lock()
			ready := len(channel.sentMedia) >= 1 && len(channel.sentMessages) >= 1
			channel.mu.Unlock()
			if ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		channel.mu.Lock()
		mediaCount := len(channel.sentMedia)
		textCount := len(channel.sentMessages)
		if mediaCount == 0 || textCount == 0 {
			channel.mu.Unlock()
			t.Fatalf(
				"timed out waiting for Telegram delivery: media=%d text=%d",
				mediaCount,
				textCount,
			)
		}
		if mediaCount != 1 || textCount != 1 {
			channel.mu.Unlock()
			t.Fatalf("Telegram delivery count = (media %d, text %d), want exactly once", mediaCount, textCount)
		}
		delivered := channel.sentMedia[0]
		final := channel.sentMessages[0]
		channel.mu.Unlock()
		if delivered.Channel != "telegram" || delivered.ChatID != "pdf-chat" ||
			delivered.Context.TopicID != "pdf-topic" || len(delivered.Parts) != 1 {
			t.Fatalf("delivered media = %#v", delivered)
		}
		path, err := store.Resolve(delivered.Parts[0].Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("Telegram render delivery is not a PNG")
		}
		if final.Content != "Rendered and delivered source page 1." {
			t.Fatalf("final answer = %q", final.Content)
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf")
	})

	t.Run("verified form fill is delivered exactly once without retaining values", func(t *testing.T) {
		requireDocumentFormBackend(t)
		workspace := documentE2EWorkspace(t)
		home := filepath.Join(workspace, "instance")
		t.Setenv(config.EnvHome, home)
		store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
		fieldID := documentE2EFieldID(t, sourcePath, "full_name")
		privateValue := "MintClaw Agent Private Value"
		provider := documentFormE2EProvider(ref, digest, sourcePath, fieldID, privateValue)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
		})
		fixture.Loop.SetMediaStore(store)

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(
			t,
			fixture.Bus,
			ref,
			"Fill full_name with the protected value supplied for this call and send me the verified PDF.",
		)

		waitDocumentE2EChannel(t, channel, func() bool {
			channel.mu.Lock()
			defer channel.mu.Unlock()
			return len(channel.sentMedia) == 1 && len(channel.sentMessages) == 1
		})
		channel.mu.Lock()
		if len(channel.sentMedia) != 1 || len(channel.sentMessages) != 1 ||
			len(channel.sentMedia[0].Parts) != 1 {
			channel.mu.Unlock()
			t.Fatalf(
				"Telegram form delivery count = media %d text %d",
				len(channel.sentMedia),
				len(channel.sentMessages),
			)
		}
		delivered := channel.sentMedia[0]
		final := channel.sentMessages[0]
		channel.mu.Unlock()
		if delivered.DeliveryID == "" || delivered.Parts[0].ContentType != "application/pdf" ||
			delivered.Parts[0].Filename != "filled-document.pdf" ||
			final.Content != "Filled form delivered." {
			t.Fatalf("delivered form = %#v final = %#v", delivered, final)
		}
		path, err := store.Resolve(delivered.Parts[0].Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) < 5 || string(data[:5]) != "%PDF-" {
			t.Fatalf("delivered form is not a PDF: size=%d err=%v", len(data), err)
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
		assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", privateValue)
		record := assertDocumentFormJournal(
			t,
			home,
			privateValue,
			delivered.Parts[0].Ref,
			document.WriteDelivered,
		)
		if record.OutboxDeliveryID != delivered.DeliveryID {
			t.Fatalf(
				"document outbox correlation = %q, want %q",
				record.OutboxDeliveryID,
				delivered.DeliveryID,
			)
		}
		for _, sessionKey := range fixture.Agent.Sessions.ListSessions() {
			history := fixture.Agent.Sessions.GetHistory(sessionKey)
			for _, message := range history {
				if strings.Contains(message.Content, privateValue) {
					t.Fatalf("durable history retained form value: %#v", message)
				}
			}
		}
	})

	t.Run("protected form questions resume through opaque receipts into redacted review", func(t *testing.T) {
		requireDocumentFormBackend(t)
		workspace := documentE2EWorkspace(t)
		home := filepath.Join(workspace, "instance")
		t.Setenv(config.EnvHome, home)
		store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
		privateValues := []string{
			"09/17/2026",
			"MINTCLAW_PDF3_AGENT_PRIVATE_8f21",
		}
		provider := newDocumentFormReviewE2EProvider(ref, digest, sourcePath, privateValues)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
			cfg.Agents.Defaults.ContextManager = "seahorse"
			cfg.Tools.Document.AuditModel = provider.GetDefaultModel()
			cfg.ModelList = []*config.ModelConfig{{
				ModelName: provider.GetDefaultModel(), Provider: "openai",
				Model: provider.GetDefaultModel(), Enabled: true,
			}}
		})
		fixture.Loop.SetMediaStore(store)
		t.Cleanup(func() { closeDocumentE2EFixtureAfterTraceDrain(t, fixture) })

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-review-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(
			t,
			fixture.Bus,
			ref,
			"Fill this attached form. Ask me only for the missing information.",
		)

		answered := make(map[string]struct{}, len(privateValues))
		lastQuestionID := ""
		for _, privateValue := range privateValues {
			shortID := waitDocumentFormQuestion(t, channel, answered)
			answered[shortID] = struct{}{}
			lastQuestionID = shortID
			waitDocumentFormInteractionWaiting(t, workspace, shortID)
			publishDocumentE2EAnswer(t, fixture.Bus, shortID, privateValue, len(answered))
		}
		waitDocumentE2EChannel(t, channel, func() bool {
			for _, message := range channel.messagesSnapshot() {
				if message.Content == "Form review is ready." {
					return true
				}
			}
			return false
		})
		waitDocumentFormInteractionResolved(t, workspace, lastQuestionID)
		if err := provider.AssertComplete(); err != nil {
			t.Fatal(err)
		}
		assertDocumentFormPromptCounts(t, channel, len(privateValues), 0)
		assertDocumentSourceDigest(t, sourcePath, digest)

		for _, sessionKey := range fixture.Agent.Sessions.ListSessions() {
			for _, message := range fixture.Agent.Sessions.GetHistory(sessionKey) {
				for _, privateValue := range privateValues {
					if strings.Contains(message.Content, privateValue) {
						t.Fatalf("durable form review history retained protected value: %#v", message)
					}
				}
			}
		}
		assertDocumentFormReviewState(t, workspace, home, privateValues...)
	})

	t.Run("protected form commit consumes approval and delivers once", func(t *testing.T) {
		requireDocumentFormBackend(t)
		workspace := documentE2EWorkspace(t)
		home := filepath.Join(workspace, "instance")
		t.Setenv(config.EnvHome, home)
		store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
		privateValues := []string{"09/18/2026", "MINTCLAW_PDFI1_PRIVATE_71c4"}
		provider := newDocumentFormCommitE2EProvider(ref, digest, sourcePath, privateValues)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
			cfg.Tools.Approval.Mode = config.ToolApprovalModeRequired
			cfg.Agents.Defaults.ContextManager = "seahorse"
			cfg.Tools.Document.AuditModel = provider.GetDefaultModel()
			cfg.ModelList = []*config.ModelConfig{{
				ModelName: provider.GetDefaultModel(), Provider: "openai",
				Model: provider.GetDefaultModel(), Enabled: true,
			}}
		})
		fixture.Loop.SetMediaStore(store)
		t.Cleanup(func() { closeDocumentE2EFixtureAfterTraceDrain(t, fixture) })

		channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-commit-e2e"}}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(
			t,
			fixture.Bus,
			ref,
			"Fill this attached form. Ask me only for the missing information.",
		)

		answered := make(map[string]struct{}, len(privateValues))
		lastQuestionID := ""
		for _, privateValue := range privateValues {
			shortID := waitDocumentFormQuestion(t, channel, answered)
			answered[shortID] = struct{}{}
			lastQuestionID = shortID
			waitDocumentFormInteractionWaiting(t, workspace, shortID)
			publishDocumentE2EAnswer(t, fixture.Bus, shortID, privateValue, len(answered))
		}
		waitDocumentFormReviewReady(t, channel)
		waitDocumentFormInteractionResolved(t, workspace, lastQuestionID)
		publishDocumentE2EFollowup(t, fixture.Bus, "Finish and deliver the verified PDF.", 1)
		approvalID := waitDocumentFormApproval(t, channel)
		waitDocumentFormInteractionWaiting(t, workspace, approvalID)
		publishDocumentE2EAnswer(t, fixture.Bus, approvalID, "allow_once", len(answered)+1)
		waitDocumentE2EChannel(t, channel, func() bool {
			for _, message := range channel.messagesSnapshot() {
				if message.Content == "Form commit is verified and delivered." {
					return true
				}
			}
			return false
		})
		waitDocumentFormInteractionResolved(t, workspace, approvalID)
		if err := provider.AssertComplete(); err != nil {
			t.Fatal(err)
		}
		assertDocumentFormPromptCounts(t, channel, len(privateValues), 1)
		assertDocumentSourceDigest(t, sourcePath, digest)
		channel.mu.Lock()
		mediaCount := len(channel.sentMedia)
		var delivered bus.OutboundMediaMessage
		if mediaCount == 1 {
			delivered = channel.sentMedia[0]
		}
		channel.mu.Unlock()
		if mediaCount != 1 || len(delivered.Parts) != 1 || delivered.Recovery == nil ||
			delivered.Recovery.DomainJobID == "" || delivered.Recovery.DomainOwnerDigest == "" ||
			delivered.Parts[0].Filename != "filled-document.pdf" ||
			delivered.Parts[0].ContentType != "application/pdf" {
			t.Fatalf("commit delivery = %#v", delivered)
		}
		assertDocumentFormCommitState(t, workspace, home, privateValues...)
	})

	t.Run("agent-led form explains navigates and delivers exactly once", func(t *testing.T) {
		requireDocumentFormBackend(t)
		workspace := documentE2EWorkspace(t)
		home := filepath.Join(workspace, "instance")
		t.Setenv(config.EnvHome, home)
		store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
		privateValues := []string{"PDFI2-PRIVATE"}
		provider := newDocumentAgentLedFormCommitE2EProvider(ref, digest, sourcePath, privateValues)
		fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
			configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
			cfg.Tools.Approval.Mode = config.ToolApprovalModeRequired
			cfg.Agents.Defaults.ContextManager = "seahorse"
			cfg.Tools.Document.AuditModel = provider.GetDefaultModel()
			cfg.ModelList = []*config.ModelConfig{{
				ModelName: provider.GetDefaultModel(), Provider: "openai",
				Model: provider.GetDefaultModel(), Enabled: true,
			}}
		})
		fixture.Loop.SetMediaStore(store)
		t.Cleanup(func() { closeDocumentE2EFixtureAfterTraceDrain(t, fixture) })

		channel := &fakeMediaChannel{
			fakeChannel: fakeChannel{id: "document-agent-led-form-e2e"}, bindPlatformMessageIDs: true,
		}
		stop := startDocumentE2EChannel(t, fixture, store, channel)
		defer stop()
		publishDocumentE2EInbound(
			t,
			fixture.Bus,
			ref,
			"Fill this attached form. Ask me only for the missing information.",
		)

		seen := make(map[string]struct{})
		firstID := waitDocumentFormQuestion(t, channel, seen)
		seen[firstID] = struct{}{}
		first := documentFormQuestionMessage(t, channel, firstID)
		wantFirstActions := []bus.InboundInteractionChoice{
			bus.InboundInteractionChoiceClarify,
		}
		if !strings.Contains(first.Content, "I inspected the form") ||
			!strings.Contains(first.Content, "show a review") ||
			!slices.Equal(first.Metadata.InteractionActions(), wantFirstActions) {
			t.Fatalf("first agent-led form prompt = %#v", first)
		}
		waitDocumentFormInteractionWaiting(t, workspace, firstID)
		publishDocumentE2ENavigation(
			t,
			fixture.Bus,
			firstID,
			first.Context.MessageID,
			bus.InboundInteractionChoiceClarify,
			1,
		)

		clarifiedID := waitDocumentFormQuestion(t, channel, seen)
		seen[clarifiedID] = struct{}{}
		clarified := documentFormQuestionMessage(t, channel, clarifiedID)
		if !strings.Contains(clarified.Content, "You may reply with free text") ||
			!slices.Equal(clarified.Metadata.InteractionActions(), wantFirstActions) {
			t.Fatalf("clarified form prompt = %#v", clarified)
		}
		waitDocumentFormInteractionWaiting(t, workspace, clarifiedID)
		publishDocumentE2EAnswer(t, fixture.Bus, clarifiedID, privateValues[0], 2)

		secondID := waitDocumentFormQuestion(t, channel, seen)
		seen[secondID] = struct{}{}
		second := documentFormQuestionMessage(t, channel, secondID)
		wantOptionalActions := []bus.InboundInteractionChoice{
			bus.InboundInteractionChoiceClarify,
			bus.InboundInteractionChoiceBack,
			bus.InboundInteractionChoiceSkip,
			bus.InboundInteractionChoiceNotApplicable,
		}
		if !slices.Equal(second.Metadata.InteractionActions(), wantOptionalActions) {
			t.Fatalf("second form prompt actions = %#v", second.Metadata.InteractionActions())
		}
		waitDocumentFormInteractionWaiting(t, workspace, secondID)
		publishDocumentE2ENavigation(
			t,
			fixture.Bus,
			secondID,
			second.Context.MessageID,
			bus.InboundInteractionChoiceBack,
			3,
		)

		correctionID := waitDocumentFormQuestion(t, channel, seen)
		seen[correctionID] = struct{}{}
		correction := documentFormQuestionMessage(t, channel, correctionID)
		if !strings.Contains(correction.Content, "You may reply with free text") ||
			!slices.Equal(correction.Metadata.InteractionActions(), wantFirstActions) {
			t.Fatalf("back correction prompt = %#v", correction)
		}
		waitDocumentFormInteractionWaiting(t, workspace, correctionID)
		publishDocumentE2EAnswer(t, fixture.Bus, correctionID, privateValues[0], 4)

		optionalID := waitDocumentFormQuestion(t, channel, seen)
		seen[optionalID] = struct{}{}
		optional := documentFormQuestionMessage(t, channel, optionalID)
		if !slices.Equal(optional.Metadata.InteractionActions(), wantOptionalActions) {
			t.Fatalf("revisited optional prompt = %#v", optional)
		}
		waitDocumentFormInteractionWaiting(t, workspace, optionalID)
		publishDocumentE2EAnswer(
			t,
			fixture.Bus,
			optionalID,
			interactions.ProtectedAnswerSkipLabel,
			5,
		)

		waitDocumentFormReviewReady(t, channel)
		waitDocumentFormInteractionResolved(t, workspace, optionalID)
		publishDocumentE2EFollowup(t, fixture.Bus, "Finish and deliver the verified PDF.", 1)
		approvalID := waitDocumentFormApproval(t, channel)
		waitDocumentFormInteractionWaiting(t, workspace, approvalID)
		publishDocumentE2EAnswer(t, fixture.Bus, approvalID, "allow_once", 6)
		waitDocumentE2EChannel(t, channel, func() bool {
			for _, message := range channel.messagesSnapshot() {
				if message.Content == "Form commit is verified and delivered." {
					return true
				}
			}
			return false
		})
		waitDocumentFormInteractionResolved(t, workspace, approvalID)
		if err := provider.AssertComplete(); err != nil {
			t.Fatal(err)
		}
		assertDocumentFormPromptCounts(t, channel, 5, 1)
		assertDocumentSourceDigest(t, sourcePath, digest)
		channel.mu.Lock()
		mediaCount := len(channel.sentMedia)
		var delivered bus.OutboundMediaMessage
		if mediaCount == 1 {
			delivered = channel.sentMedia[0]
		}
		channel.mu.Unlock()
		if mediaCount != 1 || len(delivered.Parts) != 1 || delivered.Parts[0].Filename != "filled-document.pdf" {
			t.Fatalf("agent-led form delivery = %#v", delivered)
		}
		assertDocumentFormCommitState(t, workspace, home, privateValues...)
	})

	for _, scenario := range []struct {
		name              string
		state             document.WriteOperationState
		result            func() channels.DeliveryResult[bus.OutboundMediaMessage]
		modelContinuation bool
	}{
		{
			name:  "definite form delivery rejection retries safely without duplicate identity",
			state: document.WriteDeliveryFailed,
			result: func() channels.DeliveryResult[bus.OutboundMediaMessage] {
				return channels.RejectedDelivery[bus.OutboundMediaMessage](errors.New("synthetic preflight rejection"))
			},
			modelContinuation: true,
		},
		{
			name:  "ambiguous form delivery stops the turn without replay",
			state: document.WriteDeliveryAmbiguous,
			result: func() channels.DeliveryResult[bus.OutboundMediaMessage] {
				return channels.FailedDelivery[bus.OutboundMediaMessage](
					nil,
					nil,
					0,
					errors.New("synthetic response loss"),
				)
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			requireDocumentFormBackend(t)
			workspace := documentE2EWorkspace(t)
			home := filepath.Join(workspace, "instance")
			t.Setenv(config.EnvHome, home)
			store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
			fieldID := documentE2EFieldID(t, sourcePath, "full_name")
			privateValue := "MintClaw Failed Delivery Private Value"
			provider := documentFormDeliveryE2EProvider(
				ref,
				digest,
				sourcePath,
				fieldID,
				privateValue,
				scenario.modelContinuation,
			)
			fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
				configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
			})
			fixture.Loop.SetMediaStore(store)

			var attempts atomic.Int32
			var deliveryIdentity atomic.Value
			channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-form-delivery-e2e"}}
			channel.mediaDelivery = func(
				_ context.Context,
				pending []bus.OutboundMediaMessage,
			) channels.DeliveryResult[bus.OutboundMediaMessage] {
				if len(pending) != 1 || len(pending[0].Parts) != 1 {
					return channels.RejectedDelivery[bus.OutboundMediaMessage](
						fmt.Errorf("unexpected form payload count: %d", len(pending)),
					)
				}
				if current := deliveryIdentity.Load(); current == nil {
					deliveryIdentity.Store(pending[0].DeliveryID)
				} else if current.(string) != pending[0].DeliveryID {
					return channels.RejectedDelivery[bus.OutboundMediaMessage](
						fmt.Errorf("delivery identity changed from %s to %s", current, pending[0].DeliveryID),
					)
				}
				attempts.Add(1)
				return scenario.result()
			}
			stop := startDocumentE2EChannel(t, fixture, store, channel)
			defer stop()
			publishDocumentE2EInbound(
				t,
				fixture.Bus,
				ref,
				"Fill full_name with the protected value supplied for this call and send me the verified PDF.",
			)

			record := waitDocumentFormJournalState(t, home, scenario.state, channel)
			assertDocumentE2ETrace(t, workspace, digest, sourcePath, "same-name.pdf", privateValue)
			wantAttempts := int32(1)
			if scenario.state == document.WriteDeliveryFailed {
				wantAttempts = 4
			}
			if attempts.Load() != wantAttempts {
				t.Fatalf("form delivery attempts = %d, want %d", attempts.Load(), wantAttempts)
			}
			if deliveryIdentity.Load() == nil {
				t.Fatal("form delivery identity was never observed")
			}
			if record.OutboxDeliveryID != deliveryIdentity.Load().(string) {
				t.Fatalf(
					"document outbox correlation = %q, want %q",
					record.OutboxDeliveryID,
					deliveryIdentity.Load().(string),
				)
			}
			channel.mu.Lock()
			mediaCount := len(channel.sentMedia)
			textCount := len(channel.sentMessages)
			channel.mu.Unlock()
			if mediaCount != 0 || (scenario.modelContinuation && textCount != 1) ||
				(!scenario.modelContinuation && textCount != 0) {
				t.Fatalf("failed form deliveries = media %d text %d", mediaCount, textCount)
			}
			if record.ArtifactRef == "" || record.DeliveryID == "" {
				t.Fatalf("terminal delivery lost durable correlations: %#v", record)
			}
			if err := provider.AssertExhausted(); err != nil {
				t.Fatal(err)
			}
			assertDocumentFormJournal(t, home, privateValue, record.ArtifactRef, scenario.state)
		})
	}
}

func TestCodingDocumentReadOnlyVerticalSlice(t *testing.T) {
	requireDocumentReadBackend(t)

	t.Run("attached PDF inspect and extract", func(t *testing.T) {
		workspace, layout, store, ref, digest := codingDocumentE2ESource(t, "text.pdf")
		provider := llmscenario.NewScriptedProvider(
			"coding-document-text-e2e-model",
			llmscenario.ProviderStep{
				Name:   "inspect attached document",
				Assert: codingDocumentFirstCallAssertion(ref),
				Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					"inspect-coding-document",
					"document",
					map[string]any{"action": "inspect", "source": ref},
				)),
			},
			llmscenario.ProviderStep{
				Name: "extract attached document",
				Assert: func(call llmscenario.ProviderCall) error {
					if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
						return err
					}
					return llmscenario.RequireLastMessage("tool", `"page_count":1`)(call)
				},
				Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					"extract-coding-document",
					"document",
					map[string]any{
						"action": "extract", "source": ref, "pages": []any{float64(1)},
					},
				)),
			},
			llmscenario.ProviderStep{
				Name: "answer from extracted text",
				Assert: llmscenario.RequireLastMessage(
					"tool",
					"MintClaw text fixture",
				),
				Response: llmscenario.TextResponse("The marker is MintClaw text fixture [page 1]."),
			},
		)
		loop := newCodingDocumentE2ELoop(t, workspace, layout, store, provider, false)
		response, err := loop.ProcessDirectInputWithOptions(
			t.Context(),
			DirectTurnInput{Content: "Inspect and extract page 1.", Media: []string{ref}},
			layout.SessionKey(),
			"coding",
			layout.ThreadID(),
			DirectTurnOptions{},
		)
		if err != nil || response != "The marker is MintClaw text fixture [page 1]." {
			t.Fatalf("coding document response = %q, %v", response, err)
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("attached PDF render stays turn scoped", func(t *testing.T) {
		workspace, layout, store, ref, _ := codingDocumentE2ESource(t, "rotated-crop.pdf")
		artifactRef := ""
		provider := llmscenario.NewScriptedProvider(
			"coding-document-render-e2e-model",
			llmscenario.ProviderStep{
				Name:   "render attached document",
				Assert: codingDocumentFirstCallAssertion(ref),
				Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					"render-coding-document",
					"document",
					map[string]any{"action": "render", "source": ref, "pages": []any{float64(1)}},
				)),
			},
			llmscenario.ProviderStep{
				Name: "observe turn-scoped render",
				Assert: func(call llmscenario.ProviderCall) error {
					for _, message := range call.Messages {
						if message.Role != "tool" || !strings.Contains(message.Content, `"operation":"render"`) {
							continue
						}
						jsonStart := strings.IndexByte(message.Content, '{')
						var report struct {
							Artifacts []json.RawMessage `json:"artifacts"`
						}
						if jsonStart < 0 || !strings.HasPrefix(message.Content, "[image:") {
							return fmt.Errorf("coding render projection = %#v", message)
						}
						if err := json.Unmarshal([]byte(message.Content[jsonStart:]), &report); err != nil {
							return fmt.Errorf("coding render result = %#v: %w", message, err)
						}
						if len(report.Artifacts) != 1 {
							return fmt.Errorf("coding render artifact count = %d", len(report.Artifacts))
						}
						artifactRef = latestCodingDocumentArtifactRef(store)
						if artifactRef == "" {
							return errors.New("coding render artifact ref was not registered")
						}
						return nil
					}
					return errors.New("coding render result is missing")
				},
				Response: llmscenario.TextResponse("Rendered source page 1."),
			},
		)
		loop := newCodingDocumentE2ELoop(t, workspace, layout, store, provider, true)
		response, err := loop.ProcessDirectInputWithOptions(
			t.Context(),
			DirectTurnInput{Content: "Render page 1 for inspection.", Media: []string{ref}},
			layout.SessionKey(),
			"coding",
			layout.ThreadID(),
			DirectTurnOptions{},
		)
		if err != nil || response != "Rendered source page 1." || artifactRef == "" {
			t.Fatalf("coding render response = %q, artifact=%q, error=%v", response, artifactRef, err)
		}
		if _, err = store.Resolve(artifactRef); err == nil {
			t.Fatal("turn-scoped coding render survived terminal cleanup")
		}
		if err = provider.AssertExhausted(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("local PDF inspect cannot escape workspace", func(t *testing.T) {
		workspace := t.TempDir()
		layout, err := NewCodingRuntimeLayout(
			"thread-local-document",
			workspace,
			filepath.Join(t.TempDir(), "state"),
			[]string{workspace},
		)
		if err != nil {
			t.Fatal(err)
		}
		store := newCodingDocumentTestMediaStore()
		t.Cleanup(store.owned.Stop)
		inside := filepath.Join(workspace, "inside.pdf")
		fixture := documentFixturePath(t, "text.pdf")
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(inside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		provider := llmscenario.NewScriptedProvider(
			"coding-document-local-e2e-model",
			llmscenario.ProviderStep{
				Name:   "inspect local document",
				Assert: llmscenario.RequireToolDefinition("document"),
				Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					"inspect-local-coding-document",
					"document",
					map[string]any{"action": "inspect", "path": inside},
				)),
			},
			llmscenario.ProviderStep{
				Name:     "confirm local inspection",
				Assert:   llmscenario.RequireLastMessage("tool", `"operation":"inspect"`),
				Response: llmscenario.TextResponse("Inspected local PDF."),
			},
		)
		loop := newCodingDocumentE2ELoop(t, workspace, layout, store, provider, false)
		response, err := loop.ProcessDirect(
			t.Context(),
			`Inspect "`+inside+`".`,
			layout.SessionKey(),
		)
		if err != nil || response != "Inspected local PDF." {
			t.Fatalf("local coding document response = %q, %v", response, err)
		}
		outside := filepath.Join(t.TempDir(), "outside.pdf")
		if err = os.WriteFile(outside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		documentTool, ok := loop.GetRegistry().GetDefaultAgent().Tools.Get("document")
		if !ok {
			t.Fatal("coding document tool is unavailable")
		}
		ctx := toolshared.WithToolExecutionIdentity(t.Context(), workspace, "outside-path-test")
		ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{outside})
		result := documentTool.Execute(ctx, map[string]any{"action": "inspect", "path": outside})
		if !result.IsError || !strings.Contains(result.ForLLM, string(document.FailureSourceUnauthorized)) {
			t.Fatalf("outside coding document result = %#v", result)
		}
	})
}

type documentFormReviewE2EProvider struct {
	mu sync.Mutex

	model                  string
	ref                    string
	sourceDigest           string
	sourcePath             string
	privateValues          []string
	initialCalls           int
	receipts               map[string]struct{}
	navigationReceipts     map[string]struct{}
	auditCalls             int
	reviewCalls            int
	finalCalls             int
	commit                 bool
	commitCalls            int
	expectedReceipts       int
	agentLed               bool
	firstFieldID           string
	optionalFieldID        string
	optionalSkipID         string
	nativeClarify          bool
	nativeBack             bool
	optionalAsked          bool
	omitFirstQuestion      bool
	omittedFirstQuestion   bool
	recoveredFirstQuestion bool
	rejectFirstReceipt     bool
	rejectedFirstReceipt   bool
	rejectPreparedFollowup bool
	rejectedPrepared       bool
	rejectFirstFollowup    bool
	rejectedFirstFollowup  bool
	err                    error
}

func newDocumentFormReviewE2EProvider(
	ref string,
	sourceDigest string,
	sourcePath string,
	privateValues []string,
) *documentFormReviewE2EProvider {
	return &documentFormReviewE2EProvider{
		model: "document-form-review-e2e-model", ref: ref, sourceDigest: sourceDigest, sourcePath: sourcePath,
		privateValues: append([]string(nil), privateValues...), receipts: make(map[string]struct{}),
		navigationReceipts: make(map[string]struct{}),
		expectedReceipts:   len(privateValues),
	}
}

func newDocumentFormCommitE2EProvider(
	ref string,
	sourceDigest string,
	sourcePath string,
	privateValues []string,
) *documentFormReviewE2EProvider {
	provider := newDocumentFormReviewE2EProvider(ref, sourceDigest, sourcePath, privateValues)
	provider.model = "document-form-commit-e2e-model"
	provider.commit = true
	return provider
}

func newDocumentAgentLedFormCommitE2EProvider(
	ref string,
	sourceDigest string,
	sourcePath string,
	privateValues []string,
) *documentFormReviewE2EProvider {
	provider := newDocumentFormCommitE2EProvider(ref, sourceDigest, sourcePath, privateValues)
	provider.model = "document-agent-led-form-commit-e2e-model"
	provider.agentLed = true
	provider.rejectFirstReceipt = true
	provider.rejectPreparedFollowup = true
	provider.rejectFirstFollowup = true
	provider.expectedReceipts = 3
	return provider
}

func (*documentFormReviewE2EProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{CallerMediatedTools: true}
}

func (provider *documentFormReviewE2EProvider) GetDefaultModel() string { return provider.model }

func (provider *documentFormReviewE2EProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.err != nil {
		return nil, provider.err
	}
	joined := documentProviderMessagesText(messages)
	if len(toolDefs) == 0 {
		if strings.Contains(joined, "<runtime_response_only_followup>") &&
			strings.Contains(joined, `"state":"review_ready"`) && strings.Contains(joined, `"ready":true`) {
			provider.reviewCalls++
			if !strings.Contains(joined, "No tools are available in this iteration") ||
				!strings.Contains(joined, "Ask the user to confirm") {
				return nil, errors.New("ready review did not install a response-only human boundary")
			}
			if !provider.commit {
				provider.finalCalls++
			}
			return llmscenario.TextResponse("Form review is ready."), nil
		}
		provider.auditCalls++
		for _, value := range provider.privateValues {
			if !strings.Contains(joined, value) {
				return nil, fmt.Errorf("protected audit omitted value %q", value)
			}
		}
		if strings.Contains(joined, provider.sourcePath) || strings.Contains(joined, provider.ref) {
			return nil, errors.New("protected audit received document authority")
		}
		return llmscenario.TextResponse(`{"decision":"pass"}`), nil
	}
	for _, value := range provider.privateValues {
		if strings.Contains(joined, value) {
			return nil, fmt.Errorf("ordinary model context retained protected value %q", value)
		}
	}
	toolOnlyFollowup := documentLatestToolOnlyFollowupInstruction(messages)
	if provider.initialCalls == 0 {
		provider.initialCalls++
		if err := documentFirstCallAssertion(provider.ref, provider.sourcePath)(llmscenario.ProviderCall{
			Messages: messages, Tools: toolDefs,
		}); err != nil {
			return nil, err
		}
		return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			"search-document-form-review", tools.BM25SearchToolName,
			map[string]any{"query": "start protected PDF form collection and review"},
		)), nil
	}
	if provider.initialCalls == 1 {
		provider.initialCalls++
		if err := llmscenario.RequireToolDefinition("document")(llmscenario.ProviderCall{
			Messages: messages, Tools: toolDefs,
		}); err != nil {
			return nil, err
		}
		return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			"inspect-document-form-source", "document",
			map[string]any{"action": "inspect", "source": provider.ref},
		)), nil
	}
	if provider.initialCalls == 2 {
		provider.initialCalls++
		if err := llmscenario.RequireLastMessage("tool", provider.sourceDigest)(llmscenario.ProviderCall{
			Messages: messages, Tools: toolDefs,
		}); err != nil {
			return nil, err
		}
		if !strings.Contains(joined, `"operation":"inspect"`) ||
			!strings.Contains(joined, `"state":"succeeded"`) {
			return nil, errors.New("protected form workflow did not inspect the exact source before start")
		}
		return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			"discover-document-form-source", "document",
			map[string]any{"action": "form", "form_action": "discover", "source": provider.ref},
		)), nil
	}
	if provider.initialCalls == 3 {
		provider.initialCalls++
		if !strings.Contains(joined, `"operation":"form"`) ||
			!strings.Contains(joined, `"form_action":"discover"`) {
			return nil, errors.New("protected form workflow did not run bounded discovery before start")
		}
		if len(toolDefs) != 1 || toolDefs[0].Function.Name != "document" ||
			!strings.Contains(toolOnlyFollowup, "form_action=start") ||
			!strings.Contains(toolOnlyFollowup, "Do not answer in prose") {
			return nil, errors.New("protected form discovery did not require an exact tool-only start")
		}
		if provider.agentLed {
			provider.firstFieldID, provider.optionalFieldID, provider.optionalSkipID = documentAgentLedFieldIDsFromMessages(
				messages,
			)
			if provider.firstFieldID == "" || provider.optionalFieldID == "" || provider.optionalSkipID == "" {
				return nil, errors.New("agent-led form workflow did not find its bounded semantic field plan")
			}
		}
		fieldSchemaDigest := documentFieldSchemaDigestFromMessages(messages)
		if fieldSchemaDigest == "" {
			return nil, errors.New("protected form workflow did not retain the discovered field schema digest")
		}
		return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			"start-document-form-review", "document",
			map[string]any{
				"action": "form", "form_action": "start", "source": provider.ref,
				"field_schema_digest": fieldSchemaDigest,
			},
		)), nil
	}
	if provider.commit && documentLatestToolMessageContains(
		messages,
		`"form_action":"commit"`,
		`"operation_id":"document_write_`,
		`"artifact_ref":"media://`,
		"Structured deliverable:",
	) {
		provider.finalCalls++
		return llmscenario.TextResponse("Form commit is verified and delivered."), nil
	}
	if strings.Contains(joined, `"state":"review_ready"`) && strings.Contains(joined, `"ready":true`) {
		if provider.commit {
			provider.commitCalls++
			jobID := documentFormJobIDFromMessages(messages)
			if jobID == "" {
				return nil, errors.New("review-ready form job ID is unavailable")
			}
			return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"commit-document-form-review",
				"document",
				map[string]any{"action": "form", "form_action": "commit", "job_id": jobID},
			)), nil
		}
		return nil, errors.New("ready review bypassed its response-only human boundary")
	}
	if provider.rejectPreparedFollowup && !provider.rejectedPrepared &&
		strings.Contains(toolOnlyFollowup, "preceding trusted tool result") {
		provider.rejectedPrepared = true
		if len(toolDefs) != 1 || toolDefs[0].Function.Name != "document" {
			return nil, errors.New("prepared form follow-up did not remain restricted to document")
		}
		return llmscenario.TextResponse("Please provide all form values in plain text."), nil
	}
	if provider.rejectFirstFollowup && !provider.rejectedFirstFollowup &&
		strings.Contains(toolOnlyFollowup, "protected answer receipt") {
		provider.rejectedFirstFollowup = true
		if len(toolDefs) != 1 || toolDefs[0].Function.Name != "document" {
			return nil, errors.New("protected answer follow-up did not remain restricted to document")
		}
		return llmscenario.TextResponse("Please provide the protected value again in plain text."), nil
	}
	if reference := protectedReferenceFromMessages(messages); reference != "" {
		if provider.rejectFirstReceipt && !provider.rejectedFirstReceipt {
			provider.rejectedFirstReceipt = true
			if len(toolDefs) != 1 || toolDefs[0].Function.Name != "document" ||
				!strings.Contains(joined, "runtime_protected_answer_continuation") {
				return nil, errors.New("protected answer continuation did not restrict the retry to document")
			}
			return llmscenario.TextResponse("Please provide the protected value again in plain text."), nil
		}
		if navigation, navigationErr := document.ParseFormProtectedNavigationReference(
			reference,
		); navigationErr == nil {
			if _, duplicate := provider.navigationReceipts[reference]; duplicate {
				return nil, fmt.Errorf("protected navigation receipt %q was replayed", reference)
			}
			provider.navigationReceipts[reference] = struct{}{}
			switch navigation.Action {
			case interactions.ProtectedAnswerClarify:
				provider.nativeClarify = true
			case interactions.ProtectedAnswerBack:
				provider.nativeBack = true
			default:
				return nil, fmt.Errorf("unexpected protected navigation action %q", navigation.Action)
			}
			return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				fmt.Sprintf("navigate-document-form-%d", len(provider.navigationReceipts)),
				"document",
				map[string]any{
					"action": "form", "form_action": string(navigation.Action), "navigation_ref": reference,
				},
			)), nil
		}
		if _, duplicate := provider.receipts[reference]; duplicate {
			return nil, fmt.Errorf("protected receipt %q was replayed", reference)
		}
		provider.receipts[reference] = struct{}{}
		return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
			fmt.Sprintf("continue-document-form-review-%d", len(provider.receipts)),
			"document",
			map[string]any{"action": "form", "form_action": "continue", "answer_ref": reference},
		)), nil
	}
	if jobID, fieldID, ready := documentFormProgressFromMessages(messages); jobID != "" {
		if ready {
			if provider.agentLed && len(provider.receipts) >= 1 && len(provider.receipts) <= 2 {
				return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					fmt.Sprintf("collect-agent-led-optional-value-%d", len(provider.receipts)),
					"document",
					map[string]any{
						"action": "form", "form_action": "collect", "job_id": jobID,
						"field_id": provider.optionalFieldID,
						"question": "Would you like to provide this optional information?",
					},
				)), nil
			}
			if provider.agentLed && len(provider.receipts) == 3 && !provider.optionalAsked {
				provider.optionalAsked = true
				return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
					"collect-agent-led-optional-skip",
					"document",
					map[string]any{
						"action": "form", "form_action": "collect", "job_id": jobID,
						"field_id": provider.optionalSkipID,
						"question": "Would you like to provide this other optional information?",
					},
				)), nil
			}
			return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"review-document-form", "document",
				map[string]any{"action": "form", "form_action": "review", "job_id": jobID},
			)), nil
		}
		if fieldID != "" {
			selectedFieldID := fieldID
			if provider.agentLed {
				switch len(provider.receipts) {
				case 0:
					selectedFieldID = provider.firstFieldID
					if selectedFieldID == fieldID {
						return nil, errors.New("agent-led form plan followed raw schema order")
					}
				case 1, 2:
					selectedFieldID = provider.optionalFieldID
				}
			}
			question := "Please provide the next missing value for this PDF form."
			arguments := map[string]any{
				"action": "form", "form_action": "collect", "job_id": jobID,
				"field_id": selectedFieldID,
			}
			if !provider.agentLed || len(provider.receipts) > 0 {
				arguments["blank_actions"] = []string{"skip", "not_applicable"}
			}
			if provider.agentLed && len(provider.receipts) == 0 {
				question = "First, what should I enter in the free-text field?"
			}
			if len(provider.receipts) == 0 {
				arguments["form_summary"] = "I inspected the form and found a small set of missing facts."
				arguments["collection_plan"] = "I'll collect only those facts, then show a review before writing anything."
			}
			if provider.omitFirstQuestion && len(provider.receipts) == 0 && !provider.omittedFirstQuestion {
				provider.omittedFirstQuestion = true
			} else {
				if provider.omittedFirstQuestion && !provider.recoveredFirstQuestion {
					toolRecovery := strings.Contains(joined, `"code":"invalid_input"`) &&
						strings.Contains(joined, "retry the same field without asking in plain text")
					runtimeRecovery := strings.Contains(joined, "runtime_tool_only_followup") &&
						strings.Contains(joined, "previous response did not make an allowed tool-only follow-up")
					if !toolRecovery && !runtimeRecovery {
						return nil, errors.New("agent did not receive a safe missing-question recovery contract")
					}
					provider.recoveredFirstQuestion = true
				}
				arguments["question"] = question
			}
			return llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				fmt.Sprintf("collect-document-form-value-%d", len(provider.receipts)+1),
				"document",
				arguments,
			)), nil
		}
	}
	return nil, fmt.Errorf(
		"document form review scenario received unexpected model context: %s",
		documentProviderMessageSummary(messages),
	)
}

func documentProviderMessageSummary(messages []providers.Message) string {
	start := max(0, len(messages)-6)
	var builder strings.Builder
	for index := start; index < len(messages); index++ {
		message := messages[index]
		fmt.Fprintf(
			&builder,
			"[%d role=%s tool_call_id=%s] %s\n",
			index,
			message.Role,
			message.ToolCallID,
			truncateDocumentE2EText(message.Content, 1200),
		)
	}
	return builder.String()
}

func documentLatestToolMessageContains(messages []providers.Message, required ...string) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		for _, fragment := range required {
			if !strings.Contains(message.Content, fragment) {
				return false
			}
		}
		return true
	}
	return false
}

func truncateDocumentE2EText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit] + "...[truncated]"
	}
	return value
}

func (provider *documentFormReviewE2EProvider) AssertComplete() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.err != nil {
		return provider.err
	}
	wantCommitCalls := 0
	if provider.commit {
		wantCommitCalls = 1
	}
	maxAuditCalls := 1
	if provider.omitFirstQuestion {
		// The malformed first call may also trigger one pressure-dependent
		// no-tools maintenance call. Every such call is validated above, but
		// only the ordinary protected form audit is a required workflow step.
		maxAuditCalls = 2
	}
	if provider.initialCalls != 4 || len(provider.receipts) != provider.expectedReceipts ||
		provider.auditCalls < 1 || provider.auditCalls > maxAuditCalls || provider.finalCalls != 1 ||
		provider.reviewCalls != 1 || provider.commitCalls != wantCommitCalls {
		return fmt.Errorf(
			"document form review calls = initial:%d receipts:%d audit:%d review:%d commit:%d final:%d",
			provider.initialCalls,
			len(provider.receipts),
			provider.auditCalls,
			provider.reviewCalls,
			provider.commitCalls,
			provider.finalCalls,
		)
	}
	if provider.agentLed && (len(provider.navigationReceipts) != 2 ||
		!provider.nativeClarify || !provider.nativeBack) {
		return fmt.Errorf(
			"native navigation = receipts:%d clarify:%t back:%t",
			len(provider.navigationReceipts),
			provider.nativeClarify,
			provider.nativeBack,
		)
	}
	if provider.omitFirstQuestion && (!provider.omittedFirstQuestion || !provider.recoveredFirstQuestion) {
		return fmt.Errorf(
			"missing-question recovery = omitted:%t recovered:%t",
			provider.omittedFirstQuestion,
			provider.recoveredFirstQuestion,
		)
	}
	if provider.rejectFirstReceipt && !provider.rejectedFirstReceipt {
		return errors.New("protected receipt plain-text regression was not exercised")
	}
	if provider.rejectPreparedFollowup && !provider.rejectedPrepared {
		return errors.New("prepared form plain-text regression was not exercised")
	}
	if provider.rejectFirstFollowup && !provider.rejectedFirstFollowup {
		return errors.New("post-consumption plain-text regression was not exercised")
	}
	return nil
}

func documentAgentLedFieldIDsFromMessages(messages []providers.Message) (
	requiredID string,
	optionalValueID string,
	optionalSkipID string,
) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || !strings.Contains(message.Content, `"form_action":"discover"`) {
			continue
		}
		start := strings.IndexByte(message.Content, '{')
		end := strings.LastIndexByte(message.Content, '}')
		if start < 0 || end <= start {
			continue
		}
		var payload struct {
			Mapping *struct {
				CandidateFields []struct {
					FieldID  string                 `json:"field_id"`
					Kind     document.FormFieldKind `json:"kind"`
					Required bool                   `json:"required"`
				} `json:"candidate_fields"`
			} `json:"mapping"`
		}
		if json.Unmarshal([]byte(message.Content[start:end+1]), &payload) != nil || payload.Mapping == nil {
			continue
		}
		for _, field := range payload.Mapping.CandidateFields {
			fieldID := strings.TrimSpace(field.FieldID)
			if fieldID == "" {
				continue
			}
			if requiredID == "" && field.Kind == document.FormFieldText && !field.Required {
				requiredID = fieldID
			}
			if optionalValueID == "" && !field.Required && field.Kind == document.FormFieldDate {
				optionalValueID = fieldID
				continue
			}
			if optionalSkipID == "" && !field.Required && fieldID != requiredID &&
				fieldID != optionalValueID {
				optionalSkipID = fieldID
			}
		}
		return requiredID, optionalValueID, optionalSkipID
	}
	return "", "", ""
}

func documentProviderMessagesText(messages []providers.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func documentLatestToolOnlyFollowupInstruction(messages []providers.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.Contains(messages[index].Content, "<runtime_tool_only_followup>") {
			return messages[index].Content
		}
	}
	return ""
}

func protectedReferenceFromMessages(messages []providers.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		if !strings.Contains(message.Content, `"protected_answer_ref"`) {
			return ""
		}
		start := strings.IndexByte(message.Content, '{')
		end := strings.LastIndexByte(message.Content, '}')
		if start < 0 || end <= start {
			return ""
		}
		var payload interactionToolResultPayload
		if json.Unmarshal([]byte(message.Content[start:end+1]), &payload) == nil {
			return payload.ProtectedAnswerRef
		}
		return ""
	}
	return ""
}

func documentFormProgressFromMessages(messages []providers.Message) (jobID, fieldID string, ready bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || !strings.Contains(message.Content, `"operation":"form"`) ||
			!strings.Contains(message.Content, `"mapping"`) {
			continue
		}
		start := strings.IndexByte(message.Content, '{')
		end := strings.LastIndexByte(message.Content, '}')
		if start < 0 || end <= start {
			continue
		}
		var payload struct {
			Job *struct {
				JobID string `json:"job_id"`
			} `json:"job"`
			Mapping *struct {
				CandidateFields []struct {
					FieldID string `json:"field_id"`
				} `json:"candidate_fields"`
				ReadyForReview bool `json:"ready_for_review"`
			} `json:"mapping"`
		}
		if json.Unmarshal([]byte(message.Content[start:end+1]), &payload) != nil ||
			payload.Job == nil || payload.Mapping == nil {
			continue
		}
		fieldID := ""
		if len(payload.Mapping.CandidateFields) > 0 {
			fieldID = strings.TrimSpace(payload.Mapping.CandidateFields[0].FieldID)
		}
		return strings.TrimSpace(payload.Job.JobID), fieldID, payload.Mapping.ReadyForReview
	}
	return "", "", false
}

func documentFieldSchemaDigestFromMessages(messages []providers.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || !strings.Contains(message.Content, `"form_action":"discover"`) {
			continue
		}
		start := strings.IndexByte(message.Content, '{')
		end := strings.LastIndexByte(message.Content, '}')
		if start < 0 || end <= start {
			continue
		}
		var payload struct {
			FieldSchemaDigest string `json:"field_schema_digest"`
		}
		if json.Unmarshal([]byte(message.Content[start:end+1]), &payload) == nil {
			return strings.TrimSpace(payload.FieldSchemaDigest)
		}
	}
	return ""
}

func documentFormJobIDFromMessages(messages []providers.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || !strings.Contains(message.Content, `"job_id"`) {
			continue
		}
		start := strings.IndexByte(message.Content, '{')
		end := strings.LastIndexByte(message.Content, '}')
		if start < 0 || end <= start {
			continue
		}
		var payload struct {
			Job *struct {
				JobID string `json:"job_id"`
			} `json:"job"`
		}
		if json.Unmarshal([]byte(message.Content[start:end+1]), &payload) == nil && payload.Job != nil {
			if jobID := strings.TrimSpace(payload.Job.JobID); jobID != "" {
				return jobID
			}
		}
	}
	return ""
}

func TestDocumentRenderToolLinuxIntegration(t *testing.T) {
	requireDocumentReadBackend(t)
	workspace := t.TempDir()
	store, ref, _, _ := documentE2ESource(t, "rotated-crop.pdf")
	owner, err := media.NewMediaOwner(
		workspace,
		"main",
		"pdf-operator",
		"document-render-route",
		"document-render-session",
		"telegram",
		"pdf-chat",
		"pdf-topic",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewDocumentTool()
	tool.SetMediaStore(store)
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-render-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-render-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-render-execution")
	ctx = toolshared.WithToolDocumentContext(ctx, []string{ref}, true)
	result := tool.Execute(ctx, map[string]any{
		"action": "render", "source": ref, "pages": []any{float64(1)},
	})
	if result.IsError {
		t.Fatalf("render failed: safe=%s internal=%v", result.ForLLM, result.Err)
	}
	if len(result.ContextMedia) != 1 {
		t.Fatalf("rendered refs = %#v", result.ContextMedia)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentLocalPathToolLinuxIntegration(t *testing.T) {
	requireDocumentReadBackend(t)
	workspace := t.TempDir()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "document", "testdata", "text.pdf")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "server-local.pdf")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	store := media.NewFileMediaStore()
	tool := tools.NewDocumentTool(tools.WithDocumentLocalPathPolicy(workspace, true, nil))
	tool.SetMediaStore(store)
	ctx := documentLocalPathToolContext(t, workspace, path)
	inspected := tool.Execute(ctx, map[string]any{"action": "inspect", "path": path})
	if inspected.IsError || strings.Contains(inspected.ForLLM, path) {
		t.Fatalf("inspect failed or leaked path: safe=%s internal=%v", inspected.ForLLM, inspected.Err)
	}
	var projection struct {
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err = json.Unmarshal([]byte(inspected.ForLLM), &projection); err != nil ||
		!strings.HasPrefix(projection.Source.Ref, "media://") {
		t.Fatalf("inspect projection = %#v, %v", projection, err)
	}
	if err = os.WriteFile(path, []byte("%PDF-1.7\nREPLACEMENT_BYTES\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracted := tool.Execute(ctx, map[string]any{
		"action": "extract", "source": projection.Source.Ref, "pages": []any{float64(1)},
	})
	if extracted.IsError || !strings.Contains(extracted.ContextText, "MintClaw text fixture") ||
		strings.Contains(extracted.ContextText, "REPLACEMENT_BYTES") || strings.Contains(extracted.ForLLM, path) {
		t.Fatalf("extract did not use immutable admission: %#v", extracted)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ResolveWithMeta(projection.Source.Ref); err == nil {
		t.Fatal("local snapshot survived turn cleanup")
	}
}

func TestDocumentFormToolLinuxIntegration(t *testing.T) {
	requireDocumentFormBackend(t)
	workspace := t.TempDir()
	stateRoot := filepath.Join(workspace, "state", "document-writes")
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	sourcePath = filepath.Join(filepath.Dir(sourcePath), "..", "document", "testdata", "acroform-fields.pdf")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(workspace, "local-form.pdf")
	if err = os.WriteFile(localPath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	owner, err := media.NewMediaOwner(
		workspace,
		"main",
		"pdf-operator",
		"document-form-route",
		"document-form-session",
		"telegram",
		"pdf-chat",
		"pdf-topic",
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := tools.NewDocumentTool(
		tools.WithDocumentStateRoot(stateRoot),
		tools.WithDocumentLocalPathPolicy(workspace, true, nil),
	)
	tool.SetMediaStore(store)
	ctx := documentFormToolContext(t, workspace)
	ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{localPath})
	inspected := tool.Execute(ctx, map[string]any{"action": "inspect", "path": localPath})
	if inspected.IsError {
		t.Fatalf("local inspect failed: safe=%s internal=%v", inspected.ForLLM, inspected.Err)
	}
	var inspectedReport struct {
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err = json.Unmarshal([]byte(inspected.ForLLM), &inspectedReport); err != nil ||
		inspectedReport.Source.Ref == "" {
		t.Fatalf("local inspect report = %#v err=%v", inspectedReport, err)
	}
	ref := inspectedReport.Source.Ref
	fields := tool.Execute(ctx, map[string]any{"action": "fields", "source": ref})
	if fields.IsError {
		t.Fatalf("fields failed: safe=%s internal=%v", fields.ForLLM, fields.Err)
	}
	fieldID := documentFieldIDFromSafeReport(t, fields.ForLLM, "full_name")
	privateValue := "MintClaw Tool Private Value"
	fillArgs := map[string]any{
		"action": "fill",
		"source": ref,
		"assignments": []any{map[string]any{
			"field_id": fieldID,
			"value":    map[string]any{"type": "text", "text": privateValue},
		}},
	}
	filled := tool.Execute(ctx, fillArgs)
	if filled.IsError || len(filled.Media) != 1 || filled.Deliverable == nil ||
		strings.Contains(filled.ForLLM, privateValue) || filled.Delivery.Commit == nil ||
		filled.Delivery.Settle == nil {
		t.Fatalf("fill result = %#v", filled)
	}
	operationID, deliveryID := documentWriteIDsFromSafeReport(t, filled.ForLLM)
	outboxDeliveryID := "out_" + strings.Repeat("a", 32)
	if err = filled.Delivery.Commit(toolshared.WithToolOutboundDeliveryID(ctx, outboxDeliveryID)); err != nil {
		t.Fatal(err)
	}
	if err = filled.Delivery.Settle(ctx, toolshared.DeliverySettlement{
		Status: toolshared.DeliverySettlementDelivered, DeliveryID: outboxDeliveryID,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filled.ForLLM, `"state":"delivered"`) {
		t.Fatalf("settled fill result did not expose confirmed delivery: %s", filled.ForLLM)
	}
	assertDocumentWriteState(t, stateRoot, operationID, owner, document.WriteDelivered)

	verifyCtx := documentFormToolContext(t, workspace, ref)
	verifyCtx = toolshared.WithToolExecutionIdentity(
		verifyCtx,
		workspace,
		"document-form-verify-execution",
	)
	verified := tool.Execute(verifyCtx, map[string]any{
		"action": "verify", "source": filled.Media[0], "operation_id": operationID,
	})
	if verified.IsError || !strings.Contains(verified.ForLLM, `"operation":"verify"`) ||
		!strings.Contains(verified.ForLLM, operationID) || strings.Contains(verified.ForLLM, sourcePath) ||
		strings.Contains(verified.ForLLM, privateValue) {
		t.Fatalf("verify result = %#v", verified)
	}
	wrongOperation := tool.Execute(verifyCtx, map[string]any{
		"action": "verify", "source": filled.Media[0], "operation_id": document.NewWriteOperationID(),
	})
	if !wrongOperation.IsError ||
		!strings.Contains(wrongOperation.ForLLM, string(document.FailureSourceUnauthorized)) {
		t.Fatalf("wrong-operation verify result = %#v", wrongOperation)
	}

	retryArgs := map[string]any{
		"action":       "fill",
		"source":       ref,
		"assignments":  fillArgs["assignments"],
		"operation_id": operationID,
	}
	retryCtx := toolshared.WithToolCallID(ctx, "document-form-fill-retry")
	replayed := tool.Execute(retryCtx, retryArgs)
	if replayed.IsError || len(replayed.Media) != 0 || replayed.Delivery.Commit != nil ||
		!strings.Contains(replayed.ForLLM, string(document.WriteDelivered)) {
		t.Fatalf("delivered operation was replayed: %#v", replayed)
	}
	if err = store.ReleaseAll("document-form-" + deliveryID); err != nil {
		t.Fatal(err)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCodingDocumentLocalWriteToolLinuxIntegration(t *testing.T) {
	requireDocumentFormBackend(t)
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "coding-thread", "document-writes")
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	sourcePath = filepath.Join(filepath.Dir(sourcePath), "..", "document", "testdata", "acroform-fields.pdf")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(workspace, "local-form.pdf")
	if err = os.WriteFile(localPath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	mediaIndex := filepath.Join(t.TempDir(), "document-artifacts", "index.json")
	store, err := media.NewFileMediaStoreWithPersistentIndex(mediaIndex, media.MediaCleanerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	const sessionID = "coding:document-local-write"
	owner, err := media.NewRuntimeMediaOwner(
		workspace,
		string(runtimecap.KindCoding),
		"main",
		"local:pdf-operator",
		sessionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := tools.NewDocumentTool(
		tools.WithDocumentLocalWriteSurface(),
		tools.WithDocumentStateRoot(stateRoot),
		tools.WithDocumentLocalPathPolicy(workspace, true, nil),
	)
	tool.SetMediaStore(store)
	principal := runtimecap.Principal{
		Runtime: runtimecap.KindCoding, ActorID: "local:pdf-operator", AgentID: "main",
		SessionID: sessionID, ExecutionID: "coding-document-local-write",
	}
	runtimeContext := runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}).BindPrincipal(principal)
	ctx := toolshared.WithRuntimeCapabilities(t.Context(), runtimeContext)
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, principal.ExecutionID)
	ctx = toolshared.WithToolSessionContext(ctx, "main", sessionID, nil)
	ctx = toolshared.WithToolCallID(ctx, "coding-document-fill")
	ctx = toolshared.WithToolDocumentContext(ctx, nil, true)
	ctx = toolshared.WithToolDocumentLocalPaths(ctx, []string{localPath})

	inspected := tool.Execute(ctx, map[string]any{"action": "inspect", "path": localPath})
	if inspected.IsError {
		t.Fatalf("local inspect failed: safe=%s internal=%v", inspected.ForLLM, inspected.Err)
	}
	var inspectedReport struct {
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err = json.Unmarshal([]byte(inspected.ForLLM), &inspectedReport); err != nil ||
		inspectedReport.Source.Ref == "" {
		t.Fatalf("local inspect report = %#v err=%v", inspectedReport, err)
	}
	ref := inspectedReport.Source.Ref
	fields := tool.Execute(ctx, map[string]any{"action": "fields", "source": ref})
	if fields.IsError {
		t.Fatalf("fields failed: safe=%s internal=%v", fields.ForLLM, fields.Err)
	}
	fieldID := documentFieldIDFromSafeReport(t, fields.ForLLM, "full_name")
	privateValue := "MintClaw Coding Local Private Value"
	filled := tool.Execute(ctx, map[string]any{
		"action": "fill",
		"source": ref,
		"assignments": []any{map[string]any{
			"field_id": fieldID,
			"value":    map[string]any{"type": "text", "text": privateValue},
		}},
	})
	if filled.IsError || filled.ForUser != "" || len(filled.Media) != 0 || filled.Deliverable == nil ||
		strings.Contains(filled.ForLLM, privateValue) || strings.Contains(filled.ForLLM, `"delivery"`) ||
		filled.Delivery.Outbound != nil || filled.Delivery.Commit != nil || filled.Delivery.Settle != nil {
		t.Fatalf("coding local fill result = %#v", filled)
	}
	var filledReport struct {
		OperationID   string `json:"operation_id"`
		LocalArtifact struct {
			State document.WriteOperationState `json:"state"`
			Ref   string                       `json:"ref"`
		} `json:"local_artifact"`
		Artifacts []struct {
			Ref string `json:"ref"`
		} `json:"artifacts"`
	}
	if err = json.Unmarshal([]byte(filled.ForLLM), &filledReport); err != nil ||
		filledReport.OperationID == "" || len(filledReport.Artifacts) != 1 ||
		filledReport.Artifacts[0].Ref != filledReport.LocalArtifact.Ref ||
		filledReport.LocalArtifact.Ref == "" ||
		filledReport.LocalArtifact.State != document.WriteRegistered {
		t.Fatalf("coding local fill report = %#v, %v", filledReport, err)
	}
	assertDocumentWriteState(t, stateRoot, filledReport.OperationID, owner, document.WriteRegistered)

	verified := tool.Execute(ctx, map[string]any{
		"action": "verify", "source": filledReport.LocalArtifact.Ref, "operation_id": filledReport.OperationID,
	})
	if verified.IsError || !strings.Contains(verified.ForLLM, `"operation":"verify"`) ||
		strings.Contains(verified.ForLLM, privateValue) || strings.Contains(verified.ForLLM, sourcePath) {
		t.Fatalf("coding local verify result = %#v", verified)
	}
	if _, err = store.Resolve(filledReport.LocalArtifact.Ref); err != nil {
		t.Fatalf("coding local artifact is not durable: %v", err)
	}
	if err = tool.CleanupTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Resolve(filledReport.LocalArtifact.Ref); err != nil {
		t.Fatalf("coding local artifact was removed by turn cleanup: %v", err)
	}
}

func documentFormToolContext(t *testing.T, workspace string, refs ...string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-form-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-form-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-form-execution")
	ctx = toolshared.WithToolCallID(ctx, "document-form-fill-call")
	return toolshared.WithToolDocumentContext(ctx, refs, false)
}

func documentFieldIDFromSafeReport(t *testing.T, encoded string, name string) string {
	t.Helper()
	var report struct {
		Fields struct {
			Fields []document.FormField `json:"fields"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(encoded), &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range report.Fields.Fields {
		if field.Name == name {
			return field.ID
		}
	}
	t.Fatalf("field %q is absent from %s", name, encoded)
	return ""
}

func documentWriteIDsFromSafeReport(t *testing.T, encoded string) (string, string) {
	t.Helper()
	var report struct {
		OperationID string `json:"operation_id"`
		Delivery    struct {
			DeliveryID string `json:"delivery_id"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(encoded), &report); err != nil {
		t.Fatal(err)
	}
	if report.OperationID == "" || report.Delivery.DeliveryID == "" {
		t.Fatalf("write identities are absent from %s", encoded)
	}
	return report.OperationID, report.Delivery.DeliveryID
}

func assertDocumentWriteState(
	t *testing.T,
	stateRoot string,
	operationID string,
	owner media.MediaOwner,
	want document.WriteOperationState,
) {
	t.Helper()
	journal, err := document.NewWriteJournal(filepath.Join(stateRoot, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := journal.Lookup(t.Context(), operationID, document.Authority{
		Kind: "inbound_media", WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID,
		ActorID: owner.ActorID, RouteID: owner.RouteID, SessionID: owner.SessionID,
	})
	if err != nil || !found || record.State != want {
		t.Fatalf("write state = %#v found=%v err=%v want=%s", record, found, err, want)
	}
}

func documentLocalPathToolContext(t *testing.T, workspace string, path string) context.Context {
	t.Helper()
	ctx := toolshared.WithToolInboundContext(t.Context(), "telegram", "pdf-chat", "pdf-message", "")
	ctx = toolshared.WithToolInboundMetadata(ctx, bus.InboundContext{
		Channel: "telegram", ChatID: "pdf-chat", TopicID: "pdf-topic",
		SenderID: "pdf-operator", ActorID: "pdf-operator",
	})
	ctx = toolshared.WithToolTopicID(ctx, "pdf-topic")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "document-local-session", nil)
	ctx = toolshared.WithToolRouteSessionKey(ctx, "document-local-route")
	ctx = toolshared.WithToolExecutionIdentity(ctx, workspace, "document-local-execution")
	ctx = toolshared.WithToolDocumentContext(ctx, nil, true)
	return toolshared.WithToolDocumentLocalPaths(ctx, []string{path})
}

func requireDocumentReadBackend(t *testing.T) {
	t.Helper()
	capabilities := document.Capabilities()
	for _, operation := range []string{"inspect", "extract", "render"} {
		if capabilities.Operations[operation].State != document.CapabilitySupported {
			if os.Getenv("MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E") == "1" {
				t.Fatalf(
					"document %s capability is required: %s",
					operation,
					capabilities.Operations[operation].Reason,
				)
			}
			t.Skipf("document %s capability is unavailable: %s", operation, capabilities.Operations[operation].Reason)
		}
	}
}

func requireDocumentFormBackend(t *testing.T) {
	t.Helper()
	capabilities := document.Capabilities()
	for _, operation := range []string{"fields", "fill", "verify"} {
		if capabilities.Operations[operation].State != document.CapabilitySupported {
			if os.Getenv("MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E") == "1" {
				t.Fatalf(
					"document %s capability is required: %s",
					operation,
					capabilities.Operations[operation].Reason,
				)
			}
			t.Skipf("document %s capability is unavailable: %s", operation, capabilities.Operations[operation].Reason)
		}
	}
}

func configureDocumentE2E(cfg *config.Config, model string, vision bool) {
	cfg.Agents.Defaults.ModelName = model
	cfg.Agents.Defaults.ContextWindow = 32_768
	cfg.Agents.Defaults.ResponseFooter.Enabled = false
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	cfg.Tools.Document.Enabled = true
	cfg.Tools.Approval.Mode = config.ToolApprovalModeAllowAll
	cfg.Diagnostics.TraceCapture = config.DiagnosticTraceCaptureConfig{
		Enabled: true, ContentMode: "redacted_content", RetentionHours: 1, MaxTraces: 10,
	}
	if vision {
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: model,
			Provider:  "openai",
			Model:     model,
			Enabled:   true,
			Capabilities: &config.ModelCapabilities{
				Vision: &config.ModelCapabilityOverride{},
			},
		}}
	}
}

func documentE2EWorkspace(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "skills", "bundled", "pdf", "SKILL.md")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	destination := filepath.Join(workspace, "skills", "pdf")
	if err = os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(destination, "SKILL.md"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func documentE2ESource(t *testing.T, fixture string) (*media.FileMediaStore, string, string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	source := filepath.Join(filepath.Dir(currentFile), "..", "document", "testdata", fixture)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	store, err := media.NewFileMediaStoreWithPersistentIndex(
		filepath.Join(t.TempDir(), "media", "index.json"),
		media.MediaCleanerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	ref, err := store.Store(source, media.MediaMeta{
		Filename:      "same-name.pdf",
		ContentType:   "",
		Source:        "test:document-telegram-e2e",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "document-telegram-e2e-source")
	if err != nil {
		t.Fatal(err)
	}
	return store, ref, hex.EncodeToString(digest[:]), source
}

func documentFixturePath(t *testing.T, fixture string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "document", "testdata", fixture)
}

func codingDocumentE2ESource(
	t *testing.T,
	fixture string,
) (string, CodingRuntimeLayout, *codingDocumentTestMediaStore, string, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	layout, err := NewCodingRuntimeLayout(
		"thread-document-"+strings.TrimSuffix(fixture, filepath.Ext(fixture)),
		workspace,
		filepath.Join(root, "state"),
		[]string{workspace},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := documentFixturePath(t, fixture)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	store := newCodingDocumentTestMediaStore()
	t.Cleanup(store.owned.Stop)
	ref, err := store.Store(source, media.MediaMeta{
		Filename:      "coding-source.pdf",
		ContentType:   "application/pdf",
		Source:        "test:coding-document-e2e",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "coding-document-e2e-source")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := media.NewRuntimeMediaOwner(
		workspace,
		"coding",
		"main",
		"local:document-e2e",
		layout.SessionKey(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	return workspace, layout, store, ref, hex.EncodeToString(digest[:])
}

func newCodingDocumentE2ELoop(
	t *testing.T,
	workspace string,
	layout CodingRuntimeLayout,
	store *codingDocumentTestMediaStore,
	provider *llmscenario.ScriptedProvider,
	vision bool,
) *AgentLoop {
	t.Helper()
	profile, err := NewCodingRuntimeProfile(CodingRuntimeBinding{AgentID: "main", Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ContextManager = "none"
	configureDocumentE2E(cfg, provider.GetDefaultModel(), vision)
	cfg.Agents.Defaults.MaxTokens = 4096
	loop, err := NewCodingAgentLoop(
		t.Context(),
		cfg,
		bus.NewMessageBus(),
		provider,
		profile,
		WithCodingMediaStore(store),
		WithRuntimeActorID("local:document-e2e"),
		WithIsolatedSkillBootstrap(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Close)
	return loop
}

func codingDocumentFirstCallAssertion(ref string) func(llmscenario.ProviderCall) error {
	return func(call llmscenario.ProviderCall) error {
		if err := llmscenario.RequireToolDefinition("document")(call); err != nil {
			return err
		}
		for _, definition := range call.Tools {
			if definition.Function.Name != "document" {
				continue
			}
			properties, ok := definition.Function.Parameters["properties"].(map[string]any)
			if !ok {
				return errors.New("coding document schema has no properties")
			}
			action, ok := properties["action"].(map[string]any)
			if !ok {
				return errors.New("coding document schema has no action selector")
			}
			enum, ok := action["enum"].([]string)
			if !ok || !slices.Equal(
				enum,
				[]string{"inspect", "extract", "render", "fields", "fill", "verify"},
			) {
				return fmt.Errorf("coding document actions = %#v", action["enum"])
			}
			for _, required := range []string{"assignments", "operation_id"} {
				if _, present := properties[required]; !present {
					return fmt.Errorf("coding document schema omits %q", required)
				}
			}
			for _, forbidden := range []string{"retain", "form_action", "answer_ref", "job_id"} {
				if _, present := properties[forbidden]; present {
					return fmt.Errorf("coding document schema exposes %q", forbidden)
				}
			}
			break
		}
		for _, message := range call.Messages {
			if message.Role != "user" || !strings.Contains(message.Content, ref) {
				continue
			}
			for _, attachment := range message.Attachments {
				if attachment.Ref == ref && attachment.Type == "document" &&
					attachment.ContentType == "application/pdf" {
					return nil
				}
			}
		}
		return errors.New("coding PDF was not projected as an opaque document attachment")
	}
}

func latestCodingDocumentArtifactRef(store *codingDocumentTestMediaStore) string {
	if store == nil {
		return ""
	}
	store.documentArtifactMu.Lock()
	defer store.documentArtifactMu.Unlock()
	if len(store.documentArtifactRefs) == 0 {
		return ""
	}
	return store.documentArtifactRefs[len(store.documentArtifactRefs)-1]
}

func documentTextE2EProvider(ref, digest, sourcePath string) *llmscenario.ScriptedProvider {
	return llmscenario.NewScriptedProvider(
		"document-text-e2e-model",
		llmscenario.ProviderStep{
			Name:   "discover document tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document", tools.BM25SearchToolName, map[string]any{
					"query": "inspect extract render the exact current PDF attachment",
				},
			)),
		},
		llmscenario.ProviderStep{
			Name:   "inspect document",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		llmscenario.ProviderStep{
			Name: "extract page",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "\"page_count\":1")(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"extract-document", "document", map[string]any{
					"action": "extract", "source": ref, "pages": []any{float64(1)},
				},
			)),
		},
		llmscenario.ProviderStep{
			Name: "answer from protected text",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", "[page 1]")(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "MintClaw text fixture")(call)
			},
			Response: llmscenario.TextResponse("The marker is MintClaw text fixture [page 1]."),
		},
	)
}

func documentRenderE2EProvider(ref, digest, sourcePath string) *llmscenario.ScriptedProvider {
	return llmscenario.NewScriptedProvider(
		"document-render-e2e-model",
		llmscenario.ProviderStep{
			Name:   "discover document tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document", tools.BM25SearchToolName, map[string]any{
					"query": "inspect and render the exact current PDF attachment",
				},
			)),
		},
		llmscenario.ProviderStep{
			Name:   "inspect document",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		llmscenario.ProviderStep{
			Name: "render retained page",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", "\"page_count\":1")(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"render-document", "document", map[string]any{
					"action": "render", "source": ref, "pages": []any{float64(1)}, "retain": true,
				},
			)),
		},
		llmscenario.ProviderStep{
			Name: "confirm rendered page",
			Assert: func(call llmscenario.ProviderCall) error {
				foundReport := false
				foundUnexpectedImage := false
				for index := len(call.Messages) - 1; index >= 0; index-- {
					message := call.Messages[index]
					if message.Role == "tool" && strings.Contains(message.Content, "page_render") {
						foundReport = true
					}
					if len(message.Media) > 0 {
						foundUnexpectedImage = true
					}
				}
				if !foundReport || foundUnexpectedImage {
					return fmt.Errorf(
						"render report/unexpected image context = (%v, %v)",
						foundReport,
						foundUnexpectedImage,
					)
				}
				return nil
			},
			Response: llmscenario.TextResponse("Rendered and delivered source page 1."),
		},
	)
}

func documentFormE2EProvider(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
) *llmscenario.ScriptedProvider {
	steps := documentFormE2ESteps(ref, digest, sourcePath, fieldID, privateValue)
	steps = append(steps, llmscenario.ProviderStep{
		Name: "confirm safe verified delivery",
		Assert: func(call llmscenario.ProviderCall) error {
			joined := documentE2ECallText(call)
			if strings.Contains(joined, privateValue) {
				return errors.New("private form value survived into the post-tool model context")
			}
			for _, required := range []string{
				`"operation":"fill"`,
				`"operation_id":"document_write_`,
				`"output_sha256"`,
				`"delivered":true`,
			} {
				if !strings.Contains(joined, required) {
					return fmt.Errorf("safe fill evidence %q is absent from %s", required, joined)
				}
			}
			for _, message := range call.Messages {
				if message.Role == "tool" && len(message.Media) > 0 {
					return errors.New("delivered form was reattached to the model context")
				}
			}
			return nil
		},
		Response: llmscenario.TextResponse("Filled form delivered."),
	})
	return llmscenario.NewScriptedProvider("document-form-e2e-model", steps...)
}

func documentFormDeliveryE2EProvider(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
	modelContinuation bool,
) *llmscenario.ScriptedProvider {
	steps := documentFormE2ESteps(ref, digest, sourcePath, fieldID, privateValue)
	if modelContinuation {
		steps = append(steps, llmscenario.ProviderStep{
			Name: "report definite delivery rejection",
			Assert: func(call llmscenario.ProviderCall) error {
				joined := documentE2ECallText(call)
				if strings.Contains(joined, privateValue) {
					return errors.New("private form value survived into the delivery failure context")
				}
				if !strings.Contains(joined, "definitely failed before remote acceptance") {
					return fmt.Errorf("definite delivery failure was not propagated safely: %s", joined)
				}
				return nil
			},
			Response: llmscenario.TextResponse("The verified PDF was not delivered."),
		})
	}
	return llmscenario.NewScriptedProvider("document-form-delivery-e2e-model", steps...)
}

func documentFormE2ESteps(
	ref string,
	digest string,
	sourcePath string,
	fieldID string,
	privateValue string,
) []llmscenario.ProviderStep {
	return []llmscenario.ProviderStep{
		{
			Name:   "discover document form tool",
			Assert: documentFirstCallAssertion(ref, sourcePath),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"search-document-form", tools.BM25SearchToolName, map[string]any{
					"query": "inspect fields fill and verify the exact current PDF form attachment",
				},
			)),
		},
		{
			Name:   "inspect form",
			Assert: llmscenario.RequireToolDefinition("document"),
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"inspect-document-form", "document", map[string]any{"action": "inspect", "source": ref},
			)),
		},
		{
			Name: "discover form fields",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", digest)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", `"page_count":2`)(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"fields-document-form", "document", map[string]any{"action": "fields", "source": ref},
			)),
		},
		{
			Name: "fill verified form",
			Assert: func(call llmscenario.ProviderCall) error {
				if err := llmscenario.RequireLastMessage("tool", fieldID)(call); err != nil {
					return err
				}
				return llmscenario.RequireLastMessage("tool", `"name":"full_name"`)(call)
			},
			Response: llmscenario.ToolCallResponse("", llmscenario.ToolCall(
				"fill-document-form", "document", map[string]any{
					"action": "fill",
					"source": ref,
					"assignments": []any{map[string]any{
						"field_id": fieldID,
						"value":    map[string]any{"type": "text", "text": privateValue},
					}},
				},
			)),
		},
	}
}

func documentE2EFieldID(t *testing.T, sourcePath string, name string) string {
	t.Helper()
	snapshot, report := document.Fields(
		t.Context(),
		sourcePath,
		document.AcquireOptions{ScratchRoot: filepath.Join(t.TempDir(), "scratch")},
	)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if report.State != document.StateSucceeded || report.Fields == nil {
		t.Fatalf("field discovery report = %#v", report)
	}
	for _, field := range report.Fields.Fields {
		if field.Name == name {
			return field.ID
		}
	}
	t.Fatalf("field %q is absent", name)
	return ""
}

func assertDocumentFormJournal(
	t *testing.T,
	home string,
	forbidden string,
	artifactRef string,
	want document.WriteOperationState,
) document.WriteOperationRecord {
	t.Helper()
	directory := filepath.Join(home, "state", "document-writes", "journal")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var records int
	var matched document.WriteOperationRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("document journal retained a protected value: %s", data)
		}
		var record document.WriteOperationRecord
		if err = json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.State != want || record.ArtifactRef != artifactRef || record.DeliveryID == "" {
			t.Fatalf("document journal record = %#v", record)
		}
		matched = record
		records++
	}
	if records != 1 {
		t.Fatalf("document journal record count = %d, want 1", records)
	}
	return matched
}

func waitDocumentFormJournalState(
	t *testing.T,
	home string,
	want document.WriteOperationState,
	channel *fakeMediaChannel,
) document.WriteOperationRecord {
	t.Helper()
	directory := filepath.Join(home, "state", "document-writes", "journal")
	var matched document.WriteOperationRecord
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
				if readErr != nil || json.Unmarshal(data, &matched) != nil {
					continue
				}
				if matched.State == want {
					return matched
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	channel.mu.Lock()
	messages := append([]bus.OutboundMessage(nil), channel.sentMessages...)
	media := append([]bus.OutboundMediaMessage(nil), channel.sentMedia...)
	channel.mu.Unlock()
	t.Fatalf(
		"timed out waiting for document journal state %s: last_record=%#v messages=%#v media=%#v",
		want,
		matched,
		messages,
		media,
	)
	return document.WriteOperationRecord{}
}

func documentFirstCallAssertion(ref, sourcePath string) func(llmscenario.ProviderCall) error {
	return func(call llmscenario.ProviderCall) error {
		if err := llmscenario.RequireToolDefinition(tools.BM25SearchToolName)(call); err != nil {
			return err
		}
		for _, definition := range call.Tools {
			if definition.Function.Name == "document" {
				return fmt.Errorf("hidden document tool was exposed before discovery")
			}
		}
		joined := documentE2ECallText(call)
		if !strings.Contains(joined, "# PDF") || !strings.Contains(joined, ref) {
			return fmt.Errorf("PDF skill or exact ref is absent from first call")
		}
		for _, required := range []string{
			"ordinary request to complete",
			"`form_action: discover`",
			"`form_action: start`",
			"`form_action: collect`",
			"`protected_answer_ref`",
			"Never substitute an",
			"`interaction_id`",
			"If `form` is absent but `fields`, `fill`,",
			"Do not solicit missing protected values through",
			"ordinary chat",
			"The branch is unavailable unless all three",
		} {
			if !strings.Contains(joined, required) {
				return fmt.Errorf("PDF agentic intake contract %q is absent from first call", required)
			}
		}
		if strings.Contains(joined, sourcePath) || strings.Contains(joined, "%PDF-") {
			return fmt.Errorf("first call leaked local path or PDF bytes")
		}
		return nil
	}
}

func documentE2ECallText(call llmscenario.ProviderCall) string {
	var builder strings.Builder
	for _, message := range call.Messages {
		builder.WriteString(message.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func startDocumentE2EChannel(
	t *testing.T,
	fixture *agentLoopTestFixture,
	store media.MediaStore,
	channel *fakeMediaChannel,
) func() {
	t.Helper()
	coordinator, err := outbox.OpenCoordinator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture.Loop.SetOutboundOutbox(coordinator)
	t.Cleanup(func() {
		fixture.Loop.SetOutboundOutbox(nil)
		if err := coordinator.Close(); err != nil {
			t.Errorf("close outbox: %v", err)
		}
	})
	manager := newStartedTestChannelManagerWithConfig(
		t,
		fixture.Config,
		fixture.Bus,
		store,
		"telegram",
		channel,
		channels.WithRuntimeEvents(fixture.Loop.runtimeEvents),
		channels.WithOutboundOutbox(coordinator),
	)
	fixture.Loop.SetChannelManager(manager)
	runContext, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fixture.Loop.Run(runContext) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent loop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent loop did not stop")
		}
	}
}

func publishDocumentE2EInbound(t *testing.T, messageBus *bus.MessageBus, ref, content string) {
	t.Helper()
	if err := messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "telegram",
			ChatID:    "pdf-chat",
			ChatType:  "direct",
			TopicID:   "pdf-topic",
			SenderID:  "pdf-operator",
			ActorID:   "pdf-operator",
			MessageID: "pdf-message",
		},
		Content:    content,
		Media:      []string{ref},
		SessionKey: "document-pdf1a-e2e",
		SpoolID:    "document-pdf1a-e2e-" + strings.TrimPrefix(ref, "media://"),
	}); err != nil {
		t.Fatal(err)
	}
}

func publishDocumentE2EFollowup(t *testing.T, messageBus *bus.MessageBus, content string, sequence int) {
	t.Helper()
	messageID := fmt.Sprintf("pdf-followup-%d", sequence)
	if err := messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "telegram",
			ChatID:    "pdf-chat",
			ChatType:  "direct",
			TopicID:   "pdf-topic",
			SenderID:  "pdf-operator",
			ActorID:   "pdf-operator",
			MessageID: messageID,
		},
		Content:    content,
		SessionKey: "document-pdf1a-e2e",
		SpoolID:    messageID,
	}); err != nil {
		t.Fatal(err)
	}
}

func waitDocumentFormReviewReady(t *testing.T, channel *fakeMediaChannel) {
	t.Helper()
	waitDocumentE2EChannel(t, channel, func() bool {
		for _, message := range channel.messagesSnapshot() {
			if message.Content == "Form review is ready." {
				return true
			}
		}
		return false
	})
}

func waitDocumentFormQuestion(
	t *testing.T,
	channel *fakeMediaChannel,
	answered map[string]struct{},
) string {
	t.Helper()
	var shortID string
	waitDocumentE2E(t, func() bool {
		for _, message := range channel.messagesSnapshot() {
			candidate := strings.TrimSpace(message.Metadata.InteractionShortID)
			if !message.Metadata.IsQuestionPrompt() || candidate == "" {
				continue
			}
			if _, exists := answered[candidate]; !exists {
				shortID = candidate
				return true
			}
		}
		return false
	})
	return shortID
}

func documentFormQuestionMessage(
	t *testing.T,
	channel *fakeMediaChannel,
	shortID string,
) bus.OutboundMessage {
	t.Helper()
	for _, message := range channel.messagesSnapshot() {
		if message.Metadata.IsQuestionPrompt() &&
			strings.EqualFold(message.Metadata.InteractionShortID, shortID) {
			return message
		}
	}
	t.Fatalf("question prompt %q is unavailable", shortID)
	return bus.OutboundMessage{}
}

func waitDocumentFormApproval(t *testing.T, channel *fakeMediaChannel) string {
	t.Helper()
	var shortID string
	waitDocumentE2E(t, func() bool {
		for _, message := range channel.messagesSnapshot() {
			candidate := strings.TrimSpace(message.Metadata.InteractionShortID)
			if message.Metadata.IsApprovalPrompt() && candidate != "" {
				shortID = candidate
				return true
			}
		}
		return false
	})
	return shortID
}

func waitDocumentFormInteractionResolved(t *testing.T, workspace, shortID string) {
	t.Helper()
	path := interactions.WorkspaceStorePath(workspace)
	waitDocumentE2E(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var snapshot struct {
			Records []interactions.Record `json:"records"`
		}
		if json.Unmarshal(data, &snapshot) != nil {
			return false
		}
		for _, record := range snapshot.Records {
			if record.ShortID == shortID {
				return record.Status == interactions.StatusResolved && len(record.FinalDeliveryIDs) > 0
			}
		}
		return false
	})
}

func waitDocumentFormInteractionWaiting(t *testing.T, workspace, shortID string) {
	t.Helper()
	path := interactions.WorkspaceStorePath(workspace)
	waitDocumentE2E(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var snapshot struct {
			Records []interactions.Record `json:"records"`
		}
		if json.Unmarshal(data, &snapshot) != nil {
			return false
		}
		for _, record := range snapshot.Records {
			if record.ShortID == shortID {
				return record.Status == interactions.StatusWaiting
			}
		}
		return false
	})
}

func publishDocumentE2EAnswer(
	t *testing.T,
	messageBus *bus.MessageBus,
	shortID string,
	answer string,
	ordinal int,
) {
	t.Helper()
	messageID := fmt.Sprintf("pdf-form-answer-%d", ordinal)
	if err := messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "pdf-chat", ChatType: "direct", TopicID: "pdf-topic",
			SenderID: "pdf-operator", ActorID: "pdf-operator", MessageID: messageID,
		},
		Content:    "/answer " + shortID + " " + answer,
		SessionKey: "document-pdf1a-e2e",
		SpoolID:    messageID,
	}); err != nil {
		t.Fatal(err)
	}
}

func publishDocumentE2ENavigation(
	t *testing.T,
	messageBus *bus.MessageBus,
	shortID string,
	responseMessageID string,
	choice bus.InboundInteractionChoice,
	ordinal int,
) {
	t.Helper()
	content := ""
	switch choice {
	case bus.InboundInteractionChoiceClarify:
		content = bus.InboundInteractionClarifyLabel
	case bus.InboundInteractionChoiceBack:
		content = bus.InboundInteractionBackLabel
	default:
		t.Fatalf("unsupported document navigation choice %q", choice)
	}
	messageID := fmt.Sprintf("pdf-form-navigation-%d", ordinal)
	if strings.TrimSpace(responseMessageID) == "" {
		t.Fatal("document navigation prompt message ID is unavailable")
	}
	if err := messageBus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "telegram", ChatID: "pdf-chat", ChatType: "direct", TopicID: "pdf-topic",
			SenderID: "pdf-operator", ActorID: "pdf-operator", MessageID: messageID,
			ReplyToMessageID: responseMessageID,
			Interaction: bus.InboundInteractionProjection{
				Choice: choice, ShortID: shortID, ResponseMessageID: responseMessageID,
			},
		},
		Content:    content,
		SessionKey: "document-pdf1a-e2e",
		SpoolID:    messageID,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDocumentFormPromptCounts(t *testing.T, channel *fakeMediaChannel, wantQuestions, wantApprovals int) {
	t.Helper()
	questions := 0
	approvals := 0
	for _, message := range channel.messagesSnapshot() {
		if message.Metadata.IsQuestionPrompt() {
			questions++
		}
		if message.Metadata.IsApprovalPrompt() {
			approvals++
		}
	}
	if questions != wantQuestions || approvals != wantApprovals {
		t.Fatalf(
			"protected prompt delivery counts = questions:%d approvals:%d, want questions:%d approvals:%d",
			questions,
			approvals,
			wantQuestions,
			wantApprovals,
		)
	}
}

func assertDocumentSourceDigest(t *testing.T, sourcePath, want string) {
	t.Helper()
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("source digest changed: got %s, want %s", got, want)
	}
}

func assertDocumentFormReviewState(t *testing.T, workspace, home string, forbidden ...string) {
	t.Helper()
	paths := []string{
		interactions.WorkspaceStorePath(workspace),
		filepath.Join(
			home,
			"state",
			"document-form-jobs",
			"document_form_jobs",
			"form_jobs.v1.json",
		),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read protected form state %s: %v", filepath.Base(path), err)
		}
		for _, value := range forbidden {
			if strings.Contains(string(data), value) {
				t.Fatalf("protected form state retained value %q: %s", value, data)
			}
		}
		if strings.HasSuffix(path, "form_jobs.v1.json") &&
			!strings.Contains(string(data), `"state":"review_ready"`) {
			t.Fatalf("protected form job did not reach review_ready: %s", data)
		}
	}
	traceRoot := filepath.Join(workspace, "state", "diagnostics", "traces")
	var scanErr error
	waitDocumentE2E(t, func() bool {
		foundTrace := false
		scanErr = filepath.WalkDir(traceRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			foundTrace = true
			for _, value := range forbidden {
				if strings.Contains(string(data), value) {
					return fmt.Errorf("diagnostic trace retained protected value %q", value)
				}
			}
			return nil
		})
		return scanErr != nil || foundTrace
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
}

func assertDocumentFormCommitState(t *testing.T, workspace, home string, forbidden ...string) {
	t.Helper()
	paths := []string{
		interactions.WorkspaceStorePath(workspace),
		filepath.Join(
			home,
			"state",
			"document-form-jobs",
			"document_form_jobs",
			"form_jobs.v1.json",
		),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read protected form commit state %s: %v", filepath.Base(path), err)
		}
		for _, value := range forbidden {
			if strings.Contains(string(data), value) {
				t.Fatalf("protected form commit state retained value %q: %s", value, data)
			}
		}
		if !strings.HasSuffix(path, "form_jobs.v1.json") {
			continue
		}
		for _, required := range []string{
			`"state":"completed"`,
			`"operation_id":"document_write_`,
			`"artifact_ref":"media://`,
			`"artifact_digest":"`,
		} {
			if !strings.Contains(string(data), required) {
				t.Fatalf("protected form commit state omitted %q: %s", required, data)
			}
		}
	}
	traceRoot := filepath.Join(workspace, "state", "diagnostics", "traces")
	var scanErr error
	waitDocumentE2E(t, func() bool {
		foundTrace := false
		scanErr = filepath.WalkDir(traceRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			foundTrace = true
			for _, value := range forbidden {
				if strings.Contains(string(data), value) {
					return fmt.Errorf("diagnostic trace retained protected commit value %q", value)
				}
			}
			return nil
		})
		return scanErr != nil || foundTrace
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
}

func waitDocumentE2E(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for document Telegram vertical slice")
}

func closeDocumentE2EFixtureAfterTraceDrain(t *testing.T, fixture *agentLoopTestFixture) {
	t.Helper()
	if fixture == nil || fixture.Loop == nil || fixture.Loop.traceCapture == nil {
		return
	}
	writer := fixture.Loop.traceCapture.writer
	fixture.Close()
	if writer == nil {
		return
	}
	waitDocumentE2E(t, func() bool {
		stats := writer.Stats()
		terminal := stats.Persisted + stats.Dropped + stats.PermanentFailures
		return terminal >= stats.Accepted
	})
}

func waitDocumentE2EChannel(t *testing.T, channel *fakeMediaChannel, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	channel.mu.Lock()
	messages := append([]bus.OutboundMessage(nil), channel.sentMessages...)
	media := append([]bus.OutboundMediaMessage(nil), channel.sentMedia...)
	channel.mu.Unlock()
	t.Fatalf("timed out waiting for document Telegram vertical slice: messages=%#v media=%#v", messages, media)
}

func assertDocumentE2ETrace(t *testing.T, workspace, digest string, forbidden ...string) {
	t.Helper()
	directory := filepath.Join(workspace, "state", "diagnostics", "traces")
	var trace []byte
	waitDocumentE2E(t, func() bool {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) == 0 {
			return false
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			trace, err = os.ReadFile(filepath.Join(directory, entry.Name()))
			if err == nil {
				return true
			}
		}
		return false
	})
	var decoded any
	if err := json.Unmarshal(trace, &decoded); err != nil {
		t.Fatalf("decode document trace: %v", err)
	}
	compact, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("normalize document trace: %v", err)
	}
	text := string(compact)
	if !strings.Contains(text, `"tool":"document"`) || !strings.Contains(text, digest) ||
		(!strings.Contains(text, "selected_pages") && !strings.Contains(text, "affected_pages") &&
			!strings.Contains(text, "output_sha256")) {
		t.Fatalf("document lifecycle evidence is absent from trace: %s", text)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("document trace retained protected value %q: %s", value, text)
		}
	}
}
