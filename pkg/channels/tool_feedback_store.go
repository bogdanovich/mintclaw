package channels

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	toolFeedbackCarrierStoreVersion    = 1
	toolFeedbackCarrierStoreMaxRecords = 4096
	toolFeedbackCarrierValueMaxBytes   = 4096
)

type toolFeedbackCarrierRecord struct {
	Version            int    `json:"version"`
	ID                 string `json:"id"`
	CoordinatorKey     string `json:"coordinator_key"`
	Channel            string `json:"channel"`
	ChatID             string `json:"chat_id"`
	MessageID          string `json:"message_id"`
	CreatedAtUnixMilli int64  `json:"created_at_unix_milli"`
}

type toolFeedbackCarrierSnapshot struct {
	Version int                         `json:"version"`
	Records []toolFeedbackCarrierRecord `json:"records"`
}

type toolFeedbackCarrierStore struct {
	mu          sync.Mutex
	path        string
	records     map[string]toolFeedbackCarrierRecord
	writeAtomic func(string, []byte, os.FileMode) error
}

func toolFeedbackCarrierStorePath(instanceRoot string) string {
	instanceRoot = strings.TrimSpace(instanceRoot)
	if instanceRoot == "" {
		return ""
	}
	return filepath.Join(instanceRoot, "state", "tool_feedback_carriers.json")
}

func openToolFeedbackCarrierStore(instanceRoot string) (*toolFeedbackCarrierStore, error) {
	instanceRoot = strings.TrimSpace(instanceRoot)
	if instanceRoot == "" {
		return nil, errors.New("tool feedback persistence root is required")
	}
	if err := fileutil.MkdirAllDurable(instanceRoot, "state", 0o700); err != nil {
		return nil, fmt.Errorf("create tool feedback state directory: %w", err)
	}
	store := &toolFeedbackCarrierStore{
		path:        toolFeedbackCarrierStorePath(instanceRoot),
		records:     make(map[string]toolFeedbackCarrierRecord),
		writeAtomic: fileutil.WriteFileAtomic,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *toolFeedbackCarrierStore) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tool feedback carrier store: %w", err)
	}
	var snapshot toolFeedbackCarrierSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("decode tool feedback carrier store: %w", err)
	}
	if snapshot.Version != toolFeedbackCarrierStoreVersion ||
		len(snapshot.Records) > toolFeedbackCarrierStoreMaxRecords {
		return errors.New("invalid tool feedback carrier store")
	}
	for _, record := range snapshot.Records {
		if err := validateToolFeedbackCarrierRecord(record); err != nil {
			return fmt.Errorf("validate tool feedback carrier store: %w", err)
		}
		if _, exists := s.records[record.ID]; exists {
			return errors.New("tool feedback carrier store contains a duplicate record")
		}
		s.records[record.ID] = record
	}
	return nil
}

func (s *toolFeedbackCarrierStore) Record(
	coordinatorKey string,
	channel string,
	chatID string,
	messageID string,
	now time.Time,
) (string, error) {
	if s == nil {
		return "", nil
	}
	record := toolFeedbackCarrierRecord{
		Version:            toolFeedbackCarrierStoreVersion,
		CoordinatorKey:     strings.TrimSpace(coordinatorKey),
		Channel:            strings.TrimSpace(channel),
		ChatID:             strings.TrimSpace(chatID),
		MessageID:          strings.TrimSpace(messageID),
		CreatedAtUnixMilli: now.UTC().UnixMilli(),
	}
	record.ID = toolFeedbackCarrierID(record)
	if err := validateToolFeedbackCarrierRecord(record); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.records[record.ID]; exists {
		if sameToolFeedbackCarrier(existing, record) {
			return record.ID, nil
		}
		return "", errors.New("tool feedback carrier identity conflict")
	}
	if len(s.records) >= toolFeedbackCarrierStoreMaxRecords {
		return "", errors.New("tool feedback carrier store is full")
	}
	next := cloneToolFeedbackCarrierRecords(s.records)
	next[record.ID] = record
	return record.ID, s.commitLocked(next)
}

func (s *toolFeedbackCarrierStore) Delete(id string) error {
	if s == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[id]; !exists {
		return nil
	}
	next := cloneToolFeedbackCarrierRecords(s.records)
	delete(next, id)
	return s.commitLocked(next)
}

func (s *toolFeedbackCarrierStore) Snapshot() []toolFeedbackCarrierRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]toolFeedbackCarrierRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records
}

func (s *toolFeedbackCarrierStore) commitLocked(next map[string]toolFeedbackCarrierRecord) error {
	records := make([]toolFeedbackCarrierRecord, 0, len(next))
	for _, record := range next {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	raw, err := json.MarshalIndent(toolFeedbackCarrierSnapshot{
		Version: toolFeedbackCarrierStoreVersion,
		Records: records,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tool feedback carrier store: %w", err)
	}
	err = s.writeAtomic(s.path, raw, 0o600)
	if err == nil || fileutil.IsCommittedWriteError(err) {
		s.records = next
	}
	return err
}

func validateToolFeedbackCarrierRecord(record toolFeedbackCarrierRecord) error {
	values := []string{
		record.ID, record.CoordinatorKey, record.Channel, record.ChatID, record.MessageID,
	}
	if record.Version != toolFeedbackCarrierStoreVersion || record.CreatedAtUnixMilli <= 0 {
		return errors.New("invalid tool feedback carrier record")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > toolFeedbackCarrierValueMaxBytes {
			return errors.New("invalid tool feedback carrier record")
		}
	}
	if record.ID != toolFeedbackCarrierID(record) {
		return errors.New("tool feedback carrier record identity mismatch")
	}
	return nil
}

func toolFeedbackCarrierID(record toolFeedbackCarrierRecord) string {
	payload, _ := json.Marshal(struct {
		CoordinatorKey string `json:"coordinator_key"`
		Channel        string `json:"channel"`
		ChatID         string `json:"chat_id"`
		MessageID      string `json:"message_id"`
	}{
		CoordinatorKey: strings.TrimSpace(record.CoordinatorKey),
		Channel:        strings.TrimSpace(record.Channel),
		ChatID:         strings.TrimSpace(record.ChatID),
		MessageID:      strings.TrimSpace(record.MessageID),
	})
	digest := sha256.Sum256(payload)
	return "tfc_" + hex.EncodeToString(digest[:16])
}

func sameToolFeedbackCarrier(left, right toolFeedbackCarrierRecord) bool {
	return left.ID == right.ID && left.CoordinatorKey == right.CoordinatorKey &&
		left.Channel == right.Channel && left.ChatID == right.ChatID &&
		left.MessageID == right.MessageID
}

func cloneToolFeedbackCarrierRecords(
	records map[string]toolFeedbackCarrierRecord,
) map[string]toolFeedbackCarrierRecord {
	cloned := make(map[string]toolFeedbackCarrierRecord, len(records))
	for id, record := range records {
		cloned[id] = record
	}
	return cloned
}
