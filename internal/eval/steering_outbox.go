package eval

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

func readSteeringOutbox(ctx context.Context, target admittedTarget) ([]SteeringOutboxEvidence, []string, error) {
	root, err := os.OpenRoot(target.preset.IsolationRoot)
	if err != nil {
		return nil, nil, errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	for _, directory := range []string{"workspace", "workspace/state", "workspace/state/outbox"} {
		info, statErr := root.Lstat(directory)
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return nil, nil, errNativeEvidence
		}
	}
	store, err := root.OpenRoot("workspace/state/outbox")
	if err != nil {
		return nil, nil, errNativeEvidence
	}
	defer func() { _ = store.Close() }()
	directory, err := store.Open(".")
	if err != nil {
		return nil, nil, errNativeEvidence
	}
	entries, err := directory.ReadDir(129)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > 128 {
		return nil, nil, errNativeEvidence
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	rows, names, total := []SteeringOutboxEvidence{}, []string{}, 0
	for _, entry := range entries {
		if entry.Name() == ".writer.lock" {
			continue
		}
		if ctx.Err() != nil || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, nil, errNativeEvidence
		}
		data, _, err := readCensusFile(store, entry.Name(), MaxManifestBytes)
		if err != nil {
			return nil, nil, errNativeEvidence
		}
		total += len(data)
		if total > 2*1024*1024 {
			clear(data)
			return nil, nil, errNativeEvidence
		}
		if err := inspectNativeJSON(data, nil, target.config.SensitiveDataReplacer()); err != nil {
			clear(data)
			return nil, nil, err
		}
		if _, err := outbox.InspectIntent(data, strings.TrimSuffix(entry.Name(), ".json")); err != nil {
			clear(data)
			return nil, nil, errNativeEvidence
		}
		rows = append(rows, SteeringOutboxEvidence{File: entry.Name(), JSON: string(data), SHA256: digest(data)})
		names = append(names, entry.Name())
		clear(data)
	}
	return rows, names, nil
}
