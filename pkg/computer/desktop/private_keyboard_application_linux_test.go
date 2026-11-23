//go:build linux && mintclaw_private_native_keyboard

package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func privateApplicationArtifactMatches(path, expected string) bool {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "owned application fixture artifact")
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 4096 {
		return false
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return false
	}
	defer clear(data)
	after, err := file.Stat()
	named, nameErr := os.Lstat(path)
	if err != nil || nameErr != nil || !os.SameFile(before, after) || !os.SameFile(after, named) ||
		before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) == expected
}

func TestPrivateKeyboardApplicationArtifactRequiresExactOwnedRegularBytes(t *testing.T) {
	data := []byte("private synthetic application artifact")
	digest := sha256.Sum256(data)
	expected := hex.EncodeToString(digest[:])
	for _, kind := range []string{"exact", "wrong", "large", "symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "artifact")
			original := filepath.Join(directory, "original")
			if err := os.WriteFile(original, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "exact":
				err = os.WriteFile(path, data, 0o600)
			case "wrong":
				err = os.WriteFile(path, []byte("different"), 0o600)
			case "large":
				err = os.WriteFile(path, make([]byte, 4097), 0o600)
			case "symlink":
				err = os.Symlink(original, path)
			case "hardlink":
				err = os.Link(original, path)
			case "fifo":
				err = unix.Mkfifo(path, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if privateApplicationArtifactMatches(path, expected) != (kind == "exact") {
				t.Fatal("artifact kind was not verified independently", kind)
			}
		})
	}
}

// Supplementary private smoke only. The external controller owns the virtual
// server, application and synthetic artifact. No production host is installed.
func TestPrivateNativeApplicationHostQualification(t *testing.T) {
	if os.Getenv("MINTCLAW_PRIVATE_NATIVE_APP_TEST") != "1" {
		t.Skip("explicit disposable application fixture required")
	}
	var fixture struct {
		Window       WindowRequest `json:"window"`
		ServerPID    int32         `json:"server_pid"`
		Chords       [][]byte      `json:"chords"`
		Artifact     string        `json:"artifact"`
		ExpectedHash string        `json:"expected_hash"`
	}
	file, err := os.Open(os.Getenv("MINTCLAW_PRIVATE_NATIVE_APP_CONFIG"))
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32*1024 {
		t.Fatal("invalid private fixture configuration file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF || fixture.ServerPID <= 0 ||
		!filepath.IsAbs(fixture.Artifact) || len(fixture.ExpectedHash) != 64 {
		t.Fatal("invalid private fixture configuration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	guard, err := WatchWindow(ctx, fixture.Window)
	if err != nil {
		t.Fatal("original application guard refused", err)
	}
	defer guard.Close()
	lease, err := guard.AcquireInputLease(ctx)
	if err != nil {
		t.Fatal("original application lease refused", err)
	}
	defer lease.Close()
	identity := guard.Identity()
	bootstrap, _, err := privateKeyboardAuthority(lease, fixture.Window, identity)
	clear(bootstrap.Auth[:])
	if err != nil || bootstrap.Server.PID != fixture.ServerPID || bootstrap.Server.UID != uint32(os.Getuid()) {
		t.Fatal("not the original owned fixture server", err)
	}
	channel, err := openPrivateKeyboardChannel(ctx, lease, fixture.Window, identity, privateKeyboardChannelConfig{
		Executable: os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER"),
		SHA256:     os.Getenv("MINTCLAW_PRIVATE_NATIVE_HOST_WORKER_SHA256"), BudgetMS: 30000,
	})
	if err != nil {
		t.Fatal("original native resource channel refused", err)
	}
	defer channel.close()
	actionCtx, actionCancel := context.WithTimeout(ctx, 10*time.Second)
	defer actionCancel()
	owner, err := preparePrivateKeyboard(actionCtx, channel, lease, privateKeyboardConfig{
		Window: fixture.Window, Identity: identity, Chords: fixture.Chords, BudgetMS: 10000,
	})
	if err != nil {
		t.Fatal("actual Go preparation failed", err)
	}
	defer owner.close()
	for owner.receipt == nil {
		if owner.pending == "dispatched" {
			for !privateApplicationArtifactMatches(fixture.Artifact, fixture.ExpectedHash) && actionCtx.Err() == nil {
				time.Sleep(2 * time.Millisecond) // Only the independent fixture artifact is polled.
			}
			if actionCtx.Err() != nil {
				_ = owner.stop()
				_, _ = owner.receive()
				t.Fatal("application effect unverified; Finish withheld")
			}
		}
		if err = owner.allow(actionCtx); err != nil {
			t.Fatal("original Go lease/generation refused a native phase", err)
		}
		if _, err = owner.receive(); err != nil {
			t.Fatal("actual Go native phase failed", err)
		}
	}
	if !owner.receipt.Acknowledged || !owner.receipt.InputQuiescent ||
		owner.receipt.Resources != privateKeyboardRetained || !guard.Alive(ctx) || lease.Check(ctx) != nil {
		t.Fatal("native acknowledgement, retained cleanup and original authority did not agree")
	}
	if owner.close() != nil || channel.close() != nil || !guard.Alive(ctx) || lease.Check(ctx) != nil {
		t.Fatal("worker/action close released the caller's original guard or lease")
	}
	t.Log("actual Go application effect verified; original guard/lease retained; quiescent retained resources")
}
