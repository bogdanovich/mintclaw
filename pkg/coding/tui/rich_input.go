package tui

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"go.mau.fi/util/shlex"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

const (
	largePasteRuneThreshold = 1_000
	maxPastedContentBytes   = 32 << 20
	maxPastedBatchBytes     = 64 << 20
)

type composerAttachment struct {
	placeholder string
	input       frontend.TurnAttachment
	owned       bool
}

type composerSubmission struct {
	input         frontend.TurnInput
	draft         string
	rememberDraft bool
}

func (m *Model) handleRichPaste(message string) (bool, error) {
	if path, contentType, ok := pastedImagePath(message); ok {
		return true, m.addComposerAttachment(path, "", contentType, false, true)
	}
	if utf8.RuneCountInString(message) <= largePasteRuneThreshold {
		return false, nil
	}
	if len(message) > maxPastedContentBytes {
		return true, fmt.Errorf("pasted content exceeds %d MiB", maxPastedContentBytes>>20)
	}
	if len(m.composerAttachments) >= frontend.MaxTurnAttachments {
		return true, fmt.Errorf("a turn supports at most %d attachments", frontend.MaxTurnAttachments)
	}
	if m.ownedAttachmentBytes()+int64(len(message)) > maxPastedBatchBytes {
		return true, fmt.Errorf("pasted content in one draft exceeds %d MiB", maxPastedBatchBytes>>20)
	}
	directory, err := m.ensurePasteDirectory()
	if err != nil {
		return true, err
	}
	number := m.nextPasteNumber + 1
	filename := fmt.Sprintf("pasted-content-%d.txt", number)
	path := filepath.Join(directory, filename)
	if err = os.WriteFile(path, []byte(message), 0o600); err != nil {
		return true, fmt.Errorf("store pasted content: %w", err)
	}
	placeholder := m.uniquePlaceholder(fmt.Sprintf("[Pasted Content %d chars]", utf8.RuneCountInString(message)))
	m.nextPasteNumber = number
	m.composerAttachments = append(m.composerAttachments, composerAttachment{
		placeholder: placeholder,
		input: frontend.TurnAttachment{
			Path:        path,
			Filename:    filename,
			ContentType: "text/plain; charset=utf-8",
		},
		owned: true,
	})
	m.composer.InsertString(placeholder)
	return true, nil
}

func (m *Model) addComposerAttachment(path, displayName, contentType string, owned, image bool) error {
	if len(m.composerAttachments) >= frontend.MaxTurnAttachments {
		return fmt.Errorf("a turn supports at most %d attachments", frontend.MaxTurnAttachments)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("attach %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("attach %q: not a regular file", path)
	}
	if displayName == "" {
		displayName = filepath.Base(path)
	}
	var placeholder string
	if image {
		m.nextImageNumber++
		placeholder = fmt.Sprintf("[Image #%d]", m.nextImageNumber)
	} else {
		placeholder = m.uniquePlaceholder(fmt.Sprintf("[File: %s]", displayName))
	}
	m.composerAttachments = append(m.composerAttachments, composerAttachment{
		placeholder: placeholder,
		input: frontend.TurnAttachment{
			Path:        path,
			Filename:    displayName,
			ContentType: contentType,
		},
		owned: owned,
	})
	m.composer.InsertString(placeholder)
	return nil
}

func (m *Model) uniquePlaceholder(base string) string {
	draft := m.composer.Value()
	if !strings.Contains(draft, base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s #%d", base, suffix)
		if !strings.Contains(draft, candidate) {
			return candidate
		}
	}
}

func (m *Model) ownedAttachmentBytes() int64 {
	var total int64
	for _, attachment := range m.composerAttachments {
		if !attachment.owned {
			continue
		}
		if info, err := os.Stat(attachment.input.Path); err == nil {
			total += info.Size()
		}
	}
	return total
}

func (m *Model) ensurePasteDirectory() (string, error) {
	if m.pasteDirectory != "" {
		return m.pasteDirectory, nil
	}
	directory, err := os.MkdirTemp("", "mintclaw-coding-paste-*")
	if err != nil {
		return "", fmt.Errorf("create private paste directory: %w", err)
	}
	if err = os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("protect private paste directory: %w", err)
	}
	m.pasteDirectory = directory
	return directory, nil
}

func (m *Model) pruneDetachedAttachments() {
	draft := m.composer.Value()
	retained := m.composerAttachments[:0]
	for _, attachment := range m.composerAttachments {
		if strings.Contains(draft, attachment.placeholder) {
			retained = append(retained, attachment)
			continue
		}
		m.removeComposerAttachment(attachment)
	}
	m.composerAttachments = retained
}

func (m *Model) prepareSubmission(draft string) composerSubmission {
	m.pruneDetachedAttachments()
	input := frontend.TurnInput{Text: unescapeSlashPrompt(draft)}
	for _, attachment := range m.composerAttachments {
		input.Text = strings.Replace(input.Text, attachment.placeholder, "", 1)
		input.Attachments = append(input.Attachments, attachment.input)
	}
	if len(input.Attachments) > 0 {
		input.Text = strings.TrimSpace(input.Text)
	}
	return composerSubmission{
		input:         input,
		draft:         draft,
		rememberDraft: len(input.Attachments) == 0,
	}
}

func (m *Model) clearSubmittedAttachments() {
	for _, attachment := range m.composerAttachments {
		m.removeComposerAttachment(attachment)
	}
	m.composerAttachments = nil
	if m.pasteDirectory != "" {
		if err := os.Remove(m.pasteDirectory); err == nil || errors.Is(err, os.ErrNotExist) {
			m.pasteDirectory = ""
		}
	}
	m.nextPasteNumber = 0
	m.nextImageNumber = 0
}

func (m *Model) removeComposerAttachment(attachment composerAttachment) {
	if attachment.owned {
		_ = os.Remove(attachment.input.Path)
	}
}

func (m *Model) closeRichInput() error {
	if m.pasteDirectory == "" {
		return nil
	}
	directory := m.pasteDirectory
	attachments := m.composerAttachments
	m.pasteDirectory = ""
	m.composerAttachments = nil
	for _, attachment := range attachments {
		m.removeComposerAttachment(attachment)
	}
	if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private paste directory: %w", err)
	}
	return nil
}

func normalizeAttachmentPath(value string) (string, error) {
	paths, err := normalizeAttachmentPaths(value)
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", errors.New("attachment must name exactly one local path")
	}
	return paths[0], nil
}

func normalizeAttachmentPaths(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("attachment path is required")
	}
	unquoted := value
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && len(value) >= 2 {
		unquoted = value[1 : len(value)-1]
	} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
		unquoted = value[1 : len(value)-1]
	}
	if isWindowsPath(unquoted) {
		return []string{filepath.Clean(unquoted)}, nil
	}
	parts, err := shlex.Split(value)
	if err != nil {
		return nil, fmt.Errorf("parse attachment path: %w", err)
	}
	if len(parts) == 0 {
		return nil, errors.New("attachment path is required")
	}
	for index := range parts {
		parts[index], err = normalizeAttachmentToken(parts[index])
		if err != nil {
			return nil, err
		}
	}
	return parts, nil
}

func normalizeAttachmentToken(value string) (string, error) {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme == "file" {
		path, pathErr := url.PathUnescape(parsed.Path)
		if pathErr != nil {
			return "", fmt.Errorf("decode attachment file URL: %w", pathErr)
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", errors.New("remote file URLs are not supported")
		}
		if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		return filepath.Clean(path), nil
	}
	return filepath.Clean(value), nil
}

func isWindowsPath(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func pastedImagePath(value string) (string, string, bool) {
	path, err := normalizeAttachmentPath(value)
	if err != nil {
		return "", "", false
	}
	contentType, ok := supportedImageContentType(path)
	if !ok {
		return "", "", false
	}
	info, err := os.Stat(path)
	return path, contentType, err == nil && info.Mode().IsRegular()
}

func supportedImageContentType(path string) (string, bool) {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return contentType, true
	default:
		return "", false
	}
}
