package gateway

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

func snapshotRuntimeProbe(
	home, configPath string,
	loop *agent.AgentLoop,
	running *services,
) (health.RuntimeProbe, error) {
	var result health.RuntimeProbe
	if loop == nil || running == nil || running.authToken == "" || running.reloading.Load() {
		return result, errors.New("runtime probe unavailable")
	}
	fingerprint, err := loop.PublicConfigFingerprint()
	if err != nil {
		return result, errors.New("runtime probe unavailable")
	}
	homeHash, err := physicalPathFingerprint(home)
	if err != nil {
		return result, err
	}
	configHash, err := physicalPathFingerprint(configPath)
	if err != nil || running.reloading.Load() {
		return result, errors.New("runtime probe unavailable")
	}
	buildTime, goVersion := config.FormatBuildInfo()
	return health.RuntimeProbe{
		Version: 1, PID: os.Getpid(), InstanceSHA256: probeHash(running.authToken), ConfigSHA256: fingerprint,
		HomeSHA256: homeHash, ConfigPathSHA256: configHash, LogLevel: logger.GetLevel().String(),
		BuildVersion: config.Version, GitCommit: config.GitCommit, BuildTime: buildTime, GoVersion: goVersion,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}, nil
}

func physicalPathFingerprint(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", errors.New("runtime probe unavailable")
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("runtime probe unavailable")
	}
	return probeHash(physical), nil
}

func probeHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
