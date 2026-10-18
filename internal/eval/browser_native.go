package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

const maxBrowserOutboxBytes = 16 * 1024 * 1024

type browserNativeSnapshot struct {
	state        browser.StateSnapshot
	stateSHA256  string
	outboxSHA256 string
	intents      []outbox.Intent
}

// Capture only after the owned child is reaped. Every original record is
// scanned before native decoding; no store constructor, lock or cleanup runs.
func observeBrowserNative(
	ctx context.Context, workspace string, facts map[string]string, filter *strings.Replacer,
) (browserNativeSnapshot, error) {
	var observed browserNativeSnapshot
	if ctx == nil || ctx.Err() != nil {
		return observed, errNativeEvidence
	}
	stateRoot, err := openBrowserEvidenceRoot(workspace, "state/browser")
	if err != nil {
		return observed, errNativeEvidence
	}
	data, _, err := readCensusFile(stateRoot, "browser.json", browser.DefaultFileStoreBytes)
	_ = stateRoot.Close()
	if err != nil {
		return observed, errNativeEvidence
	}
	defer clear(data)
	if err = inspectNativeJSON(data, facts, filter); err != nil {
		return observed, err
	}
	observed.state, err = browser.InspectStateSnapshot(data)
	if err != nil {
		return browserNativeSnapshot{}, errNativeEvidence
	}
	observed.stateSHA256 = digest(data)
	root, err := openBrowserEvidenceRoot(workspace, "state/outbox")
	if err != nil {
		return browserNativeSnapshot{}, errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	entries, err := censusDirectoryEntries(root, maxPDFCensusEntries)
	if err != nil {
		return browserNativeSnapshot{}, errNativeEvidence
	}
	identities := make(map[string]os.FileInfo, len(entries))
	hashes := make(map[string]string, len(entries))
	remaining := maxBrowserOutboxBytes
	for _, entry := range entries {
		if ctx.Err() != nil {
			return browserNativeSnapshot{}, errNativeEvidence
		}
		name := entry.Name()
		if containsPrivate(name, facts) || filter != nil && filter.Replace(name) != name {
			return browserNativeSnapshot{}, ErrPrivateEvidence
		}
		id := strings.TrimSuffix(name, ".json")
		lock := name == ".writer.lock"
		if !lock && (name == id || !validID(id)) {
			return browserNativeSnapshot{}, errNativeEvidence
		}
		raw, info, readErr := readCensusFile(root, name, min(int64(remaining), outbox.MaxInspectionBytes))
		if readErr != nil {
			return browserNativeSnapshot{}, errNativeEvidence
		}
		remaining -= len(raw)
		identities[name] = info
		if !lock || len(raw) != 0 {
			err = inspectNativeJSON(raw, facts, filter)
		}
		if err == nil && lock && len(raw) != 0 {
			err = errNativeEvidence
		}
		if err == nil && !lock {
			// Native intent loading allows unknown JSON members. Refuse lossy
			// duplicate/unknown fields here rather than silently discard evidence.
			_, err = decodeDocument[outbox.Intent](raw, outbox.MaxInspectionBytes)
			if err == nil {
				var intent outbox.Intent
				intent, err = outbox.InspectIntent(raw, id)
				if err == nil {
					observed.intents = append(observed.intents, intent)
					hashes[id] = digest(raw)
				}
			}
		}
		clear(raw)
		if err != nil {
			if errors.Is(err, ErrPrivateEvidence) {
				return browserNativeSnapshot{}, err
			}
			return browserNativeSnapshot{}, errNativeEvidence
		}
	}
	current, err := censusDirectoryEntries(root, maxPDFCensusEntries)
	if err != nil || len(current) != len(entries) {
		return browserNativeSnapshot{}, errNativeEvidence
	}
	for _, entry := range current {
		info, statErr := root.Lstat(entry.Name())
		if statErr != nil || !sameCensusFile(identities[entry.Name()], info) {
			return browserNativeSnapshot{}, errNativeEvidence
		}
	}
	encoded, err := json.Marshal(hashes)
	if err != nil {
		return browserNativeSnapshot{}, errNativeEvidence
	}
	observed.outboxSHA256 = digest(encoded)
	return observed, nil
}

func openBrowserEvidenceRoot(home, path string) (*os.Root, error) {
	parent, err := os.OpenRoot(home)
	if err != nil {
		return nil, errNativeEvidence
	}
	defer func() { _ = parent.Close() }()
	prefix := ""
	for _, component := range strings.Split(path, "/") {
		prefix = filepath.Join(prefix, component)
		info, statErr := parent.Lstat(prefix)
		if statErr != nil || !info.IsDir() {
			return nil, errNativeEvidence
		}
	}
	before, err := parent.Lstat(path)
	if err != nil || before.Mode().Perm() != 0o700 {
		return nil, errNativeEvidence
	}
	root, err := parent.OpenRoot(path)
	if err != nil {
		return nil, errNativeEvidence
	}
	opened, openErr := root.Stat(".")
	after, pathErr := parent.Lstat(path)
	if openErr != nil || pathErr != nil || !sameCensusFile(before, opened) || !sameCensusFile(opened, after) {
		_ = root.Close()
		return nil, errNativeEvidence
	}
	return root, nil
}

func browserTemporaryRootsEmpty(home string) bool {
	for _, path := range []string{browserRuntimeDirectory + "/ephemeral", ownedGatewayTempDirectory} {
		root, err := openBrowserEvidenceRoot(home, path)
		if err != nil {
			return false
		}
		before, beforeErr := root.Stat(".")
		entries, readErr := censusDirectoryEntries(root, 0)
		after, afterErr := root.Stat(".")
		closeErr := root.Close()
		if beforeErr != nil || readErr != nil || afterErr != nil || closeErr != nil || len(entries) != 0 ||
			!sameCensusFile(before, after) {
			return false
		}
	}
	return true
}
