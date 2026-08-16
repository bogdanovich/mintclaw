package memory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

// ErrHistoryCursorStale reports that a canonical prefix changed after its
// cursor was captured.
var ErrHistoryCursorStale = errors.New("memory: history cursor is stale")

type historyCursorDigest struct {
	hash  hash.Hash
	total int
}

func newHistoryCursorDigest() *historyCursorDigest {
	return &historyCursorDigest{hash: sha256.New()}
}

func (d *historyCursorDigest) add(message providers.Message) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("memory: encode history cursor message: %w", err)
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(encoded)))
	if _, err := d.hash.Write(size[:]); err != nil {
		return err
	}
	if _, err := d.hash.Write(encoded); err != nil {
		return err
	}
	d.total++
	return nil
}

func (d *historyCursorDigest) cursor() HistoryCursor {
	return HistoryCursor{Total: d.total, Digest: hex.EncodeToString(d.hash.Sum(nil))}
}

// HistoryCursorForMessages derives the stable prefix cursor used by in-memory
// and fallback session stores.
func HistoryCursorForMessages(messages []providers.Message, total int) (HistoryCursor, error) {
	if total < 0 || total > len(messages) {
		return HistoryCursor{}, fmt.Errorf("memory: history cursor total %d exceeds %d messages", total, len(messages))
	}
	digest := newHistoryCursorDigest()
	for _, message := range messages[:total] {
		if err := digest.add(message); err != nil {
			return HistoryCursor{}, err
		}
	}
	return digest.cursor(), nil
}

func validateHistoryCursor(expected *HistoryCursor, actual HistoryCursor) error {
	if expected == nil {
		return nil
	}
	if expected.Total != actual.Total || expected.Digest != actual.Digest {
		return fmt.Errorf("%w: canonical prefix changed", ErrHistoryCursorStale)
	}
	return nil
}
