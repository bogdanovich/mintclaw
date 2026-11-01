package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
)

func ownedPDFWireCase(t *testing.T, fixture *admissionFixture) LoadedCase {
	t.Helper()
	enableAdmissionPDF(t, fixture)
	settings, err := gatewayclient.ChannelSettings(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(fixture.home, ".security", "channel-key"),
		[]byte(settings.Token.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	settings.SetToken("file://.security/channel-key")
	snapshot, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg = snapshot.Config
	if _, err := document.NewWriteJournal(
		filepath.Join(fixture.home, "state", "document-writes", "journal"),
	); err != nil {
		t.Fatal(err)
	}
	corpus, err := LoadCorpus("../../evals")
	if err != nil {
		t.Fatal(err)
	}
	items, err := Select(corpus, Selection{IDs: []string{"pdf.loan.fill"}})
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	item.Case.Driver.Mode, item.Case.Driver.Persona = "scripted", ""
	item.Case.Driver.Steps = []Step{{
		Action: Action{Kind: "finish", Text: "End this synthetic wire control."}, WaitFor: Expectation{Kind: "final"},
	}}
	item.Case.Rubric = nil
	return item
}

func TestOwnedRunObservesRequestlessMediaWithoutInventingNativeSettlement(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	item := ownedPDFWireCase(t, fixture)
	writeAdmissionJSON(t, filepath.Join(fixture.home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: "normal", ParentPID: os.Getpid(), UncorrelatedMedia: true,
	})
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 20 * time.Second,
		IncludePending: true,
	})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || len(result.Trials) != 1 {
		t.Fatal("requestless media control did not complete", err)
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, result.Trials[0].File))
	var trial Trial
	if err != nil || json.Unmarshal(data, &trial) != nil || trial.PDF == nil || trial.PDF.Wire == nil ||
		trial.PDF.Wire.ObservedMessages != 1 || !trial.PDF.Wire.Complete ||
		trial.PDF.Wire.Code != "wire_media_unavailable" || trial.PDF.Settlement != nil || trial.Verdict == Pass ||
		trial.EvidenceEligible {
		t.Fatal("media without request ID was lost or substituted for native settlement", err, trial.PDF)
	}
}

func TestOwnedRunRejectsAnEarlierTrialFactInALaterIgnoredFrame(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	item := ownedPDFWireCase(t, fixture)
	fact := "MC_EVAL_PREVIOUS_TRIAL_WIRE_CONTROL"
	item.Case.Driver.Facts["contact-reference"] = fact
	writeAdmissionJSON(t, filepath.Join(fixture.home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: "normal", ParentPID: os.Getpid(), CrossTrialValue: fact,
	})
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{item, loadedTestCase(t, "general.after-private-pdf")},
		RunOptions{
			Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 20 * time.Second,
			IncludePending: true,
		})
	if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Fail || len(result.Trials) != 2 ||
		result.Trials[1].Verdict != Fail || result.Trials[1].Code != "private_wire_evidence" ||
		result.PrivateSurfaces == nil || result.PrivateSurfaces.LeakObserved {
		t.Fatal("prior trial canary in a discarded pong was missed or confused with a file leak",
			err, result.Trials, result.PrivateSurfaces)
	}
	for _, name := range []string{"run.json", result.Trials[0].File, result.Trials[1].File} {
		data, readErr := os.ReadFile(filepath.Join(result.ReportDir, name))
		if readErr != nil || strings.Contains(string(data), fact) {
			t.Fatal("cross-trial private frame reached retained reports", readErr)
		}
	}
}
