package nodes

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

var errAnchoredDirectoryLockBusy = errors.New("anchored directory lock is busy")

type anchoredProcessLock struct {
	mu   sync.Mutex
	refs int
}

type anchoredDirectoryIdentity struct {
	volume uint64
	file   uint64
}

type anchoredProcessLockKey struct {
	directory anchoredDirectoryIdentity
	name      string
}

var anchoredProcessLocks = struct {
	sync.Mutex
	entries map[anchoredProcessLockKey]*anchoredProcessLock
}{entries: make(map[anchoredProcessLockKey]*anchoredProcessLock)}

// acquireAnchoredProcessLock serializes blocking file-lock users inside this
// process. File locks still coordinate separate processes, but their behavior
// for independently opened descriptors in one process differs across operating
// systems. The process lock gives callers one consistent critical section.
func acquireAnchoredProcessLock(key anchoredProcessLockKey) func() {
	anchoredProcessLocks.Lock()
	entry := anchoredProcessLocks.entries[key]
	if entry == nil {
		entry = &anchoredProcessLock{}
		anchoredProcessLocks.entries[key] = entry
	}
	entry.refs++
	anchoredProcessLocks.Unlock()

	entry.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			anchoredProcessLocks.Lock()
			entry.refs--
			if entry.refs == 0 {
				delete(anchoredProcessLocks.entries, key)
			}
			anchoredProcessLocks.Unlock()
		})
	}
}

func validateAnchoredName(name string) error {
	if name == "" ||
		name == "." ||
		name == ".." ||
		filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\`) {
		return errors.New("anchored file name must be one path component")
	}
	return nil
}

func randomAnchoredTempName() (string, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return ".node-terminals-" + hex.EncodeToString(suffix[:]) + ".tmp", nil
}
