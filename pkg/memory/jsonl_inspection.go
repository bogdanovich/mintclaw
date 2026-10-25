package memory

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	MaxJSONLInspectionBytes   = 2 * 1024 * 1024
	MaxJSONLInspectionRecords = 256
)

// JSONLInspection is one clean canonical session decoded from caller-owned
// bytes. It does not establish file provenance, runtime ownership or freshness.
type JSONLInspection struct {
	Metadata SessionMeta
	History  []providers.Message
}

// JSONLSessionFiles returns native filenames, not paths or access authority.
// Callers must anchor access to an already admitted session directory.
func JSONLSessionFiles(key string) (history string, metadata string, err error) {
	if strings.TrimSpace(key) == "" || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return "", "", fmt.Errorf("memory: invalid inspection session key")
	}
	history, metadata = jsonlFileNames(key)
	return history, metadata, nil
}

// InspectJSONLSnapshot performs no I/O, recovery or writes. It shares the native
// committed-record decoder and rejects dirty, partial, mismatched or oversized
// input. The caller owns stable capture, scope admission and privacy inspection.
func InspectJSONLSnapshot(
	ctx context.Context,
	key string,
	metadata, history []byte,
) (JSONLInspection, error) {
	if err := contextCause(ctx); err != nil {
		return JSONLInspection{}, err
	}
	if _, _, err := JSONLSessionFiles(key); err != nil {
		return JSONLInspection{}, err
	}
	if len(history) > MaxJSONLInspectionBytes || len(metadata) > MaxJSONLInspectionBytes-len(history) {
		return JSONLInspection{}, fmt.Errorf("memory: session inspection exceeds byte limit")
	}
	meta, err := DecodeSessionMeta(metadata)
	if err != nil {
		return JSONLInspection{}, fmt.Errorf("memory: invalid inspection metadata: %w", err)
	}
	if meta.Key != key || meta.HistoryDirty || meta.Count > MaxJSONLInspectionRecords {
		return JSONLInspection{}, fmt.Errorf("memory: session inspection identity, state or record limit mismatch")
	}
	messages, err := decodeCommittedMessages(ctx, bytes.NewReader(history), meta.Count, meta.Skip)
	if err != nil {
		return JSONLInspection{}, err
	}
	return JSONLInspection{Metadata: meta, History: messages}, nil
}
