package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

func (domain *questionConversation) observeAnswer(ctx context.Context, reply Event) error {
	if domain.answerCheckID == "" || reply.AgentID != domain.agent ||
		reply.SessionKey != domain.waiting.Route.SessionKey {
		return errQuestionEvidence
	}
	if domain.resumeFinal.ID == "" {
		if reply.RequestID != domain.answerRequest.ID {
			return errQuestionEvidence
		}
		domain.resumeFinal = reply
	}
	domain.answer = nil
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.record(domain.waiting.ID)
		if err == nil && record.Status == interactions.StatusResolved {
			var snapshot nativeSessionHistory
			snapshot, err = domain.readAnswerHistory(ctx)
			if err == nil {
				waiting, waitingErr := json.Marshal(domain.waiting)
				resolved, resolvedErr := json.Marshal(record)
				if waitingErr == nil && resolvedErr == nil && len(waiting) <= MaxManifestBytes &&
					len(resolved) <= MaxManifestBytes {
					proof := &QuestionAnswerEvidence{
						Version: Version, Workspace: domain.workspace,
						AnswerRequestID: domain.answerRequest.ID, AnswerAt: domain.answerAt.UnixMilli(),
						ObservedAt:         time.Now().UnixMilli(),
						ResumeFinalEventID: domain.resumeFinal.ID, Waiting: string(waiting), Resolved: string(resolved),
						Metadata: snapshot.metadata, History: snapshot.history,
						HistorySHA256:  digest([]byte(strings.Join(snapshot.history, "\n"))),
						MetadataSHA256: digest([]byte(snapshot.metadata)),
					}
					encoded, encodeErr := json.Marshal(proof)
					bounded := encodeErr == nil && len(encoded) <= maxQuestionAnswerEvidenceBytes
					clear(encoded)
					if bounded {
						domain.answer = proof
						return nil
					}
				}
			}
		}
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		select {
		case <-ctx.Done():
			return errQuestionEvidence
		case <-ticker.C:
		}
	}
}

func (domain *questionConversation) readAnswerHistory(ctx context.Context) (nativeSessionHistory, error) {
	return readNativeSessionHistory(ctx, domain.home, domain.workspace, domain.waiting.Route.SessionKey,
		domain.facts, domain.filter)
}
