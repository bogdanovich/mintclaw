// Package modelpicker owns durable, prompt-free preferences for the coding
// model picker. It deliberately stays separate from model configuration and
// thread transcripts.
package modelpicker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const (
	SchemaVersion = 1
	RecentLimit   = 10

	recentFileName  = "model-recents.v1.json"
	recentLockName  = "model-recents.v1.lock"
	maxRecentBytes  = 32 << 10
	maxIdentitySize = 256
	lockTimeout     = 5 * time.Second
	staleLockAge    = 2 * time.Minute
)

// Route is the complete stable identity retained by the picker. It contains
// no endpoint, credential, prompt, or provider-native configuration data.
type Route struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type recentFile struct {
	SchemaVersion int     `json:"schema_version"`
	Recent        []Route `json:"recent"`
}

// Store persists one global coding-model MRU list under the coding state root.
type Store struct {
	root     string
	path     string
	lockPath string
	mu       sync.Mutex
	now      func() time.Time
}

// NewStore validates the state root without creating or modifying it.
func NewStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("coding model recents: state root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("coding model recents: resolve state root: %w", err)
	}
	return &Store{
		root:     filepath.Clean(absolute),
		path:     filepath.Join(absolute, recentFileName),
		lockPath: filepath.Join(absolute, recentLockName),
		now:      time.Now,
	}, nil
}

// Load returns the bounded newest-first route list. A missing file is empty.
func (s *Store) Load() ([]Route, error) {
	if s == nil {
		return nil, fmt.Errorf("coding model recents: store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Record moves route to the front and atomically persists the bounded result.
func (s *Store) Record(route Route) ([]Route, error) {
	if s == nil {
		return nil, fmt.Errorf("coding model recents: store is unavailable")
	}
	route, ok := normalizeRoute(route)
	if !ok {
		return nil, fmt.Errorf("coding model recents: provider and model are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("coding model recents: create state root: %w", err)
	}
	release, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	defer release()

	recent, err := s.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	result := make([]Route, 0, min(RecentLimit, len(recent)+1))
	result = append(result, route)
	key := routeKey(route)
	for _, candidate := range recent {
		if routeKey(candidate) == key {
			continue
		}
		result = append(result, candidate)
		if len(result) == RecentLimit {
			break
		}
	}
	data, err := json.MarshalIndent(recentFile{SchemaVersion: SchemaVersion, Recent: result}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("coding model recents: encode state: %w", err)
	}
	data = append(data, '\n')
	if err = fileutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return nil, fmt.Errorf("coding model recents: persist state: %w", err)
	}
	return append([]Route(nil), result...), nil
}

func (s *Store) load() ([]Route, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("coding model recents: inspect state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecentBytes {
		return nil, fmt.Errorf("coding model recents: state file is invalid")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("coding model recents: read state: %w", err)
	}
	var state recentFile
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("coding model recents: decode state: %w", err)
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("coding model recents: unsupported schema %d", state.SchemaVersion)
	}
	result := make([]Route, 0, min(RecentLimit, len(state.Recent)))
	seen := make(map[string]struct{}, len(state.Recent))
	for _, stored := range state.Recent {
		candidate, ok := normalizeRoute(stored)
		if !ok {
			continue
		}
		key := routeKey(candidate)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
		if len(result) == RecentLimit {
			break
		}
	}
	return result, nil
}

func (s *Store) acquireLock() (func(), error) {
	deadline := s.now().Add(lockTimeout)
	for {
		lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = lock.WriteString(s.now().UTC().Format(time.RFC3339Nano))
			_ = lock.Close()
			return func() { _ = os.Remove(s.lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("coding model recents: acquire state lock: %w", err)
		}
		if info, statErr := os.Stat(s.lockPath); statErr == nil && s.now().Sub(info.ModTime()) > staleLockAge {
			_ = os.Remove(s.lockPath)
			continue
		}
		if !s.now().Before(deadline) {
			return nil, fmt.Errorf("coding model recents: state lock timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func normalizeRoute(route Route) (Route, bool) {
	route.Provider = providers.NormalizeProvider(route.Provider)
	route.Model = strings.TrimSpace(route.Model)
	if route.Provider == "" || route.Model == "" || !utf8.ValidString(route.Model) ||
		len(route.Provider) > maxIdentitySize || len(route.Model) > maxIdentitySize {
		return Route{}, false
	}
	return route, true
}

func routeKey(route Route) string {
	return providers.ModelKey(route.Provider, route.Model)
}
