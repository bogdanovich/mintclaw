package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type LoadedCase struct {
	Case   Case
	Path   string
	Digest string
	Root   string
	Policy *SuitePolicy
}

type Selection struct {
	Suite       string
	Tags        []string
	IDs         []string
	DefaultOnly bool
}

const (
	maxCorpusEntries = 4096
	corpusReadBatch  = 128
)

func LoadCorpus(directory string) ([]LoadedCase, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrInvalidCorpus
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, ErrInvalidCorpus
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrInvalidCorpus
	}
	defer func() { _ = root.Close() }()
	items := make([]LoadedCase, 0)
	seen := make(map[string]bool)
	err = walkCorpusFiles(root, func(name string) error {
		if path.Ext(name) != ".json" {
			return nil
		}
		if len(items) >= MaxCorpusCases {
			return ErrInvalidCorpus
		}
		data, readErr := readBoundedFile(root, name, MaxManifestBytes)
		if readErr != nil {
			return ErrInvalidCorpus
		}
		item, decodeErr := DecodeManifest[Case](data)
		if decodeErr != nil || item.Validate() != nil || seen[item.ID] {
			return ErrInvalidCorpus
		}
		for _, fixture := range item.Fixtures {
			if verifyFixture(root, fixture) != nil {
				return ErrInvalidCorpus
			}
		}
		seen[item.ID] = true
		items = append(items, LoadedCase{Case: item, Path: name, Digest: digest(data), Root: directory})
		return nil
	})
	if err != nil || len(items) == 0 {
		return nil, ErrInvalidCorpus
	}
	if loadCatalogPolicy(root, items) != nil {
		return nil, ErrInvalidCorpus
	}
	slices.SortFunc(items, func(a, b LoadedCase) int { return strings.Compare(a.Case.ID, b.Case.ID) })
	return items, nil
}

// Directory contents are admitted in batches before being retained. WalkDir
// would allocate and sort an entire directory before enforcing this budget.
func walkCorpusFiles(root *os.Root, visit func(string) error) error {
	entries := 1 // The cases directory itself consumes an entry.
	pending := []string{"cases"}
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		before, err := root.Lstat(name)
		if err != nil || !before.IsDir() {
			return ErrInvalidCorpus
		}
		file, err := root.Open(name)
		if err != nil {
			return ErrInvalidCorpus
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.IsDir() || !os.SameFile(before, info) {
			_ = file.Close()
			return ErrInvalidCorpus
		}
		err = consumeCorpusDirectory(file, &entries, func(entry fs.DirEntry) error {
			if entry.Type()&fs.ModeSymlink != 0 {
				return ErrInvalidCorpus
			}
			child := path.Join(name, entry.Name())
			if entry.IsDir() {
				pending = append(pending, child)
				return nil
			}
			return visit(child)
		})
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return ErrInvalidCorpus
		}
	}
	return nil
}

func consumeCorpusDirectory(file fs.ReadDirFile, entries *int, visit func(fs.DirEntry) error) error {
	for {
		batch, err := file.ReadDir(min(corpusReadBatch, maxCorpusEntries-*entries+1))
		for _, entry := range batch {
			*entries++
			if *entries > maxCorpusEntries {
				return ErrInvalidCorpus
			}
			if visit(entry) != nil {
				return ErrInvalidCorpus
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrInvalidCorpus
		}
	}
}

func Select(corpus []LoadedCase, selection Selection) ([]LoadedCase, error) {
	if selection.Suite != "" && selection.Suite != "all" && !validID(selection.Suite) ||
		!validIDs(selection.Tags, 32) || !validIDs(selection.IDs, MaxCorpusCases) {
		return nil, ErrInvalidFilter
	}
	matchedIDs := make(map[string]bool)
	items := make([]LoadedCase, 0)
	for _, item := range corpus {
		policy := item.Availability()
		if !policy.valid() || policy.ID != item.Case.Suite {
			return nil, ErrInvalidFilter
		}
		if selection.DefaultOnly && !policy.Default {
			continue
		}
		if selection.Suite != "" && selection.Suite != "all" && item.Case.Suite != selection.Suite {
			continue
		}
		if len(selection.IDs) > 0 && !slices.Contains(selection.IDs, item.Case.ID) {
			continue
		}
		if !containsAll(item.Case.Tags, selection.Tags) {
			continue
		}
		items = append(items, item)
		matchedIDs[item.Case.ID] = true
	}
	if len(items) == 0 || len(selection.IDs) > 0 && len(matchedIDs) != len(selection.IDs) {
		return nil, ErrInvalidFilter
	}
	return items, nil
}

func containsAll(values, requested []string) bool {
	for _, value := range requested {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func readBoundedFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	before, err := root.Stat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maximum {
		return nil, ErrInvalidCorpus
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum || !os.SameFile(before, info) {
		return nil, ErrInvalidCorpus
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrInvalidCorpus
	}
	return data, nil
}

func verifyFixture(root *os.Root, fixture Fixture) error {
	const maxFixtureBytes = 64 * 1024 * 1024
	before, err := root.Stat(fixture.Path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxFixtureBytes {
		return ErrInvalidCorpus
	}
	file, err := root.Open(fixture.Path)
	if err != nil {
		return ErrInvalidCorpus
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFixtureBytes || !os.SameFile(before, info) {
		return ErrInvalidCorpus
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, maxFixtureBytes+1))
	if err != nil || count > maxFixtureBytes || hex.EncodeToString(hash.Sum(nil)) != fixture.SHA256 {
		return ErrInvalidCorpus
	}
	return nil
}

func safeRelativePath(value string) bool {
	return len(value) <= 256 && fs.ValidPath(value) && value != "." &&
		!strings.ContainsAny(value, "\\:\x00") && !strings.HasPrefix(value, ".")
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
