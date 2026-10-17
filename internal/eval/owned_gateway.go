package eval

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

var ErrOwnedGateway = errors.New("eval: owned gateway could not be admitted or stopped cleanly")

const ownedGatewayLogBytes = 1024 * 1024

// A child receipt is not a document/browser settlement or privacy witness.
type TargetLifecycle struct {
	Mode          string `json:"mode"`
	PID           int    `json:"pid"`
	Admitted      bool   `json:"admitted"`
	StopRequested bool   `json:"stop_requested"`
	ExitObserved  bool   `json:"exit_observed"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	Forced        bool   `json:"forced"`
	LogTruncated  bool   `json:"log_truncated"`
	Code          string `json:"code"`
}

func (receipt *TargetLifecycle) valid() bool {
	if receipt == nil {
		return true
	}
	if receipt.Mode != "owned_gateway" || receipt.PID <= 1 || !receipt.Admitted ||
		!receipt.StopRequested || !receipt.ExitObserved || receipt.ExitCode == nil ||
		*receipt.ExitCode < -1 || *receipt.ExitCode > 255 {
		return false
	}
	switch receipt.Code {
	case "native_exit_zero":
		return !receipt.Forced && *receipt.ExitCode == 0
	case "forced_exit":
		return receipt.Forced
	case "abnormal_exit":
		return !receipt.Forced
	default:
		return false
	}
}

func (receipt *TargetLifecycle) verdict() Verdict {
	if receipt == nil || receipt.Code == "native_exit_zero" {
		return Pass
	}
	return Inconclusive
}

func targetLifecycleMode(receipt *TargetLifecycle) string {
	if receipt == nil {
		return "prepared_attach"
	}
	return receipt.Mode
}

type ownedGateway struct {
	command  *exec.Cmd
	done     chan struct{}
	err      error
	log      *boundedGatewayLog
	stop     sync.Once
	forced   atomic.Bool
	admitted bool
}

type boundedGatewayLog struct {
	file      *os.File
	remaining int
	truncated bool
}

func (log *boundedGatewayLog) Write(data []byte) (int, error) {
	count := len(data)
	if len(data) > log.remaining {
		log.truncated = true
		data = data[:log.remaining]
	}
	written, err := log.file.Write(data)
	log.remaining -= written
	if err != nil || written != len(data) {
		return written, io.ErrShortWrite
	}
	return count, nil
}

// The opt-in mode launches only the current executable and a fresh, prepared
// profile. It never adopts a PID, copies production state or widens tool policy.
func startOwnedGateway(ctx context.Context, preset Preset, home string) (*ownedGateway, admittedTarget, error) {
	var target admittedTarget
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" || ctx == nil || ctx.Err() != nil {
		return nil, target, ErrOwnedGateway
	}
	profile, err := readPreparedProfile(preset, home)
	if err != nil || validateConversationProfile(profile.config, preset, profile.workspace) != nil ||
		validateFreshOwnedProfile(profile) != nil ||
		!slices.Contains([]string{"warn", "error", "fatal"}, config.EffectiveGatewayLogLevel(profile.config)) {
		return nil, target, ErrOwnedGateway
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, target, ErrOwnedGateway
	}
	child, err := launchOwnedGateway(profile, executable)
	if err != nil {
		return nil, target, ErrOwnedGateway
	}
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-child.done:
			return child, target, ErrOwnedGateway
		default:
		}
		target, err = admitPreparedTarget(startupCtx, preset, home)
		if err == nil {
			if target.probe.PID != child.command.Process.Pid {
				return child, admittedTarget{}, ErrOwnedGateway
			}
			child.admitted = true
			return child, target, nil
		}
		select {
		case <-child.done:
			return child, admittedTarget{}, ErrOwnedGateway
		case <-startupCtx.Done():
			return child, admittedTarget{}, ErrOwnedGateway
		case <-ticker.C:
		}
	}
}

func validateFreshOwnedProfile(profile preparedProfile) error {
	if profile.workspace != filepath.Join(profile.home, "workspace") {
		return ErrOwnedGateway
	}
	root, err := os.OpenRoot(profile.home)
	if err != nil {
		return ErrOwnedGateway
	}
	defer func() { _ = root.Close() }()
	for _, directory := range []string{".", "workspace"} {
		file, openErr := root.Open(directory)
		if openErr != nil {
			return ErrOwnedGateway
		}
		entries, readErr := file.ReadDir(129)
		closeErr := file.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) > 128 || closeErr != nil {
			return ErrOwnedGateway
		}
		allowed := []string{
			"config.json", config.SecurityConfigFile, config.SecurityConfigFile + ".lock",
			".security", "workspace", "reports",
		}
		if directory == "workspace" {
			allowed = []string{"AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md", "TOOLS.md", "skills"}
		}
		for _, entry := range entries {
			if !slices.Contains(allowed, entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
				return ErrOwnedGateway
			}
			if directory == "." && entry.Name() == config.SecurityConfigFile+".lock" {
				info, infoErr := entry.Info()
				if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
					return ErrOwnedGateway
				}
			}
		}
	}
	return nil
}

func launchOwnedGateway(profile preparedProfile, executable string) (*ownedGateway, error) {
	root, err := os.OpenRoot(profile.home)
	if err != nil {
		return nil, ErrOwnedGateway
	}
	defer func() { _ = root.Close() }()
	// A stale ownership marker is deliberately not adopted or removed.
	lock, err := root.OpenFile(".eval-gateway-owner", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, ErrOwnedGateway
	}
	if lock.Close() != nil {
		return nil, ErrOwnedGateway
	}
	logFile, err := root.OpenFile("eval-gateway-"+uuid.NewString()+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Remove(".eval-gateway-owner")
		return nil, ErrOwnedGateway
	}
	log := &boundedGatewayLog{file: logFile, remaining: ownedGatewayLogBytes}
	command := exec.Command(executable, "gateway")
	command.Dir = profile.home
	command.Env = []string{
		"HOME=" + profile.home, config.EnvHome + "=" + profile.home,
		config.EnvConfig + "=" + filepath.Join(profile.home, "config.json"),
		"PATH=" + os.Getenv("PATH"), "LANG=C", "MINTCLAW_LOG_LEVEL=" + config.EffectiveGatewayLogLevel(profile.config),
	}
	command.Stdout, command.Stderr = log, log
	command.WaitDelay = 2 * time.Second
	if command.Start() != nil {
		_ = logFile.Close()
		_ = root.Remove(".eval-gateway-owner")
		return nil, ErrOwnedGateway
	}
	child := &ownedGateway{command: command, done: make(chan struct{}), log: log}
	go func() {
		defer close(child.done)
		child.err = command.Wait()
		child.err = errors.Join(child.err, logFile.Sync(), logFile.Close())
		// Keep the marker on abnormal exit: never allow an uncertain retry.
		if child.err == nil {
			cleanup, openErr := os.OpenRoot(profile.home)
			if openErr == nil {
				child.err = errors.Join(cleanup.Remove(".eval-gateway-owner"), cleanup.Close())
			} else {
				child.err = ErrOwnedGateway
			}
		}
	}()
	return child, nil
}

// Cleanup gets its own budget, independent of an interrupted scenario. Only
// this command's Process handle is signaled; a stale PID file is never a target.
func (child *ownedGateway) stopAndWait(ctx context.Context) TargetLifecycle {
	receipt := TargetLifecycle{
		Mode: "owned_gateway", PID: child.command.Process.Pid, Admitted: child.admitted,
		Code: "exit_unobserved",
	}
	child.stop.Do(func() {
		select {
		case <-child.done:
		default:
			_ = child.command.Process.Signal(os.Interrupt)
		}
	})
	receipt.StopRequested = true
	select {
	case <-child.done:
	case <-ctx.Done():
		child.forced.Store(true)
		_ = child.command.Process.Kill()
		// Reap even after the caller's observation budget expires. WaitDelay
		// bounds inherited pipes; forced exit never certifies native teardown.
		<-child.done
	}
	receipt.Forced, receipt.ExitObserved = child.forced.Load(), true
	receipt.LogTruncated = child.log.truncated
	code := child.command.ProcessState.ExitCode()
	receipt.ExitCode = &code
	receipt.Code = "abnormal_exit"
	if receipt.Forced {
		receipt.Code = "forced_exit"
	} else if child.err == nil {
		receipt.Code = "native_exit_zero"
	}
	return receipt
}
