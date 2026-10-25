package eval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/memory"
)

type nativeSessionHistory struct {
	metadata string
	history  []string
}

// Native history is normally 0644 beneath the admitted private home. Anchor
// every directory and reject links/writable paths instead of chmodding runtime
// files or constructing a recovering session store. Callers bind session scope.
func readNativeSessionHistory(
	ctx context.Context, home, workspace, key string, facts map[string]string, filter *strings.Replacer,
) (nativeSessionHistory, error) {
	physical, err := privateHome(home)
	if err != nil || !pathWithin(physical, workspace) {
		return nativeSessionHistory{}, errNativeEvidence
	}
	relative, err := filepath.Rel(physical, filepath.Join(workspace, "sessions"))
	if err != nil {
		return nativeSessionHistory{}, errNativeEvidence
	}
	root, err := os.OpenRoot(physical)
	if err != nil {
		return nativeSessionHistory{}, errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, component)
		info, statErr := root.Lstat(current)
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return nativeSessionHistory{}, errNativeEvidence
		}
	}
	store, err := root.OpenRoot(relative)
	if err != nil {
		return nativeSessionHistory{}, errNativeEvidence
	}
	defer func() { _ = store.Close() }()
	historyName, metaName, err := memory.JSONLSessionFiles(key)
	if err != nil {
		return nativeSessionHistory{}, errNativeEvidence
	}
	read := func(name string, maximum int64, jsonLines bool) ([]byte, error) {
		info, statErr := store.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return nil, errNativeEvidence
		}
		data, readErr := readBoundedFile(store, name, maximum)
		if readErr == nil && jsonLines {
			scan := privateContentScan{facts: facts, filter: filter, tokens: 32768}
			if scan.jsonLines(data) {
				readErr = errNativeEvidence
				if !scan.uncertain {
					readErr = ErrPrivateEvidence
				}
			}
		} else if readErr == nil {
			readErr = inspectNativeJSON(data, facts, filter)
		}
		if readErr != nil {
			clear(data)
			return nil, readErr
		}
		return data, nil
	}
	before, err := read(metaName, MaxManifestBytes, false)
	if err != nil {
		return nativeSessionHistory{}, err
	}
	defer clear(before)
	history, err := read(historyName, int64(memory.MaxJSONLInspectionBytes-len(before)), true)
	if err != nil {
		return nativeSessionHistory{}, err
	}
	defer clear(history)
	after, err := read(metaName, MaxManifestBytes, false)
	if err != nil {
		return nativeSessionHistory{}, err
	}
	defer clear(after)
	if !bytes.Equal(before, after) {
		return nativeSessionHistory{}, errNativeEvidence
	}
	if _, err = memory.InspectJSONLSnapshot(ctx, key, before, history); err != nil {
		return nativeSessionHistory{}, err
	}
	return nativeSessionHistory{metadata: string(before), history: strings.Split(string(history), "\n")}, nil
}
