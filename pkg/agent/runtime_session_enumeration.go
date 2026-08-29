package agent

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/session"
)

func currentRuntimeSessionKeys(instance *AgentInstance, store session.SessionStore) []string {
	if instance == nil || store == nil {
		return nil
	}
	if threadID := instance.CodingLayout.ThreadID(); threadID != "" {
		current := instance.CodingLayout.SessionKey()
		if slices.Contains(store.ListSessions(), current) {
			return []string{current}
		}
		return nil
	}
	metadata, ok := store.(session.MetadataAwareSessionStore)
	if !ok {
		return nil
	}
	return metadata.ListCurrentSessions()
}
