package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func TestRuntimeSkillContextHashesNativeSelectedRevisionWithoutRawIdentity(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "skills")
	directory := filepath.Join(root, "private-skill-canary")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "SKILL.md")
	content := "---\nname: private-skill-canary\ndescription: synthetic transform\n---\n\nPRIVATE_INSTRUCTIONS\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	builder := newContextBuilderWithMemoryStoreAndSkills(workspace, NewMemoryStore(workspace), []skills.SkillRoot{{
		Path: root, Scope: skills.SkillScopeWorkspace, Runtime: skills.SkillRuntimeShared, Trust: skills.SkillTrustUser,
	}})
	selected, err := builder.SelectSkillsForTurn([]string{"private-skill-canary"}, skills.SkillRuntimeShared)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Equal(t, "sha256:"+diagnostictrace.FilteredHash(content, nil), selected[0].Revision)
	ts := &turnState{selectedSkills: selected}
	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, []string{selected[0].Name})
	snapshots := ts.skillContextSnapshotsSnapshot()
	payload := TurnEndPayload{
		SkillContextSnapshots: snapshots, UserMessage: "PRIVATE_USER", FinalContent: "PRIVATE_FINAL",
	}
	fields := runtimeEventLogFields(runtimeevents.Event{Kind: runtimeevents.KindAgentTurnEnd, Payload: payload})
	encoded, err := json.Marshal(snapshots)
	require.NoError(t, err)
	require.Equal(t, 1, fields["skill_context_version"])
	require.Equal(t, "observed", fields["skill_context_code"])
	require.Equal(t, 1, fields["skill_context_count"])
	require.Equal(t, diagnostictrace.FilteredHash(string(encoded), nil), fields["skill_context_sha256"])
	logged, err := json.Marshal(fields)
	require.NoError(t, err)
	for _, private := range []string{workspace, selected[0].Name, "PRIVATE_INSTRUCTIONS", "PRIVATE_USER", "PRIVATE_FINAL"} {
		require.NotContains(t, string(logged), private)
	}
	before := fields["skill_context_sha256"]
	snapshots[0].Selections[0].Revision = "changed"
	require.Equal(t, before, fields["skill_context_sha256"])
}

func TestRuntimeSkillContextDistinguishesEmptyCompleteCaptureFromMissingEvidence(t *testing.T) {
	for _, snapshots := range [][]SkillContextSnapshot{nil, {}} {
		fields := map[string]any{}
		appendRuntimeSkillContextSummary(fields, snapshots)
		require.Equal(t, "observed", fields["skill_context_code"])
		require.Equal(t, 0, fields["skill_context_count"])
		require.Equal(t, diagnostictrace.FilteredHash("[]", nil), fields["skill_context_sha256"])
	}
	fields := runtimeEventLogFields(runtimeevents.Event{Payload: TurnStartPayload{}})
	_, present := fields["skill_context_sha256"]
	require.False(t, present, "another event cannot invent a complete empty turn-end observation")
}

func TestRuntimeSkillContextLimitsNeverPublishPartialHash(t *testing.T) {
	for _, kind := range []string{"snapshots", "names", "selections", "bytes", "encoded_bytes"} {
		t.Run(kind, func(t *testing.T) {
			snapshots := []SkillContextSnapshot{{Sequence: 1, Trigger: skillContextTriggerInitialBuild}}
			switch kind {
			case "snapshots":
				snapshots = make([]SkillContextSnapshot, runtimeSkillContextMaxSnapshots+1)
			case "names":
				snapshots[0].SkillNames = make([]string, runtimeSkillContextMaxEntries+1)
			case "selections":
				snapshots[0].Selections = make([]SkillRevisionIdentity, runtimeSkillContextMaxEntries+1)
			case "bytes":
				snapshots[0].SkillNames = []string{strings.Repeat("x", runtimeSkillContextMaxBytes+1)}
			case "encoded_bytes":
				snapshots[0].SkillNames = []string{strings.Repeat("\x00", runtimeSkillContextMaxBytes/2)}
			}
			fields := map[string]any{}
			appendRuntimeSkillContextSummary(fields, snapshots)
			require.Equal(t, "limit", fields["skill_context_code"])
			require.Equal(t, len(snapshots), fields["skill_context_count"])
			_, present := fields["skill_context_sha256"]
			require.False(t, present, "a limit must not hide a discarded selection behind a partial digest")
		})
	}
}

func TestRuntimeSkillContextDigestCoversOrderTriggerAndEveryRevision(t *testing.T) {
	initial := SkillContextSnapshot{
		Sequence: 1, Trigger: skillContextTriggerInitialBuild, SkillNames: []string{"one", "two"},
		Selections: []SkillRevisionIdentity{
			{Name: "one", Path: "/synthetic/one", Scope: "workspace", Revision: "sha256:one"},
			{Name: "two", Path: "/synthetic/two", Scope: "workspace", Revision: "sha256:two"},
		},
	}
	hash := func(snapshot SkillContextSnapshot) any {
		fields := map[string]any{}
		appendRuntimeSkillContextSummary(fields, []SkillContextSnapshot{snapshot})
		require.Equal(t, "observed", fields["skill_context_code"])
		return fields["skill_context_sha256"]
	}
	want := hash(initial)
	for _, kind := range []string{"order", "trigger", "revision", "scope", "path", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			data, err := json.Marshal(initial)
			require.NoError(t, err)
			var changed SkillContextSnapshot
			require.NoError(t, json.Unmarshal(data, &changed))
			switch kind {
			case "order":
				changed.Selections[0], changed.Selections[1] = changed.Selections[1], changed.Selections[0]
			case "trigger":
				changed.Trigger = skillContextTriggerContextRetryRebuild
			case "revision":
				changed.Selections[1].Revision = "sha256:changed"
			case "scope":
				changed.Selections[0].Scope = "user"
			case "path":
				changed.Selections[0].Path = "/synthetic/other"
			case "sequence":
				changed.Sequence++
			}
			require.NotEqual(t, want, hash(changed))
		})
	}
}
