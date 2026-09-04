package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

type systemVersionResponse struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
}

type cachedSystemVersion struct {
	value      systemVersionResponse
	gatewayPID int
}

type systemVersionCache struct {
	mu         sync.Mutex
	current    cachedSystemVersion
	hasCurrent bool
	inflightCh chan struct{}
}

func newSystemVersionCache() *systemVersionCache {
	return &systemVersionCache{}
}

const (
	// 15 seconds matches the gateway startup window used elsewhere in launcher flow,
	// giving slow/embedded hosts enough time for first command invocation while
	// staying independent from cross-file init ordering.
	versionCmdTimeout         = 15 * time.Second
	maxVersionResolveAttempts = 3
)

var (
	ansiEscapePattern  = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	versionLinePattern = regexp.MustCompile(
		`^(?:[^A-Za-z0-9]*\s*)?mintclaw(?:\.exe)?\s+([^\s(]+)` +
			`(?:\s+\(git:\s*([^)]+)\))?\s*$`,
	)
)

func (h *Handler) registerVersionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/version", h.handleGetVersion)
}

// handleGetVersion returns runtime version information for web clients.
func (h *Handler) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	versionInfo := h.resolveSystemVersionInfo(r.Context())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(versionInfo); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// resolveSystemVersionInfo prefers the actual mintclaw binary version output,
// and falls back to launcher build metadata when command execution fails.
func (h *Handler) resolveSystemVersionInfo(ctx context.Context) systemVersionResponse {
	for range maxVersionResolveAttempts {
		gatewayPID, gatewayAlive := h.gatewayVersionState()
		if cached, ok := h.versionInfoCache.get(gatewayPID, gatewayAlive); ok {
			return cached
		}

		leader, ok := h.versionInfoCache.waitOrStart(ctx)
		if !ok {
			return h.fallbackSystemVersionInfo()
		}
		if !leader {
			continue
		}

		resolved := h.resolveSystemVersionInfoUncached(ctx)
		gatewayPID, gatewayAlive = h.gatewayVersionState()
		h.versionInfoCache.finishResolve(resolved, gatewayPID, gatewayAlive)
		return resolved
	}

	return h.fallbackSystemVersionInfo()
}

func (h *Handler) resolveSystemVersionInfoUncached(ctx context.Context) systemVersionResponse {
	if ctx == nil {
		ctx = context.Background()
	}

	fallback := h.fallbackSystemVersionInfo()

	execPath := strings.TrimSpace(h.resolveGatewayBinaryForVersionInfo())
	if execPath == "" {
		return fallback
	}

	cmdCtx, cancel := context.WithTimeout(ctx, versionCmdTimeout)
	defer cancel()

	output, err := h.gateway.runVersionOutput(cmdCtx, execPath)
	if err != nil {
		return fallback
	}

	parsed, ok := parseMintClawVersionOutput(output)
	if !ok {
		return fallback
	}

	if parsed.GoVersion == "" {
		parsed.GoVersion = fallback.GoVersion
		if parsed.GoVersion == "" {
			parsed.GoVersion = runtime.Version()
		}
	}

	return parsed
}

func (h *Handler) fallbackSystemVersionInfo() systemVersionResponse {
	return h.gateway.fallbackVersion()
}

func fallbackSystemVersionInfoFromConfig() systemVersionResponse {
	buildTime, goVer := config.FormatBuildInfo()
	return systemVersionResponse{
		Version:   config.GetVersion(),
		GitCommit: config.GitCommit,
		BuildTime: buildTime,
		GoVersion: goVer,
	}
}

// resolveGatewayBinaryForVersionInfo uses the same executable as the launcher
// gateway start path when available, then falls back to launcher binary lookup.
// This keeps version probing aligned with the actual gateway startup behavior,
// so web and gateway do not drift onto different binaries.
func (h *Handler) resolveGatewayBinaryForVersionInfo() string {
	return h.gateway.resolveBinaryForVersionInfo()
}

func (m *GatewayProcessManager) resolveBinaryForVersionInfo() string {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd != nil {
		if execPath := strings.TrimSpace(cmd.Path); execPath != "" {
			return execPath
		}
	}

	return m.findBinary()
}

func (h *Handler) gatewayVersionState() (int, bool) {
	return h.gateway.versionState()
}

func (m *GatewayProcessManager) versionState() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return 0, false
	}
	pid := m.cmd.Process.Pid
	if pid <= 0 {
		return 0, false
	}

	return pid, m.processAlive(m.cmd)
}

func (c *systemVersionCache) get(gatewayPID int, gatewayAlive bool) (systemVersionResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hasCurrent && (!gatewayAlive || gatewayPID <= 0 || gatewayPID != c.current.gatewayPID) {
		c.clearCurrentLocked()
	}

	if c.hasCurrent {
		return c.current.value, true
	}

	return systemVersionResponse{}, false
}

func (c *systemVersionCache) waitOrStart(ctx context.Context) (bool, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false, false
	}

	c.mu.Lock()
	if c.inflightCh == nil {
		c.inflightCh = make(chan struct{})
		c.mu.Unlock()
		return true, true
	}
	waitCh := c.inflightCh
	c.mu.Unlock()

	select {
	case <-waitCh:
		return false, true
	case <-ctx.Done():
		return false, false
	}
}

func (c *systemVersionCache) finishResolve(value systemVersionResponse, gatewayPID int, gatewayAlive bool) {
	c.mu.Lock()
	if gatewayAlive && gatewayPID > 0 {
		c.current = cachedSystemVersion{value: value, gatewayPID: gatewayPID}
		c.hasCurrent = true
	} else {
		c.clearCurrentLocked()
	}

	inflightCh := c.inflightCh
	c.inflightCh = nil
	c.mu.Unlock()

	if inflightCh != nil {
		close(inflightCh)
	}
}

func (c *systemVersionCache) clearCurrentLocked() {
	c.hasCurrent = false
	c.current = cachedSystemVersion{}
}

// executeMintClawVersion runs the version subcommand against the
// discovered mintclaw executable.
func executeMintClawVersion(ctx context.Context, execPath string) (string, error) {
	cmd := exec.CommandContext(ctx, execPath, "version")
	applyLauncherProcAttrs(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}

	return string(out), fmt.Errorf("failed to execute version command: %w", err)
}

// parseMintClawVersionOutput extracts version/build/go fields from CLI output.
// It accepts banner/ANSI-decorated output and only requires the version line.
func parseMintClawVersionOutput(raw string) (systemVersionResponse, bool) {
	var result systemVersionResponse

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(ansiEscapePattern.ReplaceAllString(scanner.Text(), ""))
		if line == "" {
			continue
		}

		if match := versionLinePattern.FindStringSubmatch(line); len(match) > 0 {
			candidateVersion := strings.TrimSpace(match[1])
			if !isLikelyVersionValue(candidateVersion) {
				continue
			}
			result.Version = candidateVersion
			if len(match) > 2 {
				result.GitCommit = strings.TrimSpace(match[2])
			}
			continue
		}

		if buildValue, ok := strings.CutPrefix(line, "Build:"); ok {
			result.BuildTime = strings.TrimSpace(buildValue)
			continue
		}

		if goValue, ok := strings.CutPrefix(line, "Go:"); ok {
			result.GoVersion = strings.TrimSpace(goValue)
		}
	}

	if err := scanner.Err(); err != nil {
		return systemVersionResponse{}, false
	}

	if result.Version == "" {
		return systemVersionResponse{}, false
	}

	return result, true
}

func isLikelyVersionValue(value string) bool {
	v := strings.TrimSpace(strings.ToLower(value))
	if v == "" {
		return false
	}
	if v == "dev" {
		return true
	}

	// Accept git-like short/long hashes even when they contain only letters (a-f).
	if len(v) >= 7 && len(v) <= 40 {
		allHex := true
		for _, ch := range v {
			if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
				continue
			}
			allHex = false
			break
		}
		if allHex {
			return true
		}
	}

	for _, ch := range v {
		if ch >= '0' && ch <= '9' {
			return true
		}
	}
	return false
}
