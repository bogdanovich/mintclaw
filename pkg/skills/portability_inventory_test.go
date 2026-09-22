package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type portabilityInventory struct {
	SchemaVersion int                       `json:"schema_version"`
	AuditedOn     string                    `json:"audited_on"`
	Sources       []portabilitySource       `json:"sources"`
	DecisionCount map[string]int            `json:"decision_counts"`
	Entries       []portabilityInventoryRow `json:"entries"`
}

type portabilitySource struct {
	ID             string `json:"id"`
	Repository     string `json:"repository,omitempty"`
	Distribution   string `json:"distribution,omitempty"`
	RevisionKind   string `json:"revision_kind"`
	Revision       string `json:"revision"`
	SkillCount     int    `json:"skill_count"`
	LicensePolicy  string `json:"license_policy"`
	SelectionBasis string `json:"selection_basis"`
}

type portabilityInventoryRow struct {
	SourceID     string   `json:"source_id"`
	Path         string   `json:"path"`
	License      string   `json:"license"`
	Decision     string   `json:"decision"`
	Rationale    string   `json:"rationale"`
	Target       string   `json:"target,omitempty"`
	Runtimes     []string `json:"runtimes,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Evidence     []string `json:"evidence"`
}

func TestSkillPortabilityInventoryIsPinnedExhaustiveAndActionable(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(repositoryRoot, "docs", "architecture", "skill-portability-inventory.json"))
	require.NoError(t, err)
	var inventory portabilityInventory
	require.NoError(t, decodeStrictJSON(data, &inventory))

	assert.Equal(t, 1, inventory.SchemaVersion)
	assert.Equal(t, "2026-09-22", inventory.AuditedOn)
	require.Len(t, inventory.Entries, 594)
	expectedDecisions := map[string]int{"port": 0, "adapt": 1, "covered": 13, "defer": 6, "exclude": 574}
	assert.Equal(t, expectedDecisions, inventory.DecisionCount)

	expectedSources := map[string]struct {
		kind     string
		revision string
		count    int
	}{
		"openai-codex": {
			kind: "git_commit", revision: "94174e44cbc54cece45f6052328ca0c2cd7a8a2a", count: 17,
		},
		"openai-plugins": {
			kind: "git_commit", revision: "1dc195897af4161d039b80d8471ec0a10c9bbc89", count: 536,
		},
		"codex-cache-openai-bundled": {
			kind:     "skill_tree_sha256",
			revision: "0cc1f7fdc0c68cac678383bb39d6cbe61458fd9fc89441261c3640fd85c944d9",
			count:    4,
		},
		"codex-cache-openai-curated-remote": {
			kind:     "skill_tree_sha256",
			revision: "a27910df0ecab942741f95f8015af01e7216d7a29ccf75f29a86e0b568f9d13d",
			count:    21,
		},
		"codex-cache-openai-primary-runtime": {
			kind:     "skill_tree_sha256",
			revision: "8f83709b31e28b969a710e0b9e961e8d5aa6d7ff818bc359a4a0bd2b0a0616eb",
			count:    6,
		},
		"mintclaw-baseline": {
			kind: "git_commit", revision: "dc8108553d128fc9b620b1b77926d389e35589b9", count: 10,
		},
	}
	require.Len(t, inventory.Sources, len(expectedSources))
	sources := make(map[string]portabilitySource, len(inventory.Sources))
	for _, source := range inventory.Sources {
		expected, ok := expectedSources[source.ID]
		require.True(t, ok, "unexpected source %q", source.ID)
		assert.Equal(t, expected.kind, source.RevisionKind, source.ID)
		assert.Equal(t, expected.revision, source.Revision, source.ID)
		assert.Equal(t, expected.count, source.SkillCount, source.ID)
		assert.NotEmpty(t, source.LicensePolicy, source.ID)
		assert.NotEmpty(t, source.SelectionBasis, source.ID)
		assert.NotEqual(t, source.Repository == "", source.Distribution == "", source.ID)
		if source.RevisionKind == "git_commit" {
			assert.True(t, validLowerHex(source.Revision, 20), source.ID)
		} else {
			assert.True(t, validLowerHex(source.Revision, 32), source.ID)
		}
		_, duplicate := sources[source.ID]
		assert.False(t, duplicate, source.ID)
		sources[source.ID] = source
	}

	actualSourceCounts := make(map[string]int)
	actualDecisionCounts := map[string]int{"port": 0, "adapt": 0, "covered": 0, "defer": 0, "exclude": 0}
	seen := make(map[string]struct{}, len(inventory.Entries))
	for _, row := range inventory.Entries {
		_, knownSource := sources[row.SourceID]
		assert.True(t, knownSource, row.SourceID)
		assert.True(t, fs.ValidPath(row.Path) && strings.HasSuffix(row.Path, "/SKILL.md"), row.Path)
		key := row.SourceID + "\x00" + row.Path
		_, duplicate := seen[key]
		assert.False(t, duplicate, key)
		seen[key] = struct{}{}
		assert.Equal(t, strings.TrimSpace(row.License), row.License, key)
		assert.NotEmpty(t, row.License, key)
		assert.Equal(t, strings.TrimSpace(row.Rationale), row.Rationale, key)
		assert.NotEmpty(t, row.Rationale, key)
		assert.NotEmpty(t, row.Evidence, key)
		_, knownDecision := actualDecisionCounts[row.Decision]
		assert.True(t, knownDecision, key)
		actualSourceCounts[row.SourceID]++
		actualDecisionCounts[row.Decision]++

		assert.True(t, slices.IsSorted(row.Runtimes), key)
		for _, runtimeProduct := range row.Runtimes {
			assert.Contains(t, []string{"coding", "gateway"}, runtimeProduct, key)
		}
		if row.Decision == "port" || row.Decision == "adapt" {
			assert.NotEqual(t, "NOASSERTION", row.License, key)
			require.True(t, namePattern.MatchString(row.Target), key)
			bundleRoot := filepath.Join(repositoryRoot, "pkg", "skills", "bundled", row.Target)
			assert.FileExists(t, filepath.Join(bundleRoot, "SKILL.md"), key)
			assert.FileExists(t, filepath.Join(bundleRoot, "LICENSE"), key)
			assert.FileExists(t, filepath.Join(bundleRoot, skillProvenanceName), key)
		}
		for _, evidence := range row.Evidence {
			if strings.HasPrefix(evidence, "pkg/") || strings.HasPrefix(evidence, "docs/") {
				assert.FileExists(t, filepath.Join(repositoryRoot, filepath.FromSlash(evidence)), key)
			}
		}
	}
	assert.Equal(t, expectedDecisions, actualDecisionCounts)
	for sourceID, expected := range expectedSources {
		assert.Equal(t, expected.count, actualSourceCounts[sourceID], sourceID)
	}

	imagegen := inventoryRowByKey(
		t,
		inventory.Entries,
		"openai-codex",
		"codex-rs/skills/src/assets/samples/imagegen/SKILL.md",
	)
	assert.Equal(t, "adapt", imagegen.Decision)
	assert.Equal(t, "imagegen", imagegen.Target)
	assert.Equal(t, []string{"gateway"}, imagegen.Runtimes)
	assert.Equal(t, []string{"tool:image_generate"}, imagegen.Capabilities)
}

func inventoryRowByKey(
	t *testing.T,
	rows []portabilityInventoryRow,
	sourceID string,
	skillPath string,
) portabilityInventoryRow {
	t.Helper()
	for _, row := range rows {
		if row.SourceID == sourceID && row.Path == skillPath {
			return row
		}
	}
	require.FailNow(t, "inventory row not found", "%s:%s", sourceID, skillPath)
	return portabilityInventoryRow{}
}
