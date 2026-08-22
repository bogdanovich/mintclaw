package main

import (
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

// LegacyStore holds the full-transcript baseline used for memory comparisons.
type LegacyStore struct {
	sm *session.MemoryStore
}

// NewLegacyStore creates a new full-transcript baseline.
func NewLegacyStore() *LegacyStore {
	return &LegacyStore{
		sm: session.NewMemoryStore(),
	}
}

// IngestSample loads all turns from a LOCOMO sample into the baseline store.
func (ls *LegacyStore) IngestSample(sample *LocomoSample) {
	sessionKey := "locomo-" + sample.SampleID
	turns := GetTurns(sample)
	for _, turn := range turns {
		content := turn.Speaker + ": " + turn.Text
		ls.sm.AddMessage(sessionKey, "user", content)
	}
}

// GetHistory returns all messages for a sample's session.
func (ls *LegacyStore) GetHistory(sampleID string) []providers.Message {
	sessionKey := "locomo-" + sampleID
	return ls.sm.GetHistory(sessionKey)
}
