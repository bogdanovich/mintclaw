package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	defaultCodingInstructionFileBytes  = 16 * 1024
	defaultCodingInstructionTotalBytes = 64 * 1024
	codingInstructionMarkerPrefix      = "<!-- mintclaw-project-instruction:"
	codingInstructionMarkerSuffix      = " -->"
)

var codingInstructionFilenames = []string{
	"AGENTS.override.md",
	"AGENTS.md",
	"CLAUDE.md",
}

type codingInstructionLoader struct {
	projectRoot string
	initialCWD  string
	globalRoots []string

	maxFileBytes  int
	maxTotalBytes int

	mu    sync.Mutex
	cache map[string]codingInstructionCacheEntry
}

type codingInstructionCacheEntry struct {
	resolvedPath string
	info         fs.FileInfo
	document     codingInstructionDocument
}

type codingInstructionDocument struct {
	Key       string
	Path      string
	Scope     string
	Label     string
	Content   string
	Global    bool
	Truncated bool
}

type codingInstructionDiagnostic struct {
	Key     string
	Message string
}

type codingInstructionBundle struct {
	Documents   []codingInstructionDocument
	Diagnostics []codingInstructionDiagnostic
}

type codingInstructionTarget struct {
	Path      string
	Directory bool
}

func newCodingInstructionLoader(layout CodingRuntimeLayout) *codingInstructionLoader {
	projectRoot := filepath.Clean(layout.ExecutionRoot())
	loader := &codingInstructionLoader{
		projectRoot:   projectRoot,
		initialCWD:    projectRoot,
		maxFileBytes:  defaultCodingInstructionFileBytes,
		maxTotalBytes: defaultCodingInstructionTotalBytes,
		cache:         make(map[string]codingInstructionCacheEntry),
	}

	for _, root := range layout.InstructionRoots() {
		root = filepath.Clean(root)
		if codingPathWithin(root, projectRoot) {
			if codingPathDepth(root) > codingPathDepth(loader.initialCWD) {
				loader.initialCWD = root
			}
			continue
		}
		loader.globalRoots = appendUniqueCodingPath(loader.globalRoots, root)
	}
	return loader
}

func (loader *codingInstructionLoader) workingDirectory() string {
	if loader == nil {
		return ""
	}
	return loader.initialCWD
}

func (loader *codingInstructionLoader) initial() codingInstructionBundle {
	if loader == nil {
		return codingInstructionBundle{}
	}

	bundle := codingInstructionBundle{}
	for _, root := range loader.globalRoots {
		loader.appendDirectoryInstruction(&bundle, root, root, true)
	}
	loader.appendProjectChain(&bundle, loader.initialCWD)
	return loader.boundBundle(bundle)
}

func (loader *codingInstructionLoader) forTargets(targets []codingInstructionTarget) codingInstructionBundle {
	if loader == nil || len(targets) == 0 {
		return codingInstructionBundle{}
	}

	bundle := codingInstructionBundle{}
	for _, target := range targets {
		directory, diagnostic := loader.targetDirectory(target)
		if diagnostic.Message != "" {
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic)
			continue
		}
		loader.appendProjectChain(&bundle, directory)
	}
	return loader.boundBundle(bundle)
}

func (loader *codingInstructionLoader) targetDirectory(
	target codingInstructionTarget,
) (string, codingInstructionDiagnostic) {
	path := strings.TrimSpace(target.Path)
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(loader.initialCWD, path)
	}
	path = filepath.Clean(path)
	resolved, err := resolveCodingTarget(path)
	if err != nil {
		return "", codingInstructionWarning(path, err.Error())
	}
	if !codingPathWithin(resolved, loader.projectRoot) {
		return "", codingInstructionWarning(path, "path resolves outside the admitted project root")
	}

	directory := resolved
	if !target.Directory {
		if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
			directory = resolved
		} else {
			directory = filepath.Dir(resolved)
		}
	} else if info, statErr := os.Stat(resolved); statErr == nil && !info.IsDir() {
		directory = filepath.Dir(resolved)
	}
	return directory, codingInstructionDiagnostic{}
}

func resolveCodingTarget(path string) (string, error) {
	cleaned := filepath.Clean(path)
	for current := cleaned; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			suffix, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", relErr
			}
			if suffix == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, suffix)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if info, lstatErr := os.Lstat(current); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", err
		} else if lstatErr != nil && !os.IsNotExist(lstatErr) {
			return "", lstatErr
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

func (loader *codingInstructionLoader) appendProjectChain(
	bundle *codingInstructionBundle,
	targetDirectory string,
) {
	if !codingPathWithin(targetDirectory, loader.projectRoot) {
		bundle.Diagnostics = append(
			bundle.Diagnostics,
			codingInstructionWarning(targetDirectory, "instruction scope escapes the admitted project root"),
		)
		return
	}
	for _, directory := range codingDirectoryChain(loader.projectRoot, targetDirectory) {
		loader.appendDirectoryInstruction(bundle, directory, loader.projectRoot, false)
	}
}

func (loader *codingInstructionLoader) appendDirectoryInstruction(
	bundle *codingInstructionBundle,
	directory string,
	authorityRoot string,
	global bool,
) {
	document, diagnostic, found := loader.loadDirectoryInstruction(directory, authorityRoot, global)
	if diagnostic.Message != "" {
		bundle.Diagnostics = append(bundle.Diagnostics, diagnostic)
	}
	if found {
		bundle.Documents = append(bundle.Documents, document)
	}
}

func (loader *codingInstructionLoader) loadDirectoryInstruction(
	directory string,
	authorityRoot string,
	global bool,
) (codingInstructionDocument, codingInstructionDiagnostic, bool) {
	for _, filename := range codingInstructionFilenames {
		path := filepath.Join(directory, filename)
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return codingInstructionDocument{}, codingInstructionWarning(path, err.Error()), false
		}
		document, diagnostic := loader.loadFile(path, directory, authorityRoot, global)
		return document, diagnostic, diagnostic.Message == ""
	}
	return codingInstructionDocument{}, codingInstructionDiagnostic{}, false
}

func (loader *codingInstructionLoader) loadFile(
	path string,
	scope string,
	authorityRoot string,
	global bool,
) (codingInstructionDocument, codingInstructionDiagnostic) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return codingInstructionDocument{}, codingInstructionWarning(path, err.Error())
	}
	resolved = filepath.Clean(resolved)
	if !codingPathWithin(resolved, authorityRoot) {
		return codingInstructionDocument{}, codingInstructionWarning(
			path,
			"instruction file resolves outside its admitted instruction root",
		)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return codingInstructionDocument{}, codingInstructionWarning(path, err.Error())
	}
	if !info.Mode().IsRegular() {
		return codingInstructionDocument{}, codingInstructionWarning(path, "instruction path is not a regular file")
	}

	loader.mu.Lock()
	defer loader.mu.Unlock()
	if cached, ok := loader.cache[path]; ok && cached.resolvedPath == resolved &&
		os.SameFile(cached.info, info) && cached.info.Size() == info.Size() &&
		cached.info.ModTime().Equal(info.ModTime()) && cached.info.Mode() == info.Mode() {
		return cached.document, codingInstructionDiagnostic{}
	}

	file, err := os.Open(resolved)
	if err != nil {
		return codingInstructionDocument{}, codingInstructionWarning(path, err.Error())
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(loader.maxFileBytes+1)))
	if err != nil {
		return codingInstructionDocument{}, codingInstructionWarning(path, err.Error())
	}
	truncated := len(data) > loader.maxFileBytes
	if truncated {
		data = truncateCodingInstructionBytes(data, loader.maxFileBytes)
	}
	content := strings.TrimSpace(string(data))
	keyMaterial := strings.Join([]string{
		resolved,
		path,
		scope,
		strconv.FormatBool(global),
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
		fmt.Sprintf("%x", sha256.Sum256(data)),
	}, "\x00")
	keyHash := sha256.Sum256([]byte(keyMaterial))
	document := codingInstructionDocument{
		Key:       base64.RawURLEncoding.EncodeToString(keyHash[:]),
		Path:      path,
		Scope:     scope,
		Label:     filepath.Base(path),
		Content:   content,
		Global:    global,
		Truncated: truncated,
	}
	loader.cache[path] = codingInstructionCacheEntry{
		resolvedPath: resolved,
		info:         info,
		document:     document,
	}
	return document, codingInstructionDiagnostic{}
}

func (loader *codingInstructionLoader) boundBundle(bundle codingInstructionBundle) codingInstructionBundle {
	bundle.Documents = uniqueCodingInstructionDocuments(bundle.Documents)
	bundle.Diagnostics = uniqueCodingInstructionDiagnostics(bundle.Diagnostics)
	remaining := loader.maxTotalBytes
	retained := make([]codingInstructionDocument, 0, len(bundle.Documents))
	for index := len(bundle.Documents) - 1; index >= 0; index-- {
		document := bundle.Documents[index]
		if remaining <= 0 {
			bundle.Diagnostics = append(bundle.Diagnostics, codingInstructionWarning(
				document.Path,
				"instruction omitted because the total instruction byte limit was reached",
			))
			continue
		}
		if len(document.Content) > remaining {
			document.Content = string(truncateCodingInstructionBytes([]byte(document.Content), remaining))
			document.Truncated = true
			bundle.Diagnostics = append(bundle.Diagnostics, codingInstructionWarning(
				document.Path,
				"instruction truncated because the total instruction byte limit was reached",
			))
		}
		remaining -= len(document.Content)
		retained = append(retained, document)
	}
	for left, right := 0, len(retained)-1; left < right; left, right = left+1, right-1 {
		retained[left], retained[right] = retained[right], retained[left]
	}
	bundle.Documents = retained
	bundle.Diagnostics = uniqueCodingInstructionDiagnostics(bundle.Diagnostics)
	return bundle
}

func renderCodingInstructionBundle(bundle codingInstructionBundle, late bool) string {
	if len(bundle.Documents) == 0 && len(bundle.Diagnostics) == 0 {
		return ""
	}
	var builder strings.Builder
	if late {
		builder.WriteString("# Newly discovered project instructions\n\n")
		builder.WriteString(
			"The requested tool was not executed. Review these scoped instructions, then retry the tool call.\n",
		)
	} else {
		builder.WriteString("# Project instructions\n\n")
		builder.WriteString(
			"Instructions are ordered from lower to higher precedence. A nested block applies only to files " +
				"inside its declared scope; later applicable blocks override earlier ones.\n",
		)
	}

	for _, document := range bundle.Documents {
		builder.WriteString("\n## ")
		builder.WriteString(document.Label)
		builder.WriteString("\n\nScope: ")
		if document.Global {
			builder.WriteString("all files in this coding project (global coding instructions)")
		} else {
			builder.WriteString(document.Scope)
		}
		builder.WriteString("\nSource: ")
		builder.WriteString(document.Path)
		if document.Truncated {
			builder.WriteString("\nStatus: truncated to the configured byte limit")
		}
		builder.WriteString("\n\n")
		builder.WriteString(document.Content)
		builder.WriteString("\n")
		builder.WriteString(codingInstructionMarkerPrefix)
		builder.WriteString(document.Key)
		builder.WriteString(codingInstructionMarkerSuffix)
		builder.WriteString("\n")
	}
	if len(bundle.Diagnostics) > 0 {
		builder.WriteString("\n## Instruction loading warnings\n")
		for _, diagnostic := range bundle.Diagnostics {
			builder.WriteString("\n- ")
			builder.WriteString(diagnostic.Message)
			builder.WriteString("\n")
			builder.WriteString(codingInstructionMarkerPrefix)
			builder.WriteString(diagnostic.Key)
			builder.WriteString(codingInstructionMarkerSuffix)
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func codingInstructionKeys(bundle codingInstructionBundle) []string {
	keys := make([]string, 0, len(bundle.Documents)+len(bundle.Diagnostics))
	for _, document := range bundle.Documents {
		keys = append(keys, document.Key)
	}
	for _, diagnostic := range bundle.Diagnostics {
		keys = append(keys, diagnostic.Key)
	}
	return keys
}

func codingInstructionKeysFromMessages(messages []providers.Message) []string {
	seen := make(map[string]struct{})
	for _, message := range messages {
		content := message.Content
		for {
			start := strings.Index(content, codingInstructionMarkerPrefix)
			if start < 0 {
				break
			}
			content = content[start+len(codingInstructionMarkerPrefix):]
			end := strings.Index(content, codingInstructionMarkerSuffix)
			if end < 0 {
				break
			}
			key := strings.TrimSpace(content[:end])
			if key != "" {
				seen[key] = struct{}{}
			}
			content = content[end+len(codingInstructionMarkerSuffix):]
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func codingInstructionWarning(path, reason string) codingInstructionDiagnostic {
	message := fmt.Sprintf("Could not load project instructions at %s: %s", path, strings.TrimSpace(reason))
	hash := sha256.Sum256([]byte(message))
	return codingInstructionDiagnostic{
		Key:     base64.RawURLEncoding.EncodeToString(hash[:]),
		Message: message,
	}
}

func codingDirectoryChain(root, target string) []string {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if !codingPathWithin(target, root) {
		return nil
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." {
		return []string{root}
	}
	directories := []string{root}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		directories = append(directories, current)
	}
	return directories
}

func codingPathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func codingPathDepth(path string) int {
	volume := filepath.VolumeName(path)
	path = strings.TrimPrefix(filepath.Clean(path), volume)
	components := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	return len(components)
}

func appendUniqueCodingPath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

func uniqueCodingInstructionDocuments(documents []codingInstructionDocument) []codingInstructionDocument {
	seen := make(map[string]struct{}, len(documents))
	result := make([]codingInstructionDocument, 0, len(documents))
	for _, document := range documents {
		if _, ok := seen[document.Path]; ok {
			continue
		}
		seen[document.Path] = struct{}{}
		result = append(result, document)
	}
	return result
}

func uniqueCodingInstructionDiagnostics(
	diagnostics []codingInstructionDiagnostic,
) []codingInstructionDiagnostic {
	seen := make(map[string]struct{}, len(diagnostics))
	result := make([]codingInstructionDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if diagnostic.Message == "" {
			continue
		}
		if _, ok := seen[diagnostic.Key]; ok {
			continue
		}
		seen[diagnostic.Key] = struct{}{}
		result = append(result, diagnostic)
	}
	return result
}

func truncateCodingInstructionBytes(data []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(data) <= limit {
		return append([]byte(nil), data...)
	}
	data = append([]byte(nil), data[:limit]...)
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return data
}
