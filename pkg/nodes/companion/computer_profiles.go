package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
)

// ComputerProfilePolicy is trusted local policy, never a model-authored native selector.
// Environment admits only the four explicit GUI coordinates understood by Cua.
// Revision is derived from all normalized policy fields, not read from JSON.
type ComputerProfilePolicy struct {
	Enabled            bool              `json:"enabled"`
	Executable         string            `json:"executable,omitempty"`
	SHA256             string            `json:"sha256,omitempty"`
	PID                uint32            `json:"pid,omitempty"`
	WindowID           uint32            `json:"window_id,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	AllowedActors      []string          `json:"allowed_actors,omitempty"`
	AllowedAgents      []string          `json:"allowed_agents,omitempty"`
	AllowedRuntimes    []runtimecap.Kind `json:"allowed_runtimes,omitempty"`
	LeaseSeconds       int               `json:"lease_seconds,omitempty"`
	ObservationSeconds int               `json:"observation_seconds,omitempty"`
	Revision           string            `json:"-"`
}

func HasEnabledComputerProfile(profiles map[string]ComputerProfilePolicy) bool {
	for _, profile := range profiles {
		if profile.Enabled {
			return true
		}
	}
	return false
}

// NormalizeComputerProfiles does not inspect native state, open files, start workers,
// capture a desktop, request permissions or change OS settings.
func NormalizeComputerProfiles(
	profiles map[string]ComputerProfilePolicy,
	baseDir string,
) (map[string]ComputerProfilePolicy, error) {
	if len(profiles) == 0 {
		return nil, nil
	}
	if len(profiles) > 32 {
		return nil, computer.ErrInvalid
	}
	normalized := make(map[string]ComputerProfilePolicy, len(profiles))
	for alias, profile := range profiles {
		if nodes.Alias(alias).Validate() != nil {
			return nil, computer.ErrInvalid
		}
		if !profile.Enabled {
			normalized[alias] = ComputerProfilePolicy{}
			continue
		}
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return nil, computer.ErrUnavailable
		}
		if profile.PID == 0 || profile.PID > 1<<31-1 || profile.WindowID == 0 ||
			len(profile.SHA256) != 64 || profile.Executable == "" || !utf8.ValidString(profile.Executable) ||
			strings.ContainsAny(profile.Executable, "\x00\r\n") {
			return nil, computer.ErrInvalid
		}
		if digest, err := hex.DecodeString(profile.SHA256); err != nil || len(digest) != 32 {
			return nil, computer.ErrInvalid
		}
		profile.SHA256 = strings.ToLower(profile.SHA256)
		if !filepath.IsAbs(profile.Executable) {
			if !filepath.IsAbs(baseDir) {
				return nil, computer.ErrInvalid
			}
			profile.Executable = filepath.Join(baseDir, profile.Executable)
		}
		profile.Executable = filepath.Clean(profile.Executable)
		profile.Environment = maps.Clone(profile.Environment)
		for key, value := range profile.Environment {
			switch key {
			case "DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS":
			default:
				return nil, computer.ErrInvalid
			}
			if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") ||
				runtime.GOOS == "darwin" {
				return nil, computer.ErrInvalid
			}
		}
		if runtime.GOOS == "linux" && (profile.Environment["DISPLAY"] == "" ||
			!filepath.IsAbs(profile.Environment["XAUTHORITY"])) {
			return nil, computer.ErrInvalid
		}
		if profile.LeaseSeconds == 0 {
			profile.LeaseSeconds = 300
		}
		if profile.ObservationSeconds == 0 {
			profile.ObservationSeconds = 15
		}
		// The pinned private worker's absolute capability lifetime is 30m.
		if profile.LeaseSeconds < 1 || profile.LeaseSeconds > 1800 || profile.ObservationSeconds < 1 ||
			profile.ObservationSeconds > 60 || profile.ObservationSeconds > profile.LeaseSeconds {
			return nil, computer.ErrInvalid
		}
		var err error
		profile.AllowedActors, err = normalizeOwners(profile.AllowedActors)
		if err != nil {
			return nil, err
		}
		profile.AllowedAgents, err = normalizeOwners(profile.AllowedAgents)
		if err != nil {
			return nil, err
		}
		profile.AllowedRuntimes = slices.Clone(profile.AllowedRuntimes)
		slices.Sort(profile.AllowedRuntimes)
		profile.AllowedRuntimes = slices.Compact(profile.AllowedRuntimes)
		if len(profile.AllowedRuntimes) == 0 || len(profile.AllowedRuntimes) > 2 {
			return nil, computer.ErrInvalid
		}
		for _, kind := range profile.AllowedRuntimes {
			if !kind.Valid() {
				return nil, computer.ErrInvalid
			}
		}
		encoded, err := json.Marshal(struct {
			Alias  string                `json:"alias"`
			Policy ComputerProfilePolicy `json:"policy"`
		}{Alias: alias, Policy: profile})
		if err != nil {
			return nil, computer.ErrInvalid
		}
		digest := sha256.Sum256(encoded)
		profile.Revision = hex.EncodeToString(digest[:])
		normalized[alias] = profile
	}
	return normalized, nil
}

func normalizeOwners(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, computer.ErrInvalid
	}
	values = slices.Clone(values)
	for _, value := range values {
		principal := runtimecap.Principal{
			Runtime: runtimecap.KindGateway, ActorID: value, AgentID: "shape", SessionID: "shape", ExecutionID: "shape",
		}
		if !utf8.ValidString(value) || principal.Validate() != nil {
			return nil, computer.ErrInvalid
		}
	}
	slices.Sort(values)
	return slices.Compact(values), nil
}

func (profile ComputerProfilePolicy) Admits(principal runtimecap.Principal) bool {
	return principal.Validate() == nil && slices.Contains(profile.AllowedActors, principal.ActorID) &&
		slices.Contains(
			profile.AllowedAgents,
			principal.AgentID,
		) && slices.Contains(profile.AllowedRuntimes, principal.Runtime)
}

func (profile ComputerProfilePolicy) LeaseTTL() time.Duration {
	return time.Duration(profile.LeaseSeconds) * time.Second
}
