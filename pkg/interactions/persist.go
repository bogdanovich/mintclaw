package interactions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func (r *Registry) availableLocked() error {
	if r.loadErr != nil {
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, r.loadErr)
	}
	return nil
}

func (r *Registry) lockAndReloadLocked() (func(), error) {
	if r.storePath == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(r.storePath), 0o700); err != nil {
		return nil, fmt.Errorf("create interaction store directory: %w", err)
	}
	release, err := acquireStoreFileLock(r.storePath + ".lock")
	if err != nil {
		return nil, err
	}
	before := r.snapshotLocked()
	r.records = make(map[string]Record)
	r.events = nil
	r.commitSequence = 0
	if err := r.load(); err != nil {
		r.restoreSnapshotLocked(before)
		release()
		return nil, fmt.Errorf("%w: reload under lock: %w", ErrStoreUnavailable, err)
	}
	return release, nil
}

func (r *Registry) load() error {
	data, err := os.ReadFile(r.storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("unsupported interaction snapshot schema %q", snapshot.SchemaVersion)
	}
	activeSessions := make(map[string]string)
	activeTasks := make(map[string]string)
	activeShortIDs := make(map[string]string)
	answerMessages := make(map[answerMessageIdentity]string)
	for _, rec := range snapshot.Records {
		if err := validateStoredRecord(rec); err != nil {
			return err
		}
		if _, duplicate := r.records[rec.ID]; duplicate {
			return fmt.Errorf("duplicate interaction record %q", rec.ID)
		}
		if !isTerminal(rec.Status) {
			if existing := activeSessions[rec.Route.SessionKey]; existing != "" {
				return fmt.Errorf("active interactions %q and %q share session", existing, rec.ID)
			}
			if existing := activeShortIDs[rec.ShortID]; existing != "" {
				return fmt.Errorf("active interactions %q and %q share short id", existing, rec.ID)
			}
			taskID := strings.TrimSpace(rec.Origin.TaskID)
			if existing := activeTasks[taskID]; taskID != "" && existing != "" {
				return fmt.Errorf("active interactions %q and %q share task %q", existing, rec.ID, taskID)
			}
			activeSessions[rec.Route.SessionKey] = rec.ID
			activeShortIDs[rec.ShortID] = rec.ID
			if taskID != "" {
				activeTasks[taskID] = rec.ID
			}
		}
		if rec.Answer != nil && rec.Answer.MessageID != "" {
			identity := scopedAnswerMessageIdentity(rec.Route, rec.Answer.MessageID)
			if existing := answerMessages[identity]; existing != "" {
				return fmt.Errorf("interactions %q and %q share answer message", existing, rec.ID)
			}
			answerMessages[identity] = rec.ID
		}
		r.records[rec.ID] = cloneRecord(rec)
	}
	eventSequences := make(map[string]int64)
	eventSeen := make(map[string]bool)
	commitSequence := uint64(0)
	for _, event := range snapshot.Events {
		if event.SchemaVersion != EventSchemaVersion || event.InteractionID == "" ||
			event.Type == "" {
			return fmt.Errorf("invalid interaction event %q", event.EventID)
		}
		rec, exists := r.records[event.InteractionID]
		if !exists || event.Sequence <= 0 || event.Sequence > rec.LastEventSeq ||
			event.Revision <= 0 || event.Revision > rec.Revision {
			return fmt.Errorf("invalid interaction event sequence %q", event.EventID)
		}
		if eventSeen[event.InteractionID] &&
			event.Sequence != eventSequences[event.InteractionID]+1 {
			return fmt.Errorf("invalid interaction event sequence %q", event.EventID)
		}
		eventSeen[event.InteractionID] = true
		eventSequences[event.InteractionID] = event.Sequence
		if event.CommitSequence == 0 {
			event.CommitSequence = commitSequence + 1
		}
		if event.CommitSequence <= commitSequence {
			return fmt.Errorf("invalid interaction commit sequence %q", event.EventID)
		}
		commitSequence = event.CommitSequence
		r.events = append(r.events, event)
	}
	if snapshot.CommitSequence != 0 && snapshot.CommitSequence < commitSequence {
		return fmt.Errorf("invalid interaction snapshot commit sequence")
	}
	r.commitSequence = snapshot.CommitSequence
	if r.commitSequence == 0 {
		r.commitSequence = commitSequence
	}
	return nil
}

func (r *Registry) saveLocked() error {
	if r.storePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(r.snapshotLocked(), "", "  ")
	if err != nil {
		return err
	}
	if len(data) > r.options.MaxSnapshotBytes {
		return fmt.Errorf(
			"%w: %d > %d",
			ErrSnapshotOverBudget,
			len(data),
			r.options.MaxSnapshotBytes,
		)
	}
	return fileutil.WriteFileAtomic(r.storePath, data, 0o600)
}

func (r *Registry) snapshotLocked() Snapshot {
	records := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		records = append(records, cloneRecord(rec))
	}
	sortRecords(records)
	return Snapshot{
		SchemaVersion:  SnapshotSchemaVersion,
		CommitSequence: r.commitSequence,
		Records:        records,
		Events:         cloneEvents(r.events),
	}
}

func (r *Registry) restoreSnapshotLocked(snapshot Snapshot) {
	r.records = make(map[string]Record, len(snapshot.Records))
	for _, rec := range snapshot.Records {
		r.records[rec.ID] = cloneRecord(rec)
	}
	r.events = cloneEvents(snapshot.Events)
	r.commitSequence = snapshot.CommitSequence
}

func (r *Registry) snapshotSizeLocked() int {
	data, _ := json.MarshalIndent(r.snapshotLocked(), "", "  ")
	return len(data)
}
