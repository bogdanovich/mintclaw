package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

func questionAnswerCase(t *testing.T) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"general.interaction.answer"}})
	if err != nil || len(items) != 1 {
		t.Fatal("answer candidate missing", err)
	}
	return items[0]
}

func runQuestionAnswer(t *testing.T, mode string) (Trial, LoadedCase, *questionConversation, int) {
	t.Helper()
	item := questionAnswerCase(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	domain := &questionConversation{
		task: item.Case.Task, home: home, workspace: workspace, sessionID: "eval-answer-session",
		agent: "main", started: time.Now(), answerCheckID: questionAnswerCheckID(item.Case),
		filter: strings.NewReplacer("private-history-canary", "redacted"),
	}
	transport := newFakeConversation()
	scope := session.SessionScope{
		Version: session.ScopeVersion, AgentID: domain.agent, Channel: "mintclaw", Account: "default",
		ClientSessionID: domain.sessionID, Dimensions: []string{"chat"},
		Values: map[string]string{"chat": "direct:mintclaw:" + domain.sessionID},
	}
	sessionKey := session.BuildSessionKey(scope)
	var registry *interactions.Registry
	var record interactions.Record
	var history []providers.Message
	transport.onSend = func(request protocol.MintClawMessage, number int) {
		if number == 1 {
			var response protocol.MintClawMessage
			tool, err := tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{})
			if err != nil {
				t.Fatal(err)
			}
			arguments := map[string]any{"questions": []any{map[string]any{
				"id": "preference", "question": "Email or paper?",
				"options": []any{
					map[string]any{"label": "email", "description": "Use electronic messages."},
					map[string]any{"label": "paper", "description": "Use printed pages."},
				},
			}}}
			result := tool.Execute(t.Context(), arguments)
			if result.IsError || result.Control.Suspension == nil || len(result.Control.Suspension.Questions) != 1 {
				t.Fatal("native question tool did not suspend")
			}
			question := result.Control.Suspension.Questions[0]
			registry, record, response = nativeQuestionOptionsFixture(t, domain, request,
				sessionKey, question.Question, false, question.Options)
			history = []providers.Message{
				{Role: "user", Content: item.Case.Task, RootTurnStart: true},
				{Role: "assistant", ToolCalls: []providers.ToolCall{{
					ID: record.Origin.ToolCallID, Name: "request_user_input",
					Arguments: arguments,
				}}},
			}
			transport.events <- response
			return
		}
		if number == 2 {
			if request.Payload[protocol.PayloadKeyInteractionChoice] != protocol.InteractionChoiceAnswer ||
				request.Payload[protocol.PayloadKeyInteractionShortID] != record.ShortID ||
				request.Payload[protocol.PayloadKeyInteractionPrompt] != "prompt-1" ||
				request.Payload[protocol.PayloadKeyContent] != "paper" {
				t.Fatal("answer was not bound to the native delivered prompt")
			}
			if mode != "waiting-only" {
				answer := interactions.Answer{
					Text: "paper", Values: map[string]string{"preference": "paper"},
					MessageID: request.ID, ResponseMessageID: "prompt-1", ReceivedAt: time.Now().UnixMilli(),
				}
				if mode == "wrong-prompt" {
					answer.ResponseMessageID = "stale-prompt"
				}
				var err error
				record, err = registry.ClaimAnswer(record.ID, record.Revision, answer, interactions.OutcomeAnswered)
				if err == nil {
					record, err = registry.MarkResuming(record.ID, record.Revision)
				}
				if err == nil {
					record, err = registry.Resolve(record.ID, record.Revision)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := json.Marshal(map[string]any{
				"interaction_id": record.ID, "outcome": "answered", "text": "paper",
				"answers": map[string]string{"preference": "paper"},
			})
			if err != nil {
				t.Fatal(err)
			}
			history = append(history, providers.Message{
				Role: "tool", ToolCallID: record.Origin.ToolCallID, Content: string(result),
				ToolResultStatus: providers.ToolResultStatusSuccess,
			})
			if mode == "duplicate-result" {
				history = append(history, history[len(history)-1])
			}
			history = append(history, providers.Message{Role: "assistant", Content: "Saved paper."})
			if mode == "duplicate-continuation" {
				history = append(history, history[len(history)-1])
			}
			if mode == "private-history" {
				history[len(history)-1].Content = "private-history-canary"
			}
		} else {
			history = append(history,
				providers.Message{Role: "user", Content: request.Payload[protocol.PayloadKeyContent].(string)},
				providers.Message{Role: "assistant", Content: "paper"},
			)
		}
		if mode != "missing-history" {
			snapshot, err := memory.BuildJSONLSnapshot(record.Route.SessionKey, history, domain.started)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := memory.DecodeSessionMeta(snapshot.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			meta.ClientSessionIDs = []string{domain.sessionID}
			meta.Scope, err = json.Marshal(scope)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Metadata, err = json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{snapshot.JSONLFile: snapshot.JSONL, snapshot.MetadataFile: snapshot.Metadata} {
				if err = os.WriteFile(filepath.Join(workspace, "sessions", name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		response := testResponse(request, number)
		response.Payload[protocol.PayloadKeySessionKey] = sessionKey
		response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{
			{Workspace: workspace, TurnID: request.ID},
		}
		response.Payload[protocol.PayloadKeyContent] = "paper"
		if mode == "wrong-final" && number == 3 {
			response.Payload[protocol.PayloadKeyContent] = "email"
		}
		transport.events <- response
	}
	judge := &roleTestProvider{mediated: true, response: &providers.LLMResponse{
		Content: `{"version":1,"criteria":[{"id":"grounded-answer","verdict":"pass",` +
			`"reason":"The synthetic choice is grounded.","evidence_ids":["event-000002"]}]}`,
		FinishReason: "stop",
	}}
	trial := runDomainConversation(
		t.Context(),
		transport,
		item,
		domain.sessionID,
		domain.agent,
		workspace,
		conversationRoles{
			filter: domain.filter,
			judge:  &ModelRole{provider: judge, identity: RoleIdentity{Role: "judge"}},
		},
		domain,
	)
	return trial, item, domain, len(transport.sent)
}

func TestQuestionAnswerNeedsResolvedNativePairNotJudgeProse(t *testing.T) {
	for _, mode := range []string{
		"complete", "wrong-final", "wrong-prompt", "waiting-only", "missing-history",
		"duplicate-result", "duplicate-continuation", "private-history",
	} {
		t.Run(mode, func(t *testing.T) {
			trial, _, domain, sends := runQuestionAnswer(t, mode)
			if mode == "complete" {
				if _, err := domain.readAnswerHistory(t.Context()); err != nil {
					t.Fatal("fixture history admission failed", err)
				}
				if trial.Verdict != Pass || !validQuestionAnswer(trial, domain.workspace) || sends != 3 {
					t.Fatal("ordinary answer/resume proof failed", trial.Verdict, trial.Code, trial.Checks)
				}
			} else if trial.Verdict == Pass {
				t.Fatal("missing/contradictory native evidence became pass", mode, trial.Checks)
			}
			if (mode == "wrong-final" || mode == "private-history") && trial.Verdict != Fail {
				t.Fatal("known failure was softened", mode, trial.Verdict, trial.Code)
			}
			if (mode == "waiting-only" || mode == "missing-history") && (sends != 2 || trial.Answer != nil) {
				t.Fatal("unavailable answer evidence allowed a follow-up")
			}
		})
	}
}

func TestQuestionAnswerRetainedProofRoundTripAndRegrade(t *testing.T) {
	trial, item, domain, _ := runQuestionAnswer(t, "complete")
	data, err := json.Marshal(trial)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := decodeDocument[Trial](data, 8*1024*1024)
	if err != nil || !validQuestionAnswer(copy, domain.workspace) {
		t.Fatal("retained native answer proof did not round trip", err)
	}
	summary := TrialSummary{
		ID: trial.ID, CaseID: trial.CaseID, CaseSHA256: trial.CaseSHA256,
		Verdict: trial.Verdict, Code: trial.Code,
	}
	if !gradeEligible(copy, item, summary, Preset{ExpectedAgent: domain.agent}, domain.workspace) {
		t.Fatal("retained answer could not be regraded")
	}
	if err = os.RemoveAll(filepath.Join(domain.workspace, "sessions")); err != nil {
		t.Fatal(err)
	}
	for _, check := range gradeQuestionAnswerChecks(item.Case, copy, domain.workspace) {
		if check.Verdict != Pass {
			t.Fatal("regrade read current history instead of frozen evidence", check)
		}
	}
	for _, mode := range []string{"missing", "foreign-prompt", "resume-twice", "foreign-session", "duplicate-result", "digest"} {
		t.Run(mode, func(t *testing.T) {
			mutated, err := decodeDocument[Trial](data, 8*1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			var resolved interactions.Record
			var meta memory.SessionMeta
			if mutated.Answer != nil {
				resolved, _ = frozenQuestion(mutated.Answer.Resolved)
				meta, _ = memory.DecodeSessionMeta([]byte(mutated.Answer.Metadata))
			}
			switch mode {
			case "missing":
				mutated.Answer = nil
			case "foreign-prompt":
				resolved.Answer.ResponseMessageID = "foreign-prompt"
			case "resume-twice":
				resolved.ResumeTries = 2
			case "foreign-session":
				meta.Key = "foreign-session"
			case "duplicate-result":
				lines := mutated.Answer.History
				mutated.Answer.History = append(lines[:len(lines)-1], lines[2], "")
				meta.Count++
			case "digest":
				mutated.Answer.HistorySHA256 = strings.Repeat("0", 64)
			}
			if mutated.Answer != nil {
				resolvedJSON, _ := json.Marshal(resolved)
				metadataJSON, _ := json.Marshal(meta)
				mutated.Answer.Resolved = string(resolvedJSON)
				mutated.Answer.Metadata = string(metadataJSON)
				mutated.Answer.MetadataSHA256 = digest(metadataJSON)
				if mode != "digest" {
					mutated.Answer.HistorySHA256 = digest([]byte(strings.Join(mutated.Answer.History, "\n")))
				}
			}
			if validQuestionAnswer(mutated, domain.workspace) ||
				gradeEligible(mutated, item, summary, Preset{ExpectedAgent: domain.agent}, domain.workspace) {
				t.Fatal("changed native evidence reached regrading")
			}
		})
	}
}

func TestQuestionAnswerAdmissionCannotBypassPendingOrNativeScope(t *testing.T) {
	item := questionAnswerCase(t)
	if CheckRunAvailability([]LoadedCase{item}, false) != nil || !item.Availability().Default {
		t.Fatal("qualified answer is not active/default")
	}
	if _, err := Run(t.Context(), Preset{}, []LoadedCase{item}, RunOptions{}); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatal("promotion bypassed mandatory target-budget admission")
	}
	pending := item
	pending.Policy = &SuitePolicy{ID: item.Case.Suite, Status: "pending", Reason: "Synthetic pending policy control."}
	if _, err := Run(t.Context(), Preset{}, []LoadedCase{pending}, RunOptions{}); !errors.Is(err, ErrPendingCases) {
		t.Fatal("direct Run bypassed pending policy")
	}
	for _, mode := range []string{"no-check", "approval", "no-interactions", "adaptive", "other-answer", "no-rubric"} {
		t.Run(mode, func(t *testing.T) {
			candidate := questionAnswerCase(t).Case
			switch mode {
			case "no-check":
				candidate.Checks = candidate.Checks[1:]
			case "approval":
				candidate.Driver.Steps[0].WaitFor.Kind = "approval"
			case "no-interactions":
				candidate.Requires = []string{"conversation"}
			case "adaptive":
				candidate.Driver.Mode, candidate.Driver.Persona, candidate.Driver.Steps = "llm_user", "Synthetic.", nil
			case "other-answer":
				candidate.Driver.Steps[0].Action.Text = "allow_once"
			case "no-rubric":
				candidate.Rubric = nil
			}
			if candidate.Validate() == nil {
				t.Fatal("ordinary answer admission widened", mode)
			}
		})
	}
}

func TestQuestionAnswerHistoryAdmissionRejectsLinksWritesAndDiscardedPrivateValues(t *testing.T) {
	for _, mode := range []string{"file-link", "directory-link", "writable", "dirty", "partial", "duplicate-private-key"} {
		t.Run(mode, func(t *testing.T) {
			if runtime.GOOS == "windows" && (mode == "file-link" || mode == "directory-link" || mode == "writable") {
				t.Skip("native Windows ACL/link fixtures are not provisioned")
			}
			trial, _, domain, _ := runQuestionAnswer(t, "complete")
			if trial.Verdict != Pass {
				t.Fatal("invalid positive fixture", trial.Code)
			}
			historyName, metaName, err := memory.JSONLSessionFiles(trial.SessionKey)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(domain.workspace, "sessions")
			historyPath, metadataPath := filepath.Join(directory, historyName), filepath.Join(directory, metaName)
			switch mode {
			case "file-link":
				if err = os.Rename(historyPath, historyPath+".held"); err == nil {
					err = os.Symlink(historyName+".held", historyPath)
				}
			case "directory-link":
				if err = os.Rename(directory, directory+"-held"); err == nil {
					err = os.Symlink("sessions-held", directory)
				}
			case "writable":
				err = os.Chmod(historyPath, 0o666)
			case "dirty":
				var data []byte
				data, err = os.ReadFile(metadataPath)
				if err == nil {
					meta, decodeErr := memory.DecodeSessionMeta(data)
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					meta.HistoryDirty = true
					data, err = json.Marshal(meta)
					if err == nil {
						err = os.WriteFile(metadataPath, data, 0o644)
					}
				}
			case "partial", "duplicate-private-key":
				var data []byte
				data, err = os.ReadFile(historyPath)
				if err == nil {
					if mode == "partial" {
						data = data[:len(data)-10]
					} else {
						data = bytes.Replace(data, []byte(`"role":"user"`),
							[]byte(`"content":"private-history-canary","role":"user"`), 1)
					}
					err = os.WriteFile(historyPath, data, 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = domain.readAnswerHistory(t.Context())
			if err == nil || mode == "duplicate-private-key" && !errors.Is(err, ErrPrivateEvidence) {
				t.Fatal("unsafe or partial history became evidence", mode, err)
			}
		})
	}
}
