package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

const (
	maxPDFCensusEntries = 2048
	maxPDFCensusBytes   = 16 * 1024 * 1024
)

type pdfCensusFile struct {
	kind, id string
	data     []byte
}

// Raw baseline bytes remain private until native owner validation is possible.
// A directory snapshot is an observation, never a fence against future work.
type pdfNativeInventory struct {
	files []pdfCensusFile
	bytes int
}

type pdfNativeCensus struct {
	before *pdfNativeInventory
	err    error
}

func (inventory *pdfNativeInventory) close() {
	if inventory == nil {
		return
	}
	for _, file := range inventory.files {
		clear(file.data)
	}
	inventory.files = nil
}

func (prepared *pdfTrial) beginNativeCensus(ctx context.Context) error {
	if prepared.census != nil {
		return errNativeEvidence
	}
	before, err := prepared.nativeInventory(ctx)
	prepared.census = &pdfNativeCensus{before: before, err: err}
	return err
}

func (prepared *pdfTrial) nativeInventory(ctx context.Context) (*pdfNativeInventory, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errNativeEvidence
	}
	inventory := &pdfNativeInventory{}
	var failure error
	for _, surface := range []struct {
		home, path, kind string
		maximum          int64
	}{
		{prepared.home, "state/document-writes/journal", "write", document.DefaultMaxWriteJournalRecord},
		{prepared.workspace, "state/outbox", "outbox", outbox.MaxInspectionBytes},
	} {
		if err := prepared.inventoryDirectory(
			ctx,
			inventory,
			surface.home,
			surface.path,
			surface.kind,
			surface.maximum,
		); err != nil {
			if failure == nil || errors.Is(err, ErrPrivateEvidence) {
				failure = err
			}
		}
	}
	if failure != nil {
		inventory.close()
		return nil, failure
	}
	return inventory, nil
}

func (prepared *pdfTrial) inventoryDirectory(
	ctx context.Context, inventory *pdfNativeInventory, home, path, kind string, maximum int64,
) error {
	if ctx.Err() != nil {
		return errNativeEvidence
	}
	parent, err := os.OpenRoot(home)
	if err != nil {
		return errNativeEvidence
	}
	defer func() { _ = parent.Close() }()
	// Refuse even an in-root alias of the native directory hierarchy.
	prefix := ""
	for _, component := range strings.Split(path, "/") {
		prefix = filepath.Join(prefix, component)
		info, statErr := parent.Lstat(prefix)
		if statErr != nil || !info.IsDir() {
			return errNativeEvidence
		}
	}
	directoryInfo, err := parent.Lstat(path)
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		return errNativeEvidence
	}
	root, err := parent.OpenRoot(path)
	if err != nil {
		return errNativeEvidence
	}
	defer func() { _ = root.Close() }()
	entries, err := censusDirectoryEntries(root, maxPDFCensusEntries-len(inventory.files))
	if err != nil {
		return err
	}
	infos := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return errNativeEvidence
		}
		name := entry.Name()
		if containsPrivate(name, prepared.item.Case.Driver.Facts) ||
			prepared.filter != nil && prepared.filter.Replace(name) != name {
			return ErrPrivateEvidence
		}
		lock := kind == "write" && name == ".write-journal.lock" ||
			kind == "outbox" && name == ".writer.lock"
		id := strings.TrimSuffix(name, ".json")
		if !lock && (name == id || !validID(id)) {
			return errNativeEvidence
		}
		data, info, readErr := readCensusFile(root, name, min(maximum, int64(maxPDFCensusBytes-inventory.bytes)))
		if readErr != nil {
			return readErr
		}
		if inventory.bytes+len(data) > maxPDFCensusBytes {
			clear(data)
			return errNativeEvidence
		}
		if lock {
			err = nil
			if len(data) != 0 {
				err = prepared.scanNativeSurface(data)
				if err == nil {
					err = errNativeEvidence
				}
			}
		} else {
			err = prepared.scanNativeSurface(data)
			if err == nil {
				decoder := json.NewDecoder(bytes.NewReader(data))
				value, decodeErr := decodeJSONValue(decoder, 0, true)
				_, object := value.(map[string]any)
				if _, endErr := decoder.Token(); decodeErr != nil || !object || !errors.Is(endErr, io.EOF) {
					err = errNativeEvidence
				}
			}
			if err == nil && kind == "outbox" {
				_, err = outbox.InspectIntent(data, id)
			}
		}
		if err != nil {
			clear(data)
			if errors.Is(err, ErrPrivateEvidence) {
				return err
			}
			return errNativeEvidence
		}
		infos[name] = info
		if lock {
			clear(data)
			continue
		}
		inventory.files = append(inventory.files, pdfCensusFile{kind: kind, id: id, data: data})
		inventory.bytes += len(data)
	}
	current, err := censusDirectoryEntries(root, maxPDFCensusEntries)
	if err != nil || len(current) != len(entries) {
		return errNativeEvidence
	}
	for _, entry := range current {
		previous := infos[entry.Name()]
		info, statErr := root.Lstat(entry.Name())
		if statErr != nil || !sameCensusFile(previous, info) {
			return errNativeEvidence
		}
	}
	currentDirectory, err := parent.Lstat(path)
	if err != nil || !sameCensusFile(directoryInfo, currentDirectory) {
		return errNativeEvidence
	}
	return nil
}

func censusDirectoryEntries(root *os.Root, maximum int) ([]os.DirEntry, error) {
	if maximum < 0 || maximum > maxPDFCensusEntries {
		return nil, errNativeEvidence
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, errNativeEvidence
	}
	entries, readErr := directory.ReadDir(maximum + 1)
	closeErr := directory.Close()
	if len(entries) > maximum ||
		readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errNativeEvidence
	}
	return entries, nil
}

func readCensusFile(root *os.Root, name string, maximum int64) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 ||
		before.Size() < 0 || before.Size() > maximum {
		return nil, nil, errNativeEvidence
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, errNativeEvidence
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !sameCensusFile(before, opened) {
		return nil, nil, errNativeEvidence
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	after, statErr := file.Stat()
	current, pathErr := root.Lstat(name)
	if readErr != nil || statErr != nil || pathErr != nil || len(data) > int(maximum) ||
		!sameCensusFile(opened, after) || !sameCensusFile(after, current) || int64(len(data)) != after.Size() {
		clear(data)
		return nil, nil, errNativeEvidence
	}
	return data, after, nil
}

func sameCensusFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func (inventory *pdfNativeInventory) fingerprint() string {
	entries := make([]struct {
		Kind, ID, SHA256 string
	}, 0, len(inventory.files))
	for _, file := range inventory.files {
		entries = append(entries, struct{ Kind, ID, SHA256 string }{file.kind, file.id, digest(file.data)})
	}
	slices.SortFunc(entries, func(a, b struct{ Kind, ID, SHA256 string }) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	data, _ := json.Marshal(entries)
	return digest(data)
}
