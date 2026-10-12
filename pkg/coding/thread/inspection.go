package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/memory"
)

// Inspection is a content-free exact-ID view of durable coding-thread state.
// It omits title, preview, paths, remote URLs, transcript content, session
// scope, and client session identities.
type Inspection struct {
	ThreadID                 string                  `json:"thread_id"`
	Status                   Status                  `json:"status"`
	CreatedAt                time.Time               `json:"created_at"`
	UpdatedAt                time.Time               `json:"updated_at"`
	ProjectHash              string                  `json:"project_hash"`
	GitCommit                string                  `json:"git_commit,omitempty"`
	HasParent                bool                    `json:"has_parent"`
	HasForkPoint             bool                    `json:"has_fork_point"`
	LastCompactionRevision   uint64                  `json:"last_compaction_revision,omitempty"`
	TranscriptAvailability   string                  `json:"transcript_availability"`
	HistoryRevision          uint64                  `json:"history_revision,omitempty"`
	HistoryCount             int                     `json:"history_count,omitempty"`
	HistoryVisibleCount      int                     `json:"history_visible_count,omitempty"`
	HistoryDirty             bool                    `json:"history_dirty,omitempty"`
	HistoryHasPrevious       bool                    `json:"history_has_previous,omitempty"`
	PresentationAvailability string                  `json:"presentation_availability"`
	Presentation             *PresentationCheckpoint `json:"presentation,omitempty"`
}

// Inspect reads one exact coding thread without reconciling history, taking a
// writer lease, or mutating canonical state.
func (s *Store) Inspect(ctx context.Context, threadID string) (inspection Inspection, resultErr error) {
	if s == nil || ctx == nil {
		return Inspection{}, errors.New("coding thread inspection: store and context are required")
	}
	if err := validateThreadID(threadID); err != nil {
		return Inspection{}, err
	}
	storeRoot, err := openCatalogRoot(s.root)
	if err != nil {
		return Inspection{}, fmt.Errorf("coding thread inspection: open store: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, storeRoot.Close()) }()
	threadsRoot, err := openCatalogChildDirectory(storeRoot, "threads")
	if err != nil {
		return Inspection{}, fmt.Errorf("coding thread inspection: open threads: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, threadsRoot.Close()) }()
	threadRoot, err := openCatalogChildDirectory(threadsRoot, threadID)
	if err != nil {
		return Inspection{}, fmt.Errorf("coding thread inspection: open thread: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, threadRoot.Close()) }()
	metadata, err := loadCatalogMetadataFromDirectory(threadRoot, threadID, openCatalogMetadataFile)
	if err != nil {
		return Inspection{}, fmt.Errorf("coding thread inspection: read metadata: %w", err)
	}
	inspection = Inspection{
		ThreadID:                 threadID,
		Status:                   metadata.Status,
		CreatedAt:                metadata.CreatedAt.UTC(),
		UpdatedAt:                metadata.UpdatedAt.UTC(),
		ProjectHash:              ProjectHash(metadata.Project.ProjectKey),
		GitCommit:                metadata.Project.GitHead,
		HasParent:                metadata.ParentThread != "",
		HasForkPoint:             metadata.Fork != nil,
		TranscriptAvailability:   "not_created",
		PresentationAvailability: "unavailable_by_design",
	}
	if metadata.Compaction != nil {
		inspection.LastCompactionRevision = metadata.Compaction.Revision
	}
	if err = inspectThreadSessionMetadata(ctx, threadRoot, metadata, &inspection); err != nil {
		return Inspection{}, err
	}
	checkpoint, ok, err := s.LoadPresentationCheckpoint(ctx, threadID)
	if err != nil {
		return Inspection{}, err
	}
	if ok {
		inspection.PresentationAvailability = "available"
		inspection.Presentation = &checkpoint
	}
	return inspection, nil
}

func inspectThreadSessionMetadata(
	ctx context.Context,
	threadRoot *catalogDirectory,
	metadata Metadata,
	inspection *Inspection,
) (resultErr error) {
	sessionsRoot, err := openCatalogChildDirectory(threadRoot, "sessions")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coding thread inspection: open sessions: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, sessionsRoot.Close()) }()
	name := "coding_" + metadata.ThreadID + ".meta.json"
	file, err := openCatalogFile(sessionsRoot, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coding thread inspection: open transcript metadata: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("coding thread inspection: stat transcript metadata: %w", err)
	}
	if info.Size() > MaxHistoricalSearchMetadataBytes {
		return errors.New("coding thread inspection: transcript metadata exceeds limit")
	}
	data, pinned, err := readPinnedSearchFile(ctx, file, info.Size())
	if err != nil {
		return fmt.Errorf("coding thread inspection: read transcript metadata: %w", err)
	}
	if err = verifyPinnedSearchFile(sessionsRoot, name, pinned); err != nil {
		return fmt.Errorf("coding thread inspection: revalidate transcript metadata: %w", err)
	}
	sessionMeta, err := memory.DecodeSessionMeta(data)
	if err != nil || sessionMeta.Key != metadata.SessionKey {
		return errors.New("coding thread inspection: transcript metadata is invalid")
	}
	inspection.TranscriptAvailability = "available"
	inspection.HistoryRevision = sessionMeta.HistoryRevision
	inspection.HistoryCount = sessionMeta.Count
	inspection.HistoryVisibleCount = sessionMeta.Count - sessionMeta.Skip
	inspection.HistoryDirty = sessionMeta.HistoryDirty
	inspection.HistoryHasPrevious = sessionMeta.HistoryHasPrevious
	return nil
}
