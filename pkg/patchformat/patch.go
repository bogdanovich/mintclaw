// Package patchformat parses and prepares the bounded Codex patch format used
// by local and companion workspace tools. It performs no filesystem I/O.
package patchformat

import (
	"fmt"
	"strings"
)

type Kind string

const (
	Add    Kind = "add"
	Update Kind = "update"
	Delete Kind = "delete"
)

type Operation struct {
	Kind  Kind
	Path  string
	Lines []string
}

type Mutation struct {
	Path   string
	Action string
	Before []byte
	After  []byte
}

func Parse(input string, maxOperations int) ([]Operation, error) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}
	var operations []Operation
	for index := 1; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		index++
		if line == "" {
			continue
		}
		if line == "*** End Patch" {
			if len(operations) == 0 {
				return nil, fmt.Errorf("patch contains no operations")
			}
			return operations, nil
		}
		kind, path, err := parseHeader(line)
		if err != nil {
			return nil, err
		}
		operation := Operation{Kind: kind, Path: path}
		for index < len(lines) {
			next := strings.TrimSpace(lines[index])
			if isHeader(next) || next == "*** End Patch" {
				break
			}
			if strings.HasPrefix(next, "*** Move to: ") {
				return nil, fmt.Errorf("apply_patch does not support move operations yet")
			}
			operation.Lines = append(operation.Lines, lines[index])
			index++
		}
		operations = append(operations, operation)
		if maxOperations > 0 && len(operations) > maxOperations {
			return nil, fmt.Errorf("patch exceeds the %d file limit", maxOperations)
		}
	}
	return nil, fmt.Errorf("patch must end with *** End Patch")
}

func Prepare(operation Operation, before []byte, exists bool) (Mutation, error) {
	switch operation.Kind {
	case Add:
		if exists {
			return Mutation{}, fmt.Errorf("%s: file already exists", operation.Path)
		}
		after, err := addedContent(operation.Lines)
		if err != nil {
			return Mutation{}, fmt.Errorf("%s: %w", operation.Path, err)
		}
		return Mutation{Path: operation.Path, Action: string(Add), After: after}, nil
	case Delete:
		if !exists {
			return Mutation{}, fmt.Errorf("%s: file not found", operation.Path)
		}
		return Mutation{Path: operation.Path, Action: string(Delete), Before: before}, nil
	case Update:
		if !exists {
			return Mutation{}, fmt.Errorf("%s: file not found", operation.Path)
		}
		after, err := updatedContent(before, operation.Lines)
		if err != nil {
			return Mutation{}, fmt.Errorf("%s: %w", operation.Path, err)
		}
		return Mutation{
			Path: operation.Path, Action: string(Update), Before: before, After: after,
		}, nil
	default:
		return Mutation{}, fmt.Errorf("%s: unknown patch operation", operation.Path)
	}
}

func isHeader(line string) bool {
	return strings.HasPrefix(line, "*** Add File: ") ||
		strings.HasPrefix(line, "*** Update File: ") ||
		strings.HasPrefix(line, "*** Delete File: ")
}

func parseHeader(line string) (Kind, string, error) {
	for _, candidate := range []struct {
		prefix string
		kind   Kind
	}{
		{"*** Add File: ", Add},
		{"*** Update File: ", Update},
		{"*** Delete File: ", Delete},
	} {
		if strings.HasPrefix(line, candidate.prefix) {
			path := strings.TrimSpace(strings.TrimPrefix(line, candidate.prefix))
			if path == "" {
				return candidate.kind, "", fmt.Errorf("patch operation path is required")
			}
			return candidate.kind, path, nil
		}
	}
	return Add, "", fmt.Errorf("unsupported patch header: %s", line)
}

func addedContent(lines []string) ([]byte, error) {
	output := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			output = append(output, "")
			continue
		}
		if !strings.HasPrefix(line, "+") {
			return nil, fmt.Errorf("add file lines must start with +")
		}
		output = append(output, strings.TrimPrefix(line, "+"))
	}
	return []byte(strings.Join(output, "\n") + "\n"), nil
}

func updatedContent(before []byte, lines []string) ([]byte, error) {
	blocks := splitUpdateBlocks(lines)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("update operation has no hunks")
	}
	content := string(before)
	for _, block := range blocks {
		oldText, newText, err := blockTexts(block)
		if err != nil {
			return nil, err
		}
		if oldText == "" {
			return nil, fmt.Errorf("update hunk has no removable/context content")
		}
		count := strings.Count(content, oldText)
		if count == 0 {
			return nil, fmt.Errorf("hunk context not found")
		}
		if count > 1 {
			return nil, fmt.Errorf("hunk context appears %d times; add more context", count)
		}
		content = strings.Replace(content, oldText, newText, 1)
	}
	return []byte(content), nil
}

func splitUpdateBlocks(lines []string) [][]string {
	var blocks [][]string
	var current []string
	seenHunk := false
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if seenHunk && len(current) > 0 {
				blocks = append(blocks, current)
			}
			current = nil
			seenHunk = true
			continue
		}
		if !seenHunk && strings.TrimSpace(line) == "" {
			continue
		}
		seenHunk = true
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

func blockTexts(lines []string) (string, string, error) {
	var oldLines []string
	var newLines []string
	for _, line := range lines {
		if line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			return "", "", fmt.Errorf("update hunk lines must start with space, -, or +")
		}
		text := line[1:] + "\n"
		switch line[0] {
		case ' ':
			oldLines = append(oldLines, text)
			newLines = append(newLines, text)
		case '-':
			oldLines = append(oldLines, text)
		case '+':
			newLines = append(newLines, text)
		default:
			return "", "", fmt.Errorf("update hunk lines must start with space, -, or +")
		}
	}
	return strings.Join(oldLines, ""), strings.Join(newLines, ""), nil
}
