package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/memory"
)

type questionAnswerHistory struct {
	metadata string
	history  []string
}

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
			var snapshot questionAnswerHistory
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

// Unlike registry/outbox records, native history is normally 0644 beneath the
// admitted private home. Anchor every directory and reject links/writable paths
// instead of chmodding runtime files or constructing a recovering session store.
func (domain *questionConversation) readAnswerHistory(ctx context.Context) (questionAnswerHistory, error) {
	home, err := privateHome(domain.home)
	if err != nil || !pathWithin(home, domain.workspace) {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	relative, err := filepath.Rel(home, filepath.Join(domain.workspace, "sessions"))
	if err != nil {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	defer func() { _ = root.Close() }()
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, component)
		info, statErr := root.Lstat(current)
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return questionAnswerHistory{}, errQuestionEvidence
		}
	}
	store, err := root.OpenRoot(relative)
	if err != nil {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	defer func() { _ = store.Close() }()
	historyName, metaName, err := memory.JSONLSessionFiles(domain.waiting.Route.SessionKey)
	if err != nil {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	read := func(name string, maximum int64, jsonLines bool) ([]byte, error) {
		info, statErr := store.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return nil, errQuestionEvidence
		}
		data, readErr := readBoundedFile(store, name, maximum)
		if readErr == nil && jsonLines {
			scan := privateContentScan{facts: domain.facts, filter: domain.filter, tokens: 32768}
			if scan.jsonLines(data) {
				readErr = errNativeEvidence
				if !scan.uncertain {
					readErr = ErrPrivateEvidence
				}
			}
		} else if readErr == nil {
			readErr = inspectNativeJSON(data, domain.facts, domain.filter)
		}
		if readErr != nil {
			clear(data)
			return nil, readErr
		}
		return data, nil
	}
	before, err := read(metaName, MaxManifestBytes, false)
	if err != nil {
		return questionAnswerHistory{}, err
	}
	defer clear(before)
	history, err := read(historyName, int64(memory.MaxJSONLInspectionBytes-len(before)), true)
	if err != nil {
		return questionAnswerHistory{}, err
	}
	defer clear(history)
	after, err := read(metaName, MaxManifestBytes, false)
	if err != nil {
		return questionAnswerHistory{}, err
	}
	defer clear(after)
	if !bytes.Equal(before, after) {
		return questionAnswerHistory{}, errQuestionEvidence
	}
	if _, err = memory.InspectJSONLSnapshot(ctx, domain.waiting.Route.SessionKey, before, history); err != nil {
		return questionAnswerHistory{}, err
	}
	return questionAnswerHistory{metadata: string(before), history: strings.Split(string(history), "\n")}, nil
}
