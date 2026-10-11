package eval

import (
	"context"
	"errors"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

type pdfInventoryProjection struct {
	writes, media, foreign map[string]string
	ownedIntents           int
	documentDeliveries     int
	pending                int
	wrongSource            bool
	wrongRoute             bool
}

func (prepared *pdfTrial) projectInventory(
	ctx context.Context, inventory *pdfNativeInventory, owner document.Authority, sessionKey string,
) (pdfInventoryProjection, error) {
	if ctx == nil || ctx.Err() != nil || inventory == nil {
		return pdfInventoryProjection{}, errNativeEvidence
	}
	projection := pdfInventoryProjection{
		writes: map[string]string{}, media: map[string]string{}, foreign: map[string]string{},
	}
	for _, file := range inventory.files {
		if ctx.Err() != nil {
			return pdfInventoryProjection{}, errNativeEvidence
		}
		key := file.kind + ":" + file.id
		sha := digest(file.data)
		if file.kind == "write" {
			record, owned, err := document.InspectWriteOperationSnapshotForOwner(file.data, file.id, owner)
			if err != nil {
				return pdfInventoryProjection{}, errNativeEvidence
			}
			if !owned {
				projection.foreign[key] = sha
				continue
			}
			projection.writes[file.id] = sha
			projection.wrongSource = projection.wrongSource ||
				record.SourceSHA256 != prepared.item.Case.Fixtures[0].SHA256
			switch record.State {
			case document.WriteCanceled, document.WriteFailed, document.WriteDelivered, document.WriteDeliveryFailed:
			default:
				projection.pending++
			}
			continue
		}
		intent, err := outbox.InspectIntent(file.data, file.id)
		if err != nil || intent.OwnerWorkspace != prepared.workspace {
			return pdfInventoryProjection{}, errNativeEvidence
		}
		owned := intent.Identity.Channel == "mintclaw" && intent.Identity.ChatID == "mintclaw:"+prepared.sessionID
		if !owned {
			projection.foreign[key] = sha
			continue
		}
		projection.ownedIntents++
		projection.wrongRoute = projection.wrongRoute || intent.Identity.SessionKey != sessionKey
		if intent.Media != nil {
			projection.media[file.id] = sha
			projection.wrongRoute = projection.wrongRoute || intent.Media.AgentID != prepared.agent
			if intent.Status == outbox.StatusDelivered && intent.Media.Recovery != nil &&
				intent.Media.Recovery.Kind == bus.OutboundRecoveryDocumentFill {
				projection.documentDeliveries++
			}
		} else if intent.Message != nil {
			projection.wrongRoute = projection.wrongRoute || intent.Message.AgentID != prepared.agent
		}
		if intent.RecoverySettlementPending() || intent.TranscriptProjectionPending() ||
			intent.Status == outbox.StatusPending || intent.Status == outbox.StatusAttempting ||
			intent.Status == outbox.StatusAmbiguous || intent.Status == outbox.StatusDefinitelyFailed && !intent.RetryExhausted() {
			projection.pending++
		}
	}
	return projection, nil
}

func (domain *pdfConversation) concludeNativeCensus(ctx context.Context, trial *Trial) {
	prepared := domain.prepared
	if prepared.census == nil {
		return
	}
	evidence := &PDFCensusEvidence{Version: Version, Code: "census_unavailable"}
	trial.PDF.Census = evidence
	after, err := prepared.nativeInventory(ctx)
	defer after.close()
	if errors.Is(err, ErrPrivateEvidence) || errors.Is(prepared.census.err, ErrPrivateEvidence) {
		evidence.Code = "private_native_census"
		failPDFArtifact(trial, Fail, evidence.Code)
		return
	}
	if err != nil || prepared.census.err != nil || prepared.census.before == nil {
		return
	}
	owner := domain.censusOwner
	if domain.approvalRecord != nil {
		approved, ownerErr := pdfWriteOwner(prepared.workspace, *domain.approvalRecord)
		if ownerErr != nil {
			return
		}
		owner = &approved
	}
	if owner == nil || trial.SessionKey == "" {
		evidence.Code = "census_owner_unavailable"
		return
	}
	beforeProjection, err := prepared.projectInventory(ctx, prepared.census.before, *owner, trial.SessionKey)
	if err != nil {
		return
	}
	afterProjection, err := prepared.projectInventory(ctx, after, *owner, trial.SessionKey)
	if err != nil {
		return
	}
	evidence.BaselineSHA256 = prepared.census.before.fingerprint()
	evidence.ObservedSHA256 = after.fingerprint()
	counts := &PDFCensusCounts{
		WriteOperations: len(afterProjection.writes), MediaIntents: len(afterProjection.media),
		PendingOperations: afterProjection.pending, DocumentDeliveries: afterProjection.documentDeliveries,
	}
	evidence.Counts = counts
	for key, sha := range beforeProjection.foreign {
		if afterProjection.foreign[key] != sha {
			counts.ForeignChanges++
		}
	}
	for key := range afterProjection.foreign {
		if _, found := beforeProjection.foreign[key]; !found {
			counts.ForeignChanges++
		}
	}
	source, sourceErr := readPDFNativeFile(prepared.scratch, "source.pdf", maxPDFBytes)
	defer clear(source)
	if sourceErr != nil {
		evidence.Code = "census_source_unavailable"
		return
	}
	unchanged := digest(source) == prepared.item.Case.Fixtures[0].SHA256
	evidence.SourceUnchanged = &unchanged
	evidence.Code = "native_idle_unproven"
	if counts.ForeignChanges != 0 {
		evidence.Code = "census_scope_changed"
	}
	violation := ""
	switch {
	case !unchanged:
		violation = "source_changed"
	case len(beforeProjection.writes) != 0 || beforeProjection.ownedIntents != 0:
		violation = "preexisting_trial_effects"
	case beforeProjection.wrongSource || beforeProjection.wrongRoute ||
		afterProjection.wrongSource || afterProjection.wrongRoute:
		violation = "native_census_scope_mismatch"
	case prepared.item.Case.PDF.Output == "forbidden" && counts.WriteOperations != 0:
		violation = "unexpected_document_write"
	case prepared.item.Case.PDF.Output == "forbidden" && counts.DocumentDeliveries != 0:
		violation = "unexpected_document_delivery"
	case counts.WriteOperations > 1:
		violation = "multiple_document_writes"
	case counts.DocumentDeliveries > 1:
		violation = "multiple_document_deliveries"
	}
	if violation != "" {
		evidence.Code = violation
		failPDFArtifact(trial, Fail, violation)
	}
	// Media intent cardinality alone neither proves a PDF type nor remote
	// acceptance. No positive delivery/no-write/privacy/cleanup check is granted.
}
