package agent

import (
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
)

const (
	runtimeSkillContextVersion      = 1
	runtimeSkillContextMaxSnapshots = 16
	runtimeSkillContextMaxEntries   = 128
	runtimeSkillContextMaxBytes     = 64 * 1024
)

// Commit to the complete typed snapshot sequence, not a truncated selection.
// Raw identities and instructions are never added to the default event log.
func appendRuntimeSkillContextSummary(fields map[string]any, snapshots []SkillContextSnapshot) {
	fields["skill_context_version"] = runtimeSkillContextVersion
	fields["skill_context_count"] = len(snapshots)
	fields["skill_context_code"] = "limit"
	if len(snapshots) > runtimeSkillContextMaxSnapshots {
		return
	}
	entries, bytes := 0, 0
	for _, snapshot := range snapshots {
		entries += len(snapshot.SkillNames) + len(snapshot.Selections)
		if entries > runtimeSkillContextMaxEntries {
			return
		}
		bytes += len(snapshot.Trigger)
		for _, name := range snapshot.SkillNames {
			bytes += len(name)
		}
		for _, selection := range snapshot.Selections {
			bytes += len(selection.Name) + len(selection.Path) + len(selection.Scope) + len(selection.Revision)
		}
		if bytes > runtimeSkillContextMaxBytes {
			return
		}
	}
	if snapshots == nil {
		snapshots = []SkillContextSnapshot{}
	}
	encoded, err := json.Marshal(snapshots)
	if err != nil || len(encoded) > runtimeSkillContextMaxBytes {
		return
	}
	fields["skill_context_sha256"] = diagnostictrace.FilteredHash(string(encoded), nil)
	fields["skill_context_code"] = "observed"
}
