package config

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"unicode/utf8"
)

// Build-time variables injected via ldflags during build process.
// These are set by the Makefile or .goreleaser.yaml using the -X flag:
//
//	-X github.com/bogdanovich/mintclaw/pkg/config.Version=<version>
//	-X github.com/bogdanovich/mintclaw/pkg/config.GitCommit=<commit>
//	-X github.com/bogdanovich/mintclaw/pkg/config.BuildTime=<timestamp>
//	-X github.com/bogdanovich/mintclaw/pkg/config.GoVersion=<go-version>
var (
	Version   = "dev" // Default value when not built with ldflags
	GitCommit string  // Git commit SHA (short)
	BuildTime string  // Build timestamp in RFC3339 format
	GoVersion string  // Go version used for building
)

// FormatVersion returns the version string with optional git commit
func FormatVersion() string {
	v := Version
	if GitCommit != "" {
		v += fmt.Sprintf(" (git: %s)", GitCommit)
	}
	return v
}

// FormatBuildInfo returns build time and go version info
func FormatBuildInfo() (string, string) {
	build := BuildTime
	goVer := GoVersion
	if goVer == "" {
		goVer = runtime.Version()
	}
	return build, goVer
}

// GetVersion returns the version string
func GetVersion() string {
	return Version
}

// RuntimeBuildIdentity returns bounded, non-secret build attribution for
// diagnostic records. Go's embedded VCS revision is preferred because local
// Makefile builds may inject only a shortened GitCommit through ldflags.
func RuntimeBuildIdentity() (version, commit string) {
	vcsRevision := ""
	vcsModified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRevision = setting.Value
			case "vcs.modified":
				vcsModified = setting.Value == "true"
			}
		}
	}
	return normalizeRuntimeBuildIdentity(Version, GitCommit, vcsRevision, vcsModified)
}

func normalizeRuntimeBuildIdentity(version, ldCommit, vcsRevision string, vcsModified bool) (string, string) {
	version = strings.TrimSpace(version)
	if !utf8.ValidString(version) || len(version) > 128 {
		version = ""
	}
	if vcsModified {
		return version, ""
	}
	if validRuntimeCommit(vcsRevision, 40, 64) {
		return version, strings.ToLower(strings.TrimSpace(vcsRevision))
	}
	ldCommit = strings.TrimSpace(ldCommit)
	if validRuntimeCommit(ldCommit, 7, 64) {
		return version, strings.ToLower(ldCommit)
	}
	return version, ""
}

func validRuntimeCommit(value string, minimum, maximum int) bool {
	value = strings.TrimSpace(value)
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				if value[index] < 'A' || value[index] > 'F' {
					return false
				}
			}
		}
	}
	return true
}
