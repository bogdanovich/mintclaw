//go:build linux || darwin

package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func privateTokenDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if os.Chmod(dir, 0o700) != nil {
		t.Fatal("private token fixture directory unavailable")
	}
	return dir
}

func TestPrivateTokenStoreConsumesWithoutReturningOrRetainingValue(t *testing.T) {
	dir := privateTokenDirectory(t)
	token := []byte("synthetic-private-service-token-T18")
	if StoreServiceAccountToken(dir, token) != nil {
		t.Fatal("private token provisioning failed")
	}
	var retained []byte
	if UseServiceAccountToken(dir, func(value []byte) error {
		if !bytes.Equal(value, token) {
			t.Fatal("private token binding mismatch")
		}
		retained = value
		return nil
	}) != nil || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("private token was unavailable or retained after consumption")
	}
	if !errors.Is(StoreServiceAccountToken(dir, []byte("replacement-token")), ErrUnavailable) {
		t.Fatal("initial provisioning overwrote an existing token")
	}
}

func TestPrivateTokenStoreRejectsUnsafeFilesBeforeConsumption(t *testing.T) {
	for _, kind := range []string{"public-file", "symlink", "hardlink", "directory", "oversized", "newline"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateTokenDirectory(t)
			path := filepath.Join(dir, serviceAccountTokenFile)
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0o600)
			case "symlink", "hardlink":
				other := filepath.Join(dir, "other")
				err = os.WriteFile(other, []byte("synthetic-token"), 0o600)
				if err == nil && kind == "symlink" {
					err = os.Symlink(other, path)
				} else if err == nil {
					err = os.Link(other, path)
				}
			default:
				value := []byte("synthetic-token")
				switch kind {
				case "oversized":
					value = bytes.Repeat([]byte("x"), maxTokenBytes+1)
				case "newline":
					value = append(value, '\n')
				}
				err = os.WriteFile(path, value, 0o600)
				if err == nil && kind == "public-file" {
					err = os.Chmod(path, 0o644)
				}
			}
			if err != nil {
				t.Fatal("unsafe token fixture unavailable")
			}
			used := false
			if !errors.Is(
				UseServiceAccountToken(dir, func([]byte) error { used = true; return nil }),
				ErrUnavailable,
			) ||
				used {
				t.Fatal("unsafe token file reached the private consumer")
			}
		})
	}
}

func TestPrivateTokenConsumerErrorsAndPanicsHaveFixedOutputAndCleanup(t *testing.T) {
	dir := privateTokenDirectory(t)
	if StoreServiceAccountToken(dir, []byte("synthetic-service-token-E82")) != nil {
		t.Fatal("private token fixture provisioning failed")
	}
	for _, panicConsumer := range []bool{false, true} {
		var retained []byte
		err := UseServiceAccountToken(dir, func(value []byte) error {
			retained = value
			if panicConsumer {
				panic(string(value))
			}
			return errors.New(string(value))
		})
		if !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() ||
			!bytes.Equal(retained, make([]byte, len(retained))) {
			t.Fatal("consumer failure exposed or retained private token data")
		}
	}
}
