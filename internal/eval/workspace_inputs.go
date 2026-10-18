package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

const (
	workspaceInputScope    = "workspace_files_v1"
	maxWorkspaceInputBytes = 8 * 1024 * 1024
)

// These are local file observations, not the runtime's loaded prompt, effective
// skill catalog, memory, user/system roots, or proof of stability between reads.
type WorkspaceInputs struct {
	Scope  string                 `json:"scope"`
	Before WorkspaceInputSnapshot `json:"before"`
	After  WorkspaceInputSnapshot `json:"after"`
}

type WorkspaceInputSnapshot struct {
	Code               string `json:"code"`
	InstructionsSHA256 string `json:"instructions_sha256,omitempty"`
	SkillsSHA256       string `json:"skills_sha256,omitempty"`
}

func (inputs *WorkspaceInputs) valid() bool {
	return inputs == nil || inputs.Scope == workspaceInputScope && inputs.Before.valid() && inputs.After.valid()
}

func (snapshot WorkspaceInputSnapshot) valid() bool {
	switch snapshot.Code {
	case "observed":
		return validDigest(snapshot.InstructionsSHA256) && validDigest(snapshot.SkillsSHA256)
	case "unavailable":
		return snapshot.InstructionsSHA256 == "" && snapshot.SkillsSHA256 == ""
	default:
		return false
	}
}

type workspaceInputFile struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

func observeWorkspaceInputs(
	ctx context.Context, workspace string, cases []LoadedCase, filter *strings.Replacer,
) WorkspaceInputSnapshot {
	unavailable := WorkspaceInputSnapshot{Code: "unavailable"}
	if ctx.Err() != nil {
		return unavailable
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return unavailable
	}
	defer func() { _ = root.Close() }()
	scan := privateContentScan{filter: filter, facts: map[string]string{}, tokens: 100000}
	for _, item := range cases {
		for _, value := range item.Case.Driver.Facts {
			// Handles may repeat across cases; every value still participates.
			scan.facts[value] = value
		}
	}
	remaining := maxWorkspaceInputBytes
	read := func(name string) (workspaceInputFile, error) {
		if ctx.Err() != nil || len(name) > 1024 || scan.text(name, 0) {
			return workspaceInputFile{}, ErrPrivateEvidence
		}
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			return workspaceInputFile{Name: name, Kind: "absent"}, nil
		}
		if statErr != nil || info.Mode()&fs.ModeSymlink != 0 {
			return workspaceInputFile{}, ErrPrivateEvidence
		}
		entry := workspaceInputFile{Name: name, Kind: "directory", Mode: uint32(info.Mode().Perm())}
		if info.IsDir() {
			return entry, nil
		}
		if !info.Mode().IsRegular() {
			return workspaceInputFile{}, ErrPrivateEvidence
		}
		data, readErr := readBoundedFile(root, name, int64(min(MaxManifestBytes, remaining)))
		if readErr != nil || scan.text(string(data), 0) {
			return workspaceInputFile{}, ErrPrivateEvidence
		}
		remaining -= len(data)
		entry.Kind, entry.SHA256 = "file", digest(data)
		return entry, nil
	}
	instructions := make([]workspaceInputFile, 0, 3)
	for _, name := range []string{"AGENTS.md", "SOUL.md", "USER.md"} {
		entry, readErr := read(name)
		if readErr != nil || entry.Kind == "directory" {
			return unavailable
		}
		instructions = append(instructions, entry)
	}
	skillRoot, err := read("skills")
	if err != nil || skillRoot.Kind == "file" {
		return unavailable
	}
	skillFiles := []workspaceInputFile{skillRoot}
	pending := []string{}
	if skillRoot.Kind == "directory" {
		pending = append(pending, "skills")
	}
	entries := 1
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return unavailable
		}
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		before, statErr := root.Lstat(name)
		if statErr != nil || !before.IsDir() {
			return unavailable
		}
		directory, openErr := root.Open(name)
		if openErr != nil {
			return unavailable
		}
		opened, statErr := directory.Stat()
		if statErr != nil || !opened.IsDir() || !os.SameFile(before, opened) {
			_ = directory.Close()
			return unavailable
		}
		err = consumeCorpusDirectory(directory, &entries, func(entry fs.DirEntry) error {
			child, readErr := read(path.Join(name, entry.Name()))
			if readErr != nil || child.Kind == "absent" {
				return ErrPrivateEvidence
			}
			skillFiles = append(skillFiles, child)
			if child.Kind == "directory" {
				pending = append(pending, child.Name)
			}
			return nil
		})
		closeErr := directory.Close()
		if err != nil || closeErr != nil {
			return unavailable
		}
	}
	slices.SortFunc(skillFiles, func(a, b workspaceInputFile) int { return strings.Compare(a.Name, b.Name) })
	if ctx.Err() != nil {
		return unavailable
	}
	return WorkspaceInputSnapshot{
		Code: "observed", InstructionsSHA256: workspaceInputDigest(instructions),
		SkillsSHA256: workspaceInputDigest(skillFiles),
	}
}

func workspaceInputDigest(files []workspaceInputFile) string {
	data, _ := json.Marshal(files) // This private, string/integer-only shape cannot fail marshaling.
	return digest(data)
}

func compareWorkspaceInputs(result *Comparison, baseline, current *WorkspaceInputs) {
	if baseline == nil && current == nil {
		return
	}
	result.EnvironmentCoverage = "recorded_runtime_roles_and_workspace_files_only"
	for _, source := range []struct {
		name   string
		inputs *WorkspaceInputs
	}{{"baseline", baseline}, {"current", current}} {
		if source.inputs == nil || source.inputs.Before.Code != "observed" || source.inputs.After.Code != "observed" {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_workspace_inputs")
		} else if source.inputs.Before != source.inputs.After {
			result.EnvironmentUnknowns = append(result.EnvironmentUnknowns, source.name+"_workspace_inputs_changed")
		}
	}
	if baseline == nil || current == nil || baseline.Before.Code != "observed" || current.Before.Code != "observed" {
		return
	}
	if baseline.Before.InstructionsSHA256 != current.Before.InstructionsSHA256 {
		result.EnvironmentChanges = append(result.EnvironmentChanges, "workspace_instruction_files")
	}
	if baseline.Before.SkillsSHA256 != current.Before.SkillsSHA256 {
		result.EnvironmentChanges = append(result.EnvironmentChanges, "workspace_skill_files")
	}
}
