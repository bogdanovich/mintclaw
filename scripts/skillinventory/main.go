// Command skillinventory generates the pinned MintClaw skill-portability audit.
//
// The command is intentionally a developer tool rather than part of the runtime.
// It reads explicitly supplied source checkouts/caches and writes one deterministic
// JSON inventory. CI validates the committed inventory without requiring those
// external trees.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	decisionAdapt   = "adapt"
	decisionCovered = "covered"
	decisionDefer   = "defer"
	decisionExclude = "exclude"
	decisionPort    = "port"
)

type source struct {
	ID             string `json:"id"`
	Repository     string `json:"repository,omitempty"`
	Distribution   string `json:"distribution,omitempty"`
	RevisionKind   string `json:"revision_kind"`
	Revision       string `json:"revision"`
	SkillCount     int    `json:"skill_count"`
	LicensePolicy  string `json:"license_policy"`
	SelectionBasis string `json:"selection_basis"`
}

type entry struct {
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

type inventory struct {
	SchemaVersion int            `json:"schema_version"`
	AuditedOn     string         `json:"audited_on"`
	Sources       []source       `json:"sources"`
	DecisionCount map[string]int `json:"decision_counts"`
	Entries       []entry        `json:"entries"`
}

type options struct {
	codexRoot    string
	pluginsRoot  string
	cacheRoot    string
	mintclawRoot string
	output       string
	auditedOn    string
}

func main() {
	var opts options
	flag.StringVar(&opts.codexRoot, "codex", "", "path to a clean openai/codex checkout")
	flag.StringVar(&opts.pluginsRoot, "plugins", "", "path to a clean openai/plugins checkout")
	flag.StringVar(&opts.cacheRoot, "cache", "", "path to the Codex plugin cache root")
	flag.StringVar(&opts.mintclawRoot, "mintclaw", "", "path to the pinned MintClaw baseline checkout")
	flag.StringVar(&opts.output, "output", "", "inventory JSON destination")
	flag.StringVar(&opts.auditedOn, "audited-on", time.Now().UTC().Format("2006-01-02"), "audit date")
	flag.Parse()

	if err := run(opts); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(opts options) error {
	for name, value := range map[string]string{
		"--codex": opts.codexRoot, "--plugins": opts.pluginsRoot, "--cache": opts.cacheRoot,
		"--mintclaw": opts.mintclawRoot, "--output": opts.output,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	codexRevision, err := cleanGitRevision(opts.codexRoot)
	if err != nil {
		return fmt.Errorf("pin Codex source: %w", err)
	}
	pluginsRevision, err := cleanGitRevision(opts.pluginsRoot)
	if err != nil {
		return fmt.Errorf("pin plugins source: %w", err)
	}
	mintclawRevision, err := cleanGitRevision(opts.mintclawRoot)
	if err != nil {
		return fmt.Errorf("pin MintClaw source: %w", err)
	}

	sources := make([]source, 0, 6)
	entries := make([]entry, 0, 594)

	codexEntries, err := collectCodex(opts.codexRoot)
	if err != nil {
		return err
	}
	sources = append(sources, source{
		ID: "openai-codex", Repository: "https://github.com/openai/codex", RevisionKind: "git_commit",
		Revision: codexRevision, SkillCount: len(codexEntries), LicensePolicy: "Apache-2.0 repository license",
		SelectionBasis: "all SKILL.md files in .codex/skills and codex-rs/skills/src/assets/samples",
	})
	entries = append(entries, codexEntries...)

	pluginEntries, err := collectPlugins(opts.pluginsRoot)
	if err != nil {
		return err
	}
	sources = append(sources, source{
		ID: "openai-plugins", Repository: "https://github.com/openai/plugins", RevisionKind: "git_commit",
		Revision: pluginsRevision, SkillCount: len(pluginEntries),
		LicensePolicy:  "nearest top-level plugin manifest; NOASSERTION when absent",
		SelectionBasis: "all SKILL.md files in the pinned repository",
	})
	entries = append(entries, pluginEntries...)

	for _, distribution := range []struct {
		name  string
		count int
	}{
		{name: "openai-bundled", count: 4},
		{name: "openai-curated-remote", count: 21},
		{name: "openai-primary-runtime", count: 6},
	} {
		root := filepath.Join(opts.cacheRoot, distribution.name)
		cacheEntries, collectErr := collectCache(root, distribution.name)
		if collectErr != nil {
			return collectErr
		}
		if len(cacheEntries) != distribution.count {
			return fmt.Errorf("%s skill count = %d, want %d", distribution.name, len(cacheEntries), distribution.count)
		}
		revision, fingerprintErr := skillTreeFingerprint(root, cacheEntries)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		sources = append(sources, source{
			ID: "codex-cache-" + distribution.name, Distribution: distribution.name,
			RevisionKind: "skill_tree_sha256", Revision: revision, SkillCount: len(cacheEntries),
			LicensePolicy:  "package manifest license in the installed immutable cache",
			SelectionBasis: "all SKILL.md files in the active configured distribution root",
		})
		entries = append(entries, cacheEntries...)
	}

	mintclawEntries, err := collectMintClaw(opts.mintclawRoot)
	if err != nil {
		return err
	}
	sources = append(sources, source{
		ID: "mintclaw-baseline", Repository: "https://github.com/bogdanovich/mintclaw",
		RevisionKind: "git_commit", Revision: mintclawRevision, SkillCount: len(mintclawEntries),
		LicensePolicy: "MIT repository license", SelectionBasis: "all bundled SKILL.md files before this audit",
	})
	entries = append(entries, mintclawEntries...)

	wantCounts := map[string]int{
		"openai-codex": 17, "openai-plugins": 536, "codex-cache-openai-bundled": 4,
		"codex-cache-openai-curated-remote": 21, "codex-cache-openai-primary-runtime": 6,
		"mintclaw-baseline": 10,
	}
	for _, item := range sources {
		if want := wantCounts[item.ID]; item.SkillCount != want {
			return fmt.Errorf("%s skill count = %d, want %d", item.ID, item.SkillCount, want)
		}
	}
	if len(entries) != 594 {
		return fmt.Errorf("inventory entry count = %d, want 594", len(entries))
	}

	order := make(map[string]int, len(sources))
	for index, item := range sources {
		order[item.ID] = index
	}
	sort.Slice(entries, func(i, j int) bool {
		if order[entries[i].SourceID] == order[entries[j].SourceID] {
			return entries[i].Path < entries[j].Path
		}
		return order[entries[i].SourceID] < order[entries[j].SourceID]
	})
	decisionCounts := map[string]int{
		decisionPort: 0, decisionAdapt: 0, decisionCovered: 0, decisionDefer: 0, decisionExclude: 0,
	}
	for _, item := range entries {
		decisionCounts[item.Decision]++
	}

	document := inventory{
		SchemaVersion: 1,
		AuditedOn:     opts.auditedOn,
		Sources:       sources,
		DecisionCount: decisionCounts,
		Entries:       entries,
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode inventory: %w", err)
	}
	data = append(data, '\n')
	if err = os.WriteFile(opts.output, data, 0o644); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	return nil
}

func collectCodex(root string) ([]entry, error) {
	paths, err := collectSkillPaths(root, []string{".codex/skills", "codex-rs/skills/src/assets/samples"})
	if err != nil {
		return nil, fmt.Errorf("collect Codex skills: %w", err)
	}
	items := make([]entry, 0, len(paths))
	for _, path := range paths {
		item := entry{SourceID: "openai-codex", Path: path, License: "Apache-2.0"}
		switch path {
		case "codex-rs/skills/src/assets/samples/imagegen/SKILL.md":
			item.Decision = decisionAdapt
			item.Rationale = "Portable raster-image workflow; replace Codex tools, paths, API fallback, and UI assumptions with MintClaw image_generate."
			item.Target = "imagegen"
			item.Runtimes = []string{"gateway"}
			item.Capabilities = []string{"tool:image_generate"}
			item.Evidence = []string{"pkg/skills/compatibility_test.go", "pkg/skills/system_bundle_test.go"}
		case "codex-rs/skills/src/assets/samples/review-agent/SKILL.md":
			item.Decision = decisionCovered
			item.Rationale = "MintClaw native /review provides a stronger frozen-diff, read-only, bounded reviewer contract."
			item.Target = "native:/review"
			item.Runtimes = []string{"coding"}
			item.Evidence = []string{"docs/architecture/local-coding-agent-p6-4-review.md"}
		case "codex-rs/skills/src/assets/samples/skill-creator/SKILL.md":
			item.Decision = decisionCovered
			item.Rationale = "The bundled MintClaw skill-creator already owns the Agent Skills package workflow."
			item.Target = "skill-creator"
			item.Runtimes = []string{"coding", "gateway"}
			item.Evidence = []string{"pkg/skills/bundled/skill-creator/SKILL.md"}
		case "codex-rs/skills/src/assets/samples/openai-docs/SKILL.md":
			item = deferred(
				item,
				"Requires an admitted official OpenAI documentation retrieval contract; generic web lookup is insufficient.",
				"future official-docs capability",
			)
		case "codex-rs/skills/src/assets/samples/skill-installer/SKILL.md":
			item = deferred(
				item,
				"Codex-specific installation paths and catalog policy must wait for MintClaw's S6 scoped installer.",
				"shared-skills S6",
			)
		default:
			item = excluded(
				item,
				"Codex repository maintenance or plugin-product workflow, not a host-neutral MintClaw system default.",
			)
		}
		items = append(items, item)
	}
	return items, nil
}

func collectPlugins(root string) ([]entry, error) {
	paths, err := collectSkillPaths(root, []string{"."})
	if err != nil {
		return nil, fmt.Errorf("collect OpenAI plugin skills: %w", err)
	}
	items := make([]entry, 0, len(paths))
	licenseCache := make(map[string]string)
	for _, path := range paths {
		license := "NOASSERTION"
		parts := strings.Split(path, "/")
		if len(parts) >= 2 && parts[0] == "plugins" {
			plugin := parts[1]
			if cached, ok := licenseCache[plugin]; ok {
				license = cached
			} else {
				license = manifestLicense(filepath.Join(root, "plugins", plugin, ".codex-plugin", "plugin.json"))
				licenseCache[plugin] = license
			}
		}
		item := entry{SourceID: "openai-plugins", Path: path, License: license}
		item = excluded(
			item,
			"Vendor-, connector-, marketplace-, or specialized Codex plugin workflow; not admitted as a MintClaw system default. S6 may install a reviewed package explicitly.",
		)
		items = append(items, item)
	}
	return items, nil
}

func collectCache(root, distribution string) ([]entry, error) {
	paths, err := collectSkillPaths(root, []string{"."})
	if err != nil {
		return nil, fmt.Errorf("collect %s skills: %w", distribution, err)
	}
	sourceID := "codex-cache-" + distribution
	items := make([]entry, 0, len(paths))
	for _, path := range paths {
		parts := strings.Split(path, "/")
		license := "NOASSERTION"
		if len(parts) >= 2 {
			license = manifestLicense(filepath.Join(root, parts[0], parts[1], ".codex-plugin", "plugin.json"))
		}
		item := entry{SourceID: sourceID, Path: path, License: license}
		switch {
		case distribution == "openai-primary-runtime" && strings.Contains(path, "/skills/pdf/SKILL.md"):
			item.Decision = decisionCovered
			item.Rationale = "MintClaw's feature-owned PDF skill and document tool provide the admitted gateway PDF contract."
			item.Target = "pdf"
			item.Runtimes = []string{"gateway"}
			item.Evidence = []string{"pkg/skills/bundled/pdf/SKILL.md", "docs/architecture/pdf-support-roadmap.md"}
		case distribution == "openai-primary-runtime" &&
			(strings.Contains(path, "/skills/documents/SKILL.md") ||
				strings.Contains(path, "/skills/presentations/SKILL.md") ||
				strings.Contains(path, "/skills/spreadsheets/SKILL.md") ||
				strings.Contains(path, "/skills/excel-live-control/SKILL.md")):
			item = deferred(
				item,
				"Requires a stable MintClaw artifact/runtime tool contract rather than product-specific hosted tools.",
				"future artifact-runtime roadmap",
			)
		default:
			item = excluded(
				item,
				"Product-specific, proprietary, hosted-tool, UI-template, or plugin-management workflow outside the MintClaw system bundle.",
			)
		}
		items = append(items, item)
	}
	return items, nil
}

func collectMintClaw(root string) ([]entry, error) {
	paths, err := collectSkillPaths(root, []string{"pkg/skills/bundled"})
	if err != nil {
		return nil, fmt.Errorf("collect MintClaw skills: %w", err)
	}
	items := make([]entry, 0, len(paths))
	for _, path := range paths {
		parts := strings.Split(path, "/")
		name := parts[len(parts)-2]
		runtimes := []string{"coding", "gateway"}
		if name == "agent-browser" || name == "hardware" || name == "pdf" {
			runtimes = []string{"gateway"}
		}
		items = append(items, entry{
			SourceID: "mintclaw-baseline", Path: path, License: "MIT", Decision: decisionCovered,
			Rationale: "Existing MintClaw-owned bundled skill; retain and consolidate rather than import a duplicate.",
			Target:    name, Runtimes: runtimes, Evidence: []string{path},
		})
	}
	return items, nil
}

func collectSkillPaths(root string, subroots []string) ([]string, error) {
	paths := make([]string, 0)
	for _, subroot := range subroots {
		walkRoot := filepath.Join(root, filepath.FromSlash(subroot))
		err := filepath.WalkDir(walkRoot, func(path string, directory fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if directory.IsDir() || directory.Name() != "SKILL.md" {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(relative))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func skillTreeFingerprint(root string, entries []entry) (string, error) {
	hash := sha256.New()
	for _, item := range entries {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Path)))
		if err != nil {
			return "", fmt.Errorf("read %s for fingerprint: %w", item.Path, err)
		}
		sum := sha256.Sum256(content)
		_, _ = fmt.Fprintf(hash, "%s\x00%s\n", item.Path, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func manifestLicense(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "NOASSERTION"
	}
	var manifest struct {
		License string `json:"license"`
	}
	if json.Unmarshal(data, &manifest) != nil || strings.TrimSpace(manifest.License) == "" {
		return "NOASSERTION"
	}
	return strings.TrimSpace(manifest.License)
}

func deferred(item entry, rationale, owner string) entry {
	item.Decision = decisionDefer
	item.Rationale = rationale
	item.Evidence = []string{owner}
	return item
}

func excluded(item entry, rationale string) entry {
	item.Decision = decisionExclude
	item.Rationale = rationale
	item.Evidence = []string{"docs/architecture/skill-portability-audit.md"}
	return item
}

func cleanGitRevision(root string) (string, error) {
	status, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		return "", errors.New("source checkout is dirty")
	}
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	revision := strings.TrimSpace(string(output))
	if len(revision) != 40 {
		return "", fmt.Errorf("unexpected Git revision %q", revision)
	}
	return revision, nil
}
