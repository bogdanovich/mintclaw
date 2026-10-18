package bus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// InboundAckReceipt proves that this process durably removed a validated native
// source record. Its zero value conveys no such proof. It does not prove that
// the source's turn or any domain-owned work has finished.
type InboundAckReceipt struct {
	spoolDir string
	sourceID string
}

// InboundSpoolPath is the gateway's instance-owned inbound source namespace.
func InboundSpoolPath(instanceRoot string) string {
	return filepath.Join(instanceRoot, "state", "ingress-spool", "inbound")
}

// Acknowledges checks the receipt against both the source identity and the
// actual spool belonging to the supplied instance. Receipts cannot be built
// from source IDs or from a successful acknowledgement without a native record.
func (r InboundAckReceipt) Acknowledges(instanceRoot, sourceID string) bool {
	if r.spoolDir == "" || r.sourceID == "" || sourceID != r.sourceID || strings.TrimSpace(instanceRoot) == "" {
		return false
	}
	dir, err := filepath.Abs(InboundSpoolPath(instanceRoot))
	if err != nil {
		return false
	}
	dir, err = filepath.EvalSymlinks(dir)
	return err == nil && dir == r.spoolDir
}

func (s *InboundSpool) acknowledge(id string) (InboundAckReceipt, error) {
	if s == nil || id == "" {
		return InboundAckReceipt{}, nil
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) || filepath.Base(id) != id {
		return InboundAckReceipt{}, errors.New("invalid inbound source identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for _, path := range []string{s.processingPath(id), s.pendingPath(id)} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return InboundAckReceipt{}, err
		}
		if !info.Mode().IsRegular() {
			return InboundAckReceipt{}, errors.New("inbound acknowledgement requires a regular source record")
		}
		if found {
			return InboundAckReceipt{}, errors.New("inbound acknowledgement has ambiguous source records")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return InboundAckReceipt{}, err
		}
		var rec spooledInboundRecord
		if err = json.Unmarshal(data, &rec); err != nil {
			return InboundAckReceipt{}, errors.New("invalid inbound acknowledgement source record")
		}
		if err = validateInboundSpoolVersion(rec); err != nil {
			return InboundAckReceipt{}, err
		}
		if err = validateCurrentInboundSpoolContext(rec.Message.Context); err != nil {
			return InboundAckReceipt{}, err
		}
		if rec.ID != id || rec.Message.SpoolID != "" && rec.Message.SpoolID != id {
			return InboundAckReceipt{}, errors.New("inbound acknowledgement source identity mismatch")
		}
		found = true
	}
	if err := s.ackLocked(id); err != nil {
		return InboundAckReceipt{}, err
	}
	if !found {
		// A retry can reconfirm namespace durability, but absence alone does
		// not establish what source was removed or who completed its work.
		return InboundAckReceipt{}, nil
	}
	return InboundAckReceipt{spoolDir: s.dir, sourceID: id}, nil
}
