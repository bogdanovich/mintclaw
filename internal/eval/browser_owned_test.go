package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/html"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

// These children deliberately use HTTP, not a browser or an LLM. A real fixture
// visit plus plausible prose must still leave native browser checks unknown.
func ownedBrowserChildResponse(
	mode string,
	cfg *config.Config,
	previous string,
	request protocol.MintClawMessage,
	response *protocol.MintClawMessage,
) (string, bool) {
	_, profile := browserEvalProfile(cfg)
	origin := profile.AllowedOrigins[0]
	address := previous
	for _, word := range strings.Fields(payloadString(request.Payload, protocol.PayloadKeyContent)) {
		if strings.HasPrefix(word, origin+"/eval/") {
			address = strings.TrimSuffix(word, ".")
			break
		}
	}
	if address == "" || !strings.HasPrefix(address, origin+"/eval/") {
		return "", false
	}
	if mode == "browser_approval" {
		response.Payload[protocol.PayloadKeyContent] = "Allow this browser operation?"
		response.Payload[protocol.PayloadKeyFinal] = false
		response.Payload[protocol.PayloadKeyControls] = "prompt"
		response.Payload[protocol.PayloadKeyInteraction] = "approval"
		response.Payload[protocol.PayloadKeyInteractionID] = "browser-test-approval"
		response.Payload[protocol.PayloadKeyInteractionShortID] = "browser-test"
		return address, true
	}
	if mode == "browser_claim_only" {
		response.Payload[protocol.PayloadKeyContent] = "I read the page. Reference: fabricated."
		return address, true
	}
	if mode == "browser_artifact" && previous != "" {
		response.Payload[protocol.PayloadKeyContent] = "I claimed to deliver the CSV."
		return address, true
	}
	endpoint := address
	if mode == "browser_artifact" {
		endpoint += "report.csv"
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	fetched, err := client.Get(endpoint)
	if err != nil {
		return "", false
	}
	data, readErr := io.ReadAll(io.LimitReader(fetched.Body, 16385))
	closeErr := fetched.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > 16384 || fetched.StatusCode != http.StatusOK {
		return "", false
	}
	response.Payload[protocol.PayloadKeyContent] = "No file was actually delivered."
	if mode == "browser_artifact" {
		return address, true
	}
	tokens := html.NewTokenizer(strings.NewReader(string(data)))
	strong := false
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind == html.StartTagToken || kind == html.EndTagToken {
			name, _ := tokens.TagName()
			strong = kind == html.StartTagToken && string(name) == "strong"
		} else if kind == html.TextToken && strong {
			code := string(tokens.Text())
			if !strings.HasPrefix(code, "MC-") {
				return "", false
			}
			if mode == "browser_wrong" {
				code = "Wrong reference"
			}
			response.Payload[protocol.PayloadKeyContent] = code
			return address, true
		}
	}
	return "", false
}

type browserChildStopEvidence struct {
	Status   int               `json:"status"`
	Messages int               `json:"messages"`
	Choices  []string          `json:"choices"`
	URLs     map[string]string `json:"urls"`
}

func ownedBrowserChildStop(
	home string,
	cfg *config.Config,
	address string,
	messages int,
	choices []string,
	urls map[string]string,
) error {
	_, profile := browserEvalProfile(cfg)
	if address == "" {
		address = profile.AllowedOrigins[0] + "/eval/unknown/"
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(address)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	data, err := json.Marshal(
		browserChildStopEvidence{Status: response.StatusCode, Messages: messages, Choices: choices, URLs: urls},
	)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, ".security", "browser-child-stop.json"), data, 0o600)
}

func browserCorpusCase(t *testing.T, name string) LoadedCase {
	t.Helper()
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(corpus, Selection{IDs: []string{"browser.fixture." + name}})
	if err != nil || len(selected) != 1 {
		t.Fatal("browser corpus case unavailable", err)
	}
	return selected[0]
}

func TestOwnedBrowserRunExecutesFreshFixturesButCannotPassFromHTTPOrProse(t *testing.T) {
	for _, mode := range []string{"browser_read", "browser_claim_only", "browser_artifact", "browser_wrong", "browser_approval"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, mode)
			enableAdmissionBrowser(t, fixture)
			name := "read"
			if mode == "browser_artifact" {
				name = "artifact"
			}
			item := browserCorpusCase(t, name)
			result, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
				Home: fixture.home, OutputParent: fixture.output, StartTarget: true, Repeat: 2,
			})
			if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != 2 || result.Verdict == Pass ||
				result.BrowserFixtures == nil || result.BrowserFixtures.Code != "closed" || result.BrowserFixtures.Trials != 2 ||
				result.TargetLifecycle == nil || result.TargetLifecycle.Code != "native_exit_zero" {
				t.Fatal("owned fixture run lost coverage, closure, or native uncertainty", err)
			}
			for _, summary := range result.Trials {
				data, err := os.ReadFile(filepath.Join(result.ReportDir, summary.File))
				if err != nil {
					t.Fatal(err)
				}
				trial, err := DecodeManifest[Trial](data)
				if err != nil || trial.Browser == nil || trial.Browser.Fixture == nil || trial.EvidenceEligible ||
					trial.Repetition != summary.Repetition || trial.DurationMS != summary.DurationMS ||
					trial.Judge == nil || trial.Judge.Call != nil || trial.Browser.NativeAuthority != "unavailable" ||
					trial.Browser.NativeCleanup != "unavailable" || trial.Verdict == Pass {
					t.Fatal("fixture/prose evidence became native or billable judge eligibility", err)
				}
				if mode == "browser_wrong" && trial.Verdict != Fail {
					t.Fatal("observed wrong reference was erased by native uncertainty")
				}
				if mode == "browser_wrong" {
					for index, check := range item.Case.Checks {
						if check.Kind == "browser_no_write" && trial.Checks[index].Verdict != Inconclusive {
							t.Fatal("a wrong read was mislabeled as a forbidden write")
						}
					}
				}
				if mode == "browser_read" && (trial.Browser.Fixture.Read != Pass || trial.Browser.Fixture.Visits != 2) {
					t.Fatal("actual local fixture access was not independently recorded")
				}
				if mode == "browser_claim_only" && trial.Browser.Fixture.Visits != 0 {
					t.Fatal("prose invented a fixture visit")
				}
				if mode == "browser_artifact" &&
					(trial.Browser.Fixture.Downloads != 1 || trial.Browser.Fixture.Artifact == Pass) {
					t.Fatal("a fixture download invented artifact delivery")
				}
				if _, err = os.Stat(
					filepath.Join(result.ReportDir, "pending-"+summary.File),
				); !errors.Is(
					err,
					os.ErrNotExist,
				) {
					t.Fatal("pending browser checkpoint survived finalization")
				}
			}
			comparison, compareErr := Compare(result.ReportDir, result.ReportDir)
			if compareErr != nil || comparison.CurrentVerdict != result.Verdict ||
				len(comparison.Cases) != 1 || comparison.Cases[0].Current.Trials != 2 {
				t.Fatal("final browser evidence cannot be compared without losing repeated trials", compareErr)
			}
			data, err := os.ReadFile(filepath.Join(fixture.home, ".security", "browser-child-stop.json"))
			var stop browserChildStopEvidence
			if err != nil || json.Unmarshal(data, &stop) != nil || stop.Status != http.StatusGone ||
				len(stop.URLs) != 2 ||
				len(stop.Choices) != 0 {
				t.Fatal("fixture was released before native child stop or approval was sent", err)
			}
			_, profile := browserEvalProfile(fixture.cfg)
			listener, err := net.Listen("tcp", strings.TrimPrefix(profile.AllowedOrigins[0], "http://"))
			if err != nil {
				t.Fatal("owned fixture listener was not released", err)
			}
			_ = listener.Close()
		})
	}
}

func TestOwnedBrowserRunKeepsPrivateAndActionCasesVisiblyGated(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "browser_read")
	enableAdmissionBrowser(t, fixture)
	cases := []LoadedCase{
		browserCorpusCase(t, "submit"), browserCorpusCase(t, "followup"),
		browserCorpusCase(t, "ambiguous"),
	}
	result, err := Run(t.Context(), fixture.preset, cases, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != 3 || result.Coverage[Skipped] != 3 ||
		result.BrowserFixtures == nil || result.BrowserFixtures.Trials != 0 {
		t.Fatal("gated mandatory browser coverage was omitted or attempted", err)
	}
	for index, trial := range result.Trials {
		code := "browser_action_evidence_unavailable"
		if index < 2 {
			code = "browser_private_input_unavailable"
		}
		if trial.Code != code {
			t.Fatal("missing native prerequisite was not recorded precisely")
		}
	}
}

func TestOwnedBrowserDenialAttemptsTheScenarioWithoutInventingApprovalEvidence(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "browser_read")
	enableAdmissionBrowser(t, fixture)
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{browserCorpusCase(t, "denied")}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != 1 ||
		result.Trials[0].Verdict == Pass || result.Trials[0].Verdict == Skipped ||
		result.BrowserFixtures == nil || result.BrowserFixtures.Trials != 1 {
		t.Fatal("denial was skipped or a final-only stub qualified native cancellation", err, result.Trials)
	}
}

func TestBrowserFixtureFailureInterruptsOnlyTheOwnedGateway(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	enableAdmissionBrowser(t, fixture)
	host, err := bindOwnedBrowserFixture(fixture.preset, fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.close() })
	child, target, err := startOwnedGatewayWithBrowser(t.Context(), fixture.preset, fixture.home, host)
	registerOwnedGatewayCleanup(t, child)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	join := watchBrowserFixture(ctx, target.config, host, child, cancel)
	defer join()
	if err = host.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("fixture loss did not interrupt the scenario")
	}
	select {
	case <-child.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture loss did not stop/reap its owned gateway")
	}
}

func TestBrowserEvidenceCannotReachJudgeOrOfflineGradeFromGenericConversation(t *testing.T) {
	item := browserCorpusCase(t, "read")
	transport := newFakeConversation()
	transport.onSend = func(request protocol.MintClawMessage, _ int) { transport.events <- testResponse(request, 1) }
	provider := &roleTestProvider{mediated: true}
	trial := runDomainConversation(t.Context(), transport, item, uuid.NewString(), "main", "/synthetic/eval",
		conversationRoles{judge: &ModelRole{provider: provider, identity: RoleIdentity{Role: "judge"}}}, nil)
	if trial.EvidenceEligible || trial.Verdict == Pass || provider.calls != 0 {
		t.Fatal("unqualified browser evidence reached a judge")
	}
	trial.EvidenceEligible = true
	if gradeEligible(trial, item, TrialSummary{}, Preset{}, "/synthetic/eval") {
		t.Fatal("offline grading admitted a forged browser eligibility flag")
	}
}

func TestBrowserRunRefusesUnownedAttachmentAndClosesOnPreTaskReportFailure(t *testing.T) {
	for _, mode := range []string{"attach", "report"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "browser_read")
			enableAdmissionBrowser(t, fixture)
			options := RunOptions{Home: fixture.home, OutputParent: fixture.output}
			want := ErrUnsafeTarget
			if mode == "report" {
				options.StartTarget, options.OutputParent, want = true, fixture.workspace, ErrReport
			}
			_, err := Run(t.Context(), fixture.preset, []LoadedCase{browserCorpusCase(t, "read")}, options)
			if !errors.Is(err, want) {
				t.Fatal("unsafe attachment or report destination was admitted", err)
			}
			if mode == "report" {
				data, readErr := os.ReadFile(filepath.Join(fixture.home, ".security", "browser-child-stop.json"))
				var stop browserChildStopEvidence
				if readErr != nil || json.Unmarshal(data, &stop) != nil || stop.Messages != 0 ||
					stop.Status != http.StatusNotFound {
					t.Fatal("report failure sent a task or released the host before child stop", readErr)
				}
			}
			_, profile := browserEvalProfile(fixture.cfg)
			listener, err := net.Listen("tcp", strings.TrimPrefix(profile.AllowedOrigins[0], "http://"))
			if err != nil {
				t.Fatal("refused run leaked an owned listener", err)
			}
			_ = listener.Close()
		})
	}
}

func TestBrowserFixtureHostReceiptCannotHideMissingCleanup(t *testing.T) {
	for _, code := range []string{"closed", "close_failed", "target_exit_unverified", "invented"} {
		receipt := &BrowserFixtureHostEvidence{
			Scope:        "owned_http_fixture_host_v1",
			Code:         code,
			OriginSHA256: digest([]byte("synthetic-origin")),
			Trials:       1,
		}
		if (code != "invented") != receipt.valid() || (code == "closed") != (receipt.verdict() == Pass) {
			t.Fatal("local HTTP receipt lost its bounded cleanup semantics")
		}
	}
}
