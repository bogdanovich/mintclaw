package eval

import (
	"context"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

type pdfArtifactCollector struct {
	config *config.Config
	python string
	retain func(string, []byte) (string, error)
}

func (domain *pdfConversation) waitSettlement(ctx context.Context) (pdfSettlement, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		settlement, err := domain.observeSettlement()
		if err == nil || errors.Is(err, ErrPrivateEvidence) {
			return settlement, err
		}
		select {
		case <-ctx.Done():
			return pdfSettlement{}, ErrPDFTrial
		case <-ticker.C:
		}
	}
}

func (collector *pdfArtifactCollector) collect(
	ctx context.Context, prepared *pdfTrial, settled pdfSettlement, trial *Trial,
) {
	evidence := &PDFArtifactEvidence{Verdict: Inconclusive, Code: "artifact_unavailable"}
	trial.PDF.Artifact = evidence
	trial.EvidenceEligible = false
	operation := settled.operation
	if operation.Artifact == nil {
		return
	}
	data, err := gatewayclient.DownloadMedia(ctx, collector.config, operation.ArtifactRef, gatewayclient.MediaEvidence{
		ContentType: "application/pdf", SHA256: operation.Artifact.SHA256, Size: operation.Artifact.Size,
	})
	defer clear(data)
	if err != nil {
		if errors.Is(err, gatewayclient.ErrMediaMismatch) {
			evidence.Verdict, evidence.Code = Fail, "artifact_metadata_mismatch"
		}
		failPDFArtifact(trial, evidence.Verdict, evidence.Code)
		return
	}
	if collector.retain == nil {
		evidence.Verdict, evidence.Code = Error, "artifact_retention_unavailable"
		failPDFArtifact(trial, Error, evidence.Code)
		return
	}
	evidence.File, err = collector.retain(trial.ID, data)
	if err != nil || evidence.File != "artifact-"+trial.ID+".pdf" {
		evidence.File = ""
		evidence.Verdict, evidence.Code = Error, "artifact_retention_failed"
		failPDFArtifact(trial, Error, evidence.Code)
		return
	}
	oracle, err := InspectPDFArtifact(ctx, collector.python, prepared.source, data,
		prepared.item.Case.Fixtures[0].SHA256, prepared.resolvedPDF())
	if err != nil {
		evidence.Code = "oracle_unavailable"
		failPDFArtifact(trial, Inconclusive, evidence.Code)
		return
	}
	evidence.Oracle = &oracle
	evidence.Verdict, evidence.Code = Pass, "matched"
	if oracle.Code != "matched" {
		evidence.Verdict, evidence.Code = Fail, "artifact_oracle_mismatch"
	}
	for index, check := range prepared.item.Case.Checks {
		var matched bool
		switch check.Kind {
		case "pdf_fields":
			matched = oracle.FieldsMatched && oracle.CheckedFields == prepared.item.Case.PDF.FieldCount
		case "pdf_structure":
			matched = oracle.StructureMatched && oracle.AppearanceMatched && oracle.BackgroundMatched &&
				oracle.RenderedPages == prepared.item.Case.PDF.Pages
		default:
			continue
		}
		// The check refers to the retained private artifact and its value-free
		// oracle projection; raw bytes and golden mappings never reach a judge.
		if index < len(trial.Checks) && trial.Checks[index].ID == check.ID {
			trial.Checks[index] = booleanCheck(check.ID, matched, []string{evidence.File})
		}
	}
	if evidence.Verdict == Fail {
		failPDFArtifact(trial, Fail, evidence.Code)
	}
}

func failPDFArtifact(trial *Trial, verdict Verdict, code string) {
	combined := combineVerdicts(trial.Verdict, verdict)
	if combined != trial.Verdict {
		trial.Code = code
	}
	trial.Verdict = combined
	combined = combineVerdicts(trial.ExecutionVerdict, verdict)
	if combined != trial.ExecutionVerdict {
		trial.ExecutionCode = code
	}
	trial.ExecutionVerdict = combined
	trial.EvidenceEligible = false
}
