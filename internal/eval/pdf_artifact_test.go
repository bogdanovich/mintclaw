package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

// This is a transport/oracle control, not native write or live-case evidence.
func pdfArtifactControl(t *testing.T, data []byte) (*pdfTrial, *pdfArtifactCollector, pdfSettlement, *privateReport) {
	t.Helper()
	prepared := newPDFTrialFixture(t)
	t.Cleanup(func() { _ = prepared.release(true) })
	parent := filepath.Join(prepared.home, "reports")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := newPrivateReport(parent, prepared.home, prepared.workspace, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = report.root.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mintclaw/media/artifact-control" || r.URL.RawQuery != "" ||
			r.Header.Get("Authorization") != "Bearer artifact-control-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port = endpoint.Hostname(), port
	cfg.Channels = config.ChannelsConfig{"mintclaw": {
		Type: config.ChannelMintClaw, Enabled: true, Settings: config.RawNode(`{"token":"artifact-control-token"}`),
	}}
	settled := pdfSettlement{operation: document.WriteOperationRecord{
		ArtifactRef: "media://artifact-control",
		Artifact:    &document.WriteArtifactEvidence{SHA256: digest(data), Size: int64(len(data))},
	}}
	return prepared, &pdfArtifactCollector{config: cfg, retain: report.retainPDFArtifact}, settled, report
}

func TestPDFSettlementGraceWaitsForNativeRecoveryWithoutChangingOperation(t *testing.T) {
	domain, settled := settledPDFControl(t)
	settled.delivery.RecoverySettled = false
	data, err := json.Marshal(settled.delivery)
	path := filepath.Join(domain.prepared.workspace, "state", "outbox", settled.delivery.ID+".json")
	if err != nil || os.WriteFile(path, data, 0o600) != nil {
		t.Fatal("cannot prepare unfinished native receipt")
	}
	if _, err = domain.observeSettlement(); !errors.Is(err, ErrPDFTrial) {
		t.Fatal("unfinished native recovery was admitted")
	}
	journalPath := filepath.Join(
		domain.prepared.home,
		"state",
		"document-writes",
		"journal",
		domain.operationID+".json",
	)
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.Open(domain.prepared.workspace)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, finishErr := box.MarkRecoverySettled(settled.delivery.ID)
			finished <- finishErr
		case <-t.Context().Done():
			finished <- t.Context().Err()
		}
	}()
	actual, waitErr := domain.waitSettlement(t.Context())
	finishErr := <-finished
	after, readErr := os.ReadFile(journalPath)
	if waitErr != nil || finishErr != nil || readErr != nil || !bytes.Equal(before, after) ||
		actual.delivery.ID != settled.delivery.ID || len(actual.delivery.PlatformMessageIDs) != 1 {
		t.Fatal("native recovery grace did not settle, changed the operation, or fabricated another delivery")
	}
}

func pdfArtifactTrial(prepared *pdfTrial) Trial {
	return Trial{
		ID: uuid.NewString(), Verdict: Pass, ExecutionVerdict: Pass,
		PDF: &PDFTrialEvidence{}, Checks: gradeConversation(prepared.item.Case.Checks, nil, Event{}),
	}
}

func TestPDFArtifactRetainsPrivateBytesWithoutOracleOrQualification(t *testing.T) {
	data := []byte("MC_EVAL_private_binary_control")
	prepared, collector, settled, report := pdfArtifactControl(t, data)
	prepared.item.Case.Driver.Facts["contact-reference"] = string(data)
	trial := pdfArtifactTrial(prepared)
	collector.collect(t.Context(), prepared, settled, &trial)
	evidence := trial.PDF.Artifact
	if evidence == nil || evidence.Verdict != Inconclusive || evidence.Code != "oracle_unavailable" ||
		evidence.Oracle != nil || evidence.File == "" || trial.Verdict != Inconclusive || trial.EvidenceEligible {
		t.Fatal("downloaded metadata/bytes became independent PDF or privacy qualification")
	}
	actual, err := os.ReadFile(filepath.Join(report.dir, evidence.File))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("private artifact was not retained exactly")
	}
	info, err := report.root.Stat(evidence.File)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("binary artifact is not private")
	}
	encoded, err := json.Marshal(trial)
	if err != nil || bytes.Contains(encoded, data) {
		t.Fatal("private artifact bytes reached JSON evidence")
	}
	for _, check := range trial.Checks {
		if check.Verdict == Pass {
			t.Fatal("unavailable oracle made a required check pass")
		}
	}
}

func TestPDFArtifactMismatchOrRetentionFailureCannotPass(t *testing.T) {
	for _, mode := range []string{"digest", "retention", "retention_name"} {
		t.Run(mode, func(t *testing.T) {
			prepared, collector, settled, _ := pdfArtifactControl(t, []byte("artifact-control"))
			calls := 0
			collector.retain = func(id string, _ []byte) (string, error) {
				calls++
				if mode == "retention_name" {
					return "foreign-" + id + ".pdf", nil
				}
				return "", errors.New("private-retention-diagnostic")
			}
			if mode == "digest" {
				settled.operation.Artifact.SHA256 = digest([]byte("different-control"))
			}
			trial := pdfArtifactTrial(prepared)
			collector.collect(t.Context(), prepared, settled, &trial)
			want := Error
			if mode == "digest" {
				want = Fail
			}
			if trial.Verdict != want || trial.ExecutionVerdict != want || trial.EvidenceEligible ||
				trial.PDF.Artifact.File != "" || trial.PDF.Artifact.Oracle != nil || mode == "digest" && calls != 0 {
				t.Fatal("failed artifact was retained, qualified, or allowed past a hard mismatch")
			}
			encoded, _ := json.Marshal(trial)
			if bytes.Contains(encoded, []byte("private-retention-diagnostic")) {
				t.Fatal("retention diagnostics leaked into a report")
			}
		})
	}
}

func TestPDFArtifactFailureCannotHideEarlierHardFailure(t *testing.T) {
	trial := Trial{Verdict: Fail, Code: "earlier_fail", ExecutionVerdict: Fail, ExecutionCode: "earlier_fail"}
	failPDFArtifact(&trial, Error, "retention_failed")
	if trial.Verdict != Fail || trial.ExecutionVerdict != Fail || trial.Code != "earlier_fail" ||
		trial.ExecutionCode != "earlier_fail" {
		t.Fatal("later artifact problem hid a hard failure")
	}
}

func TestPDFArtifactManualCollectorQualification(t *testing.T) {
	python := os.Getenv("MINTCLAW_EVAL_PDF_ORACLE_PYTHON")
	if python == "" {
		t.Skip("manual pinned PDF oracle not admitted")
	}
	source, err := os.ReadFile("../../evals/fixtures/pdf/loan.pdf")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unchanged", "fake_fill"} {
		t.Run(mode, func(t *testing.T) {
			prepared, collector, settled, _ := pdfArtifactControl(t, source)
			collector.python = python
			if mode == "unchanged" {
				for index := range prepared.item.Case.PDF.Fields {
					field := &prepared.item.Case.PDF.Fields[index]
					field.Preserve, field.Fact, field.Value = true, "", ""
				}
			}
			trial := pdfArtifactTrial(prepared)
			collector.collect(t.Context(), prepared, settled, &trial)
			evidence := trial.PDF.Artifact
			if evidence.Oracle == nil || evidence.File == "" || trial.EvidenceEligible ||
				!validDigest(evidence.Oracle.OracleSHA256) {
				t.Fatal("independent artifact control was missing or mistaken for full qualification")
			}
			want := Pass
			if mode == "fake_fill" {
				want = Fail
			}
			if evidence.Verdict != want || mode == "fake_fill" && trial.Verdict != Fail {
				t.Fatal("independent artifact control did not distinguish preserved from falsely claimed fill")
			}
			for index, check := range prepared.item.Case.Checks {
				if check.Kind == "pdf_fields" || check.Kind == "pdf_structure" {
					if trial.Checks[index].Verdict != want {
						t.Fatal("field/structure verdict did not use the independent oracle")
					}
				} else if trial.Checks[index].Verdict != Inconclusive {
					t.Fatal("artifact alone qualified privacy or unique delivery")
				}
			}
		})
	}
}
