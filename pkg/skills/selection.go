package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSkillInstructionBytes         = 256 * 1024
	MaxSelectedSkillInstructionBytes = 512 * 1024
)

var skillMentionPattern = regexp.MustCompile(
	`\$([A-Za-z0-9]+(?:-[A-Za-z0-9]+)*)`,
)

type SkillSelectionFailure string

const (
	SkillSelectionUnknown      SkillSelectionFailure = "unknown"
	SkillSelectionAmbiguous    SkillSelectionFailure = "ambiguous"
	SkillSelectionDisabled     SkillSelectionFailure = "disabled"
	SkillSelectionIncompatible SkillSelectionFailure = "incompatible"
	SkillSelectionUnreadable   SkillSelectionFailure = "unreadable"
	SkillSelectionTooLarge     SkillSelectionFailure = "too_large"
)

type SkillSelector struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

func (selector SkillSelector) String() string {
	if path := strings.TrimSpace(selector.Path); path != "" {
		return path
	}
	return strings.TrimSpace(selector.Name)
}

type SkillSelectionError struct {
	Kind       SkillSelectionFailure `json:"kind"`
	Selector   SkillSelector         `json:"selector"`
	Candidates []string              `json:"candidates,omitempty"`
	Err        error                 `json:"-"`
}

func (err *SkillSelectionError) Error() string {
	if err == nil {
		return ""
	}
	selector := err.Selector.String()
	switch err.Kind {
	case SkillSelectionUnknown:
		return fmt.Sprintf("unknown skill selector %q", selector)
	case SkillSelectionAmbiguous:
		return fmt.Sprintf("ambiguous skill selector %q", selector)
	case SkillSelectionDisabled:
		return fmt.Sprintf("skill %q is disabled by the active turn profile", selector)
	case SkillSelectionIncompatible:
		return fmt.Sprintf("skill %q is incompatible with this runtime", selector)
	case SkillSelectionTooLarge:
		return fmt.Sprintf("skill %q exceeds the instruction size limit", selector)
	case SkillSelectionUnreadable:
		if err.Err != nil {
			return fmt.Sprintf("skill %q could not be read: %v", selector, err.Err)
		}
		return fmt.Sprintf("skill %q could not be read", selector)
	default:
		return fmt.Sprintf("skill selector %q failed", selector)
	}
}

func (err *SkillSelectionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type SkillSelectionOptions struct {
	Runtime      SkillRuntime
	AllowedNames []string
}

type SelectedSkill struct {
	Name         string       `json:"name"`
	Path         string       `json:"path"`
	Scope        SkillScope   `json:"scope"`
	Runtime      SkillRuntime `json:"runtime"`
	Trust        SkillTrust   `json:"trust"`
	Revision     string       `json:"revision"`
	Instructions string       `json:"-"`
}

func (sl *SkillsLoader) Select(
	selectors []SkillSelector,
	options SkillSelectionOptions,
) ([]SelectedSkill, error) {
	if len(selectors) == 0 {
		return nil, nil
	}
	catalog := sl.Discover()
	allowed := selectionAllowedSet(options.AllowedNames)
	selected := make([]SelectedSkill, 0, len(selectors))
	seen := make(map[string]struct{}, len(selectors))
	totalBytes := 0
	for _, selector := range selectors {
		info, err := resolveSelectableSkill(catalog.Skills, selector, options.Runtime, allowed)
		if err != nil {
			return nil, err
		}
		key := filepath.Clean(info.Path)
		if _, ok := seen[key]; ok {
			continue
		}
		frozen, contentBytes, err := sl.freezeSelectedSkill(info, selector)
		if err != nil {
			return nil, err
		}
		totalBytes += contentBytes
		if totalBytes > MaxSelectedSkillInstructionBytes {
			return nil, &SkillSelectionError{Kind: SkillSelectionTooLarge, Selector: selector}
		}
		seen[key] = struct{}{}
		selected = append(selected, frozen)
	}
	return selected, nil
}

func (sl *SkillsLoader) Resolve(
	selector SkillSelector,
	options SkillSelectionOptions,
) (SkillInfo, error) {
	return resolveSelectableSkill(
		sl.Discover().Skills,
		selector,
		options.Runtime,
		selectionAllowedSet(options.AllowedNames),
	)
}

func (sl *SkillsLoader) MentionedSelectors(text string, runtime SkillRuntime) []SkillSelector {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	catalog := sl.Discover()
	byName := make(map[string]SkillInfo, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		if skillRuntimeCompatible(skill.Runtime, runtime) {
			byName[strings.ToLower(skill.Name)] = skill
		}
	}
	matches := skillMentionPattern.FindAllStringSubmatchIndex(text, -1)
	selectors := make([]SkillSelector, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) != 4 || skillMentionHasContinuation(text, match[0], match[3]) {
			continue
		}
		info, ok := byName[strings.ToLower(text[match[2]:match[3]])]
		if !ok {
			continue
		}
		key := strings.ToLower(info.Name)
		if _, ok = seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		selectors = append(selectors, SkillSelector{Name: info.Name})
	}
	return selectors
}

func skillMentionHasContinuation(text string, start, end int) bool {
	if start > 0 {
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		if skillMentionContinuationRune(before) {
			return true
		}
	}
	if end < len(text) {
		after, _ := utf8.DecodeRuneInString(text[end:])
		if skillMentionContinuationRune(after) {
			return true
		}
	}
	return false
}

func skillMentionContinuationRune(value rune) bool {
	return value == '_' || value == '-' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func resolveSkillSelector(skills []SkillInfo, selector SkillSelector) (SkillInfo, error) {
	name := strings.TrimSpace(selector.Name)
	path := strings.TrimSpace(selector.Path)
	if name == "" && path == "" {
		return SkillInfo{}, &SkillSelectionError{Kind: SkillSelectionUnknown, Selector: selector}
	}
	if path != "" {
		path = filepath.Clean(path)
	}
	matches := make([]SkillInfo, 0, 1)
	for _, skill := range skills {
		if path != "" && filepath.Clean(skill.Path) != path {
			continue
		}
		if name != "" && !strings.EqualFold(skill.Name, name) {
			continue
		}
		matches = append(matches, skill)
	}
	if len(matches) == 0 {
		return SkillInfo{}, &SkillSelectionError{Kind: SkillSelectionUnknown, Selector: selector}
	}
	if len(matches) > 1 {
		candidates := make([]string, len(matches))
		for index, match := range matches {
			candidates[index] = match.Path
		}
		return SkillInfo{}, &SkillSelectionError{
			Kind:       SkillSelectionAmbiguous,
			Selector:   selector,
			Candidates: candidates,
		}
	}
	return matches[0], nil
}

func resolveSelectableSkill(
	skills []SkillInfo,
	selector SkillSelector,
	runtime SkillRuntime,
	allowed map[string]struct{},
) (SkillInfo, error) {
	info, err := resolveSkillSelector(skills, selector)
	if err != nil {
		return SkillInfo{}, err
	}
	if allowed != nil {
		if _, ok := allowed[strings.ToLower(info.Name)]; !ok {
			return SkillInfo{}, &SkillSelectionError{Kind: SkillSelectionDisabled, Selector: selector}
		}
	}
	if !skillRuntimeCompatible(info.Runtime, runtime) {
		return SkillInfo{}, &SkillSelectionError{Kind: SkillSelectionIncompatible, Selector: selector}
	}
	return info, nil
}

func (sl *SkillsLoader) freezeSelectedSkill(
	info SkillInfo,
	selector SkillSelector,
) (SelectedSkill, int, error) {
	return sl.freezeSelectedSkillWithHook(info, selector, nil)
}

func (sl *SkillsLoader) freezeSelectedSkillWithHook(
	info SkillInfo,
	selector SkillSelector,
	beforeOpen func(),
) (SelectedSkill, int, error) {
	content, err := readSelectedSkillContent(info, beforeOpen)
	if err != nil {
		return SelectedSkill{}, 0, &SkillSelectionError{
			Kind:     SkillSelectionUnreadable,
			Selector: selector,
			Err:      err,
		}
	}
	if len(content) > MaxSkillInstructionBytes {
		return SelectedSkill{}, 0, &SkillSelectionError{Kind: SkillSelectionTooLarge, Selector: selector}
	}
	if !utf8.Valid(content) {
		return SelectedSkill{}, 0, &SkillSelectionError{
			Kind:     SkillSelectionUnreadable,
			Selector: selector,
			Err:      fmt.Errorf("SKILL.md is not valid UTF-8"),
		}
	}
	metadata := sl.skillMetadataFromContent(info.Path, string(content))
	if metadata == nil || !strings.EqualFold(metadata.Name, info.Name) {
		return SelectedSkill{}, 0, &SkillSelectionError{
			Kind:     SkillSelectionUnreadable,
			Selector: selector,
			Err:      fmt.Errorf("SKILL.md identity changed during selection"),
		}
	}
	hash := sha256.Sum256(content)
	instructions := sl.stripFrontmatter(string(content))
	return SelectedSkill{
		Name:         info.Name,
		Path:         info.Path,
		Scope:        info.Scope,
		Runtime:      info.Runtime,
		Trust:        info.Trust,
		Revision:     "sha256:" + hex.EncodeToString(hash[:]),
		Instructions: instructions,
	}, len([]byte(instructions)), nil
}

func readSelectedSkillContent(info SkillInfo, beforeOpen func()) ([]byte, error) {
	rootPath := filepath.Clean(info.admissionRootPath)
	relativePath := filepath.ToSlash(filepath.Clean(info.admissionRelativePath))
	if info.admissionRootInfo == nil || rootPath == "." || !filepath.IsLocal(info.admissionRelativePath) {
		return nil, fmt.Errorf("skill is missing its admitted discovery root")
	}
	currentRoot, err := os.Lstat(rootPath)
	if err != nil || !currentRoot.IsDir() || currentRoot.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info.admissionRootInfo, currentRoot) {
		return nil, fmt.Errorf("skill discovery root identity changed")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open pinned skill root: %w", err)
	}
	defer func() { _ = root.Close() }()
	anchoredRoot, err := root.Lstat(".")
	if err != nil || !anchoredRoot.IsDir() || !os.SameFile(info.admissionRootInfo, anchoredRoot) {
		return nil, fmt.Errorf("skill discovery root changed while it was opened")
	}

	before, err := root.Lstat(relativePath)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("selected skill is not a direct regular file")
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	file, err := root.Open(relativePath)
	if err != nil {
		return nil, fmt.Errorf("open selected skill through pinned root: %w", err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("selected skill identity changed while it was opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxSkillInstructionBytes+1))
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	current, currentErr := root.Lstat(relativePath)
	if statErr != nil || currentErr != nil || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || !sameSelectedSkillFile(opened, after) ||
		!sameSelectedSkillFile(after, current) {
		return nil, fmt.Errorf("selected skill identity changed while it was read")
	}
	return content, nil
}

func sameSelectedSkillFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func selectionAllowedSet(names []string) map[string]struct{} {
	if names == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	return allowed
}

func skillRuntimeCompatible(skillRuntime, requested SkillRuntime) bool {
	if requested == "" || skillRuntime == "" || skillRuntime == SkillRuntimeShared {
		return true
	}
	return skillRuntime == requested
}
