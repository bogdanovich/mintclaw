package agent

import (
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/patchformat"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type codingInstructionTurnState struct {
	mu        sync.Mutex
	loader    *codingInstructionLoader
	delivered map[string]struct{}
}

func newCodingInstructionTurnState(
	loader *codingInstructionLoader,
	history []providers.Message,
) *codingInstructionTurnState {
	if loader == nil {
		return nil
	}
	state := &codingInstructionTurnState{
		loader:    loader,
		delivered: make(map[string]struct{}),
	}
	for _, key := range codingInstructionKeys(loader.initial()) {
		state.delivered[key] = struct{}{}
	}
	for _, key := range codingInstructionKeysFromMessages(history) {
		state.delivered[key] = struct{}{}
	}
	return state
}

func (state *codingInstructionTurnState) discover(
	toolName string,
	arguments map[string]any,
) (codingInstructionBundle, bool) {
	if state == nil || state.loader == nil {
		return codingInstructionBundle{}, false
	}
	targets := codingInstructionTargets(toolName, arguments)
	if len(targets) == 0 {
		return codingInstructionBundle{}, false
	}
	bundle := state.loader.forTargets(targets)

	state.mu.Lock()
	defer state.mu.Unlock()
	unseen := codingInstructionBundle{}
	for _, document := range bundle.Documents {
		if _, ok := state.delivered[document.Key]; ok {
			continue
		}
		state.delivered[document.Key] = struct{}{}
		unseen.Documents = append(unseen.Documents, document)
	}
	for _, diagnostic := range bundle.Diagnostics {
		if _, ok := state.delivered[diagnostic.Key]; ok {
			continue
		}
		state.delivered[diagnostic.Key] = struct{}{}
		unseen.Diagnostics = append(unseen.Diagnostics, diagnostic)
	}
	return unseen, len(unseen.Documents) > 0 || len(unseen.Diagnostics) > 0
}

func codingInstructionTargets(toolName string, arguments map[string]any) []codingInstructionTarget {
	pathArgument := func(name string) string {
		value, _ := arguments[name].(string)
		return strings.TrimSpace(value)
	}

	switch toolName {
	case "read_file", "write_file", "append_file", "load_image", "send_file":
		return []codingInstructionTarget{{Path: pathArgument("path")}}
	case "list_dir":
		return []codingInstructionTarget{{Path: pathArgument("path"), Directory: true}}
	case "search_files":
		return []codingInstructionTarget{{Path: pathArgument("path"), Directory: true, Recursive: true}}
	case "exec":
		if action := pathArgument("action"); action != "run" {
			return nil
		}
		return []codingInstructionTarget{{Path: pathArgument("cwd"), Directory: true, Recursive: true}}
	case "apply_patch":
		input := pathArgument("input")
		operations, err := patchformat.Parse(input, 0)
		if err != nil {
			return nil
		}
		targets := make([]codingInstructionTarget, 0, len(operations))
		for _, operation := range operations {
			targets = append(targets, codingInstructionTarget{Path: operation.Path})
		}
		return targets
	default:
		return nil
	}
}
