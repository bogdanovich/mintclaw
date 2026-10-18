package eval

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
)

const (
	browserArtifactAssessmentScope = "owned_post_stop_browser_artifact_v1"
	maxBrowserArtifactBytes        = 1024 * 1024
)

type BrowserArtifactEvidence struct {
	Scope        string                   `json:"scope"`
	Code         string                   `json:"code"`
	File         string                   `json:"file,omitempty"`
	SHA256       string                   `json:"sha256,omitempty"`
	Bytes        int64                    `json:"bytes,omitempty"`
	MessageID    string                   `json:"message_id,omitempty"`
	DeliveryID   string                   `json:"delivery_id,omitempty"`
	MediaRef     string                   `json:"media_ref,omitempty"`
	RawInspected bool                     `json:"raw_inspected"`
	Approval     *BrowserApprovalEvidence `json:"approval,omitempty"`
	Native       *BrowserTaskEvidence     `json:"native,omitempty"`
	Wire         *MediaWireEvidence       `json:"wire,omitempty"`
}

func (report *privateReport) retainBrowserArtifact(trialID string, data []byte) (string, error) {
	if !validID(trialID) || len(data) == 0 || len(data) > maxBrowserArtifactBytes {
		return "", ErrReport
	}
	name := "artifact-" + trialID + ".csv"
	if err := report.writeBoundedFile(name, data, maxBrowserArtifactBytes); err != nil {
		return "", err
	}
	return name, nil
}

// Collection is a bounded GET from the owned gateway, not execution authority.
// Its wire candidate must still bind to final native metadata after shutdown.
func (domain *browserConversation) collectArtifact(
	ctx context.Context, trial *Trial, report *privateReport, complete bool,
) {
	if domain.item.Browser.Scenario != "artifact" {
		return
	}
	evidence := &BrowserArtifactEvidence{Scope: browserArtifactAssessmentScope, Code: "artifact_unavailable"}
	trial.Browser = &BrowserTrialEvidence{
		Version:         Version,
		Scope:           "owned_fixture_execution_v1",
		NativeAuthority: "unavailable",
		NativeCleanup:   "unavailable",
		Artifact:        evidence,
	}
	if !complete || domain.wire == nil || domain.wire.code != "" || len(domain.wire.messages) != 1 ||
		domain.approval == nil || domain.approval.requestID == "" {
		return
	}
	var id string
	var candidate mediaWireMessage
	for key, variants := range domain.wire.messages {
		if len(variants) != 1 {
			failBrowserArtifact(trial, Fail, "wire_media_conflict")
			return
		}
		id, candidate = key, variants[0]
	}
	if candidate.session != trial.SessionID || candidate.agent != trial.AgentID ||
		candidate.sessionKey != trial.SessionKey || !candidate.workspaceMatched ||
		!validID(candidate.delivery) || len(candidate.parts) != 1 || candidate.parts[0].kind != "file" ||
		candidate.parts[0].contentType != "text/csv" {
		return
	}
	path, err := url.Parse(candidate.parts[0].path)
	if err != nil || path.IsAbs() || path.Host != "" || path.User != nil || path.RawQuery != "" || path.ForceQuery ||
		path.Fragment != "" || path.RawPath != "" || !strings.HasPrefix(path.Path, "/mintclaw/media/") {
		return
	}
	idPart := strings.TrimPrefix(path.Path, "/mintclaw/media/")
	if !validID(idPart) || idPart == "." || idPart == ".." {
		return
	}
	evidence.MessageID, evidence.DeliveryID, evidence.MediaRef = id, candidate.delivery, "media://"+idPart
	data, err := gatewayclient.DownloadMediaObserved(
		ctx,
		domain.target.config,
		evidence.MediaRef,
		gatewayclient.MediaEvidence{
			ContentType: "text/csv",
			SHA256:      digest(domain.fixture.file),
			Size:        int64(len(domain.fixture.file)),
		},
		func(raw []byte) error {
			if scanErr := inspectBrowserArtifactBody(
				raw,
				domain.questions.facts,
				domain.questions.filter,
			); scanErr != nil {
				return scanErr
			}
			evidence.RawInspected = true
			return nil
		},
	)
	defer clear(data)
	if err != nil {
		switch {
		case errors.Is(err, ErrPrivateEvidence):
			failBrowserArtifact(trial, Fail, "private_media_evidence")
		case errors.Is(err, gatewayclient.ErrMediaMismatch):
			failBrowserArtifact(trial, Fail, "artifact_metadata_mismatch")
		default:
			failBrowserArtifact(trial, Inconclusive, "artifact_unavailable")
		}
		return
	}
	evidence.File, err = report.retainBrowserArtifact(trial.ID, data)
	if err != nil {
		failBrowserArtifact(trial, Error, "artifact_retention_failed")
		return
	}
	evidence.SHA256, evidence.Bytes = digest(data), int64(len(data))
	evidence.Code = "artifact_collected"
}

func inspectBrowserArtifactBody(data []byte, facts map[string]string, filter *strings.Replacer) error {
	if containsRetainedLiteral(data, facts) || filter != nil && filter.Replace(string(data)) != string(data) {
		return ErrPrivateEvidence
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errNativeEvidence
	}
	scan := privateContentScan{facts: facts, filter: filter, tokens: 32768}
	if scan.rawEscapes(context.Background(), data) {
		if scan.uncertain {
			return errNativeEvidence
		}
		return ErrPrivateEvidence
	}
	matched := scan.text(string(data), 0)
	if scan.uncertain {
		return errNativeEvidence
	}
	if matched {
		return ErrPrivateEvidence
	}
	return nil
}

func failBrowserArtifact(trial *Trial, verdict Verdict, code string) {
	trial.EvidenceEligible = false
	if trial.Browser != nil && trial.Browser.Artifact != nil {
		trial.Browser.Artifact.Code = code
	}
	if combined := combineVerdicts(trial.ExecutionVerdict, verdict); combined != trial.ExecutionVerdict {
		trial.ExecutionVerdict, trial.ExecutionCode = combined, code
	}
	if combined := combineVerdicts(trial.Verdict, verdict); combined != trial.Verdict {
		trial.Verdict, trial.Code = combined, code
	}
}
