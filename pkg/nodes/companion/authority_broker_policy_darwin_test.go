//go:build darwin

package companion

import (
	"os"
	"testing"
)

func validAuthorityBrokerPlatformConfig(_ *testing.T, _ string) AuthorityBrokerConfig {
	return AuthorityBrokerConfig{
		Companion: &AuthorityBrokerCompanionConfig{
			ExecutablePath: "/bin/sh",
			ConfigPath:     "/etc/hosts",
			UID:            uint32(os.Getuid()),
			GID:            uint32(os.Getgid()),
		},
	}
}

func TestDarwinAuthorityBrokerPolicyRejectsLinuxPeerBoundary(t *testing.T) {
	config := validAuthorityBrokerPlatformConfig(t, "")
	config.SocketPath = "/tmp/public.sock"
	config.Revision = "broker-v1"
	config.Profiles = map[string]AuthorityBrokerProfile{
		"owner-root": {
			Revision: "profile-v1", ShellPath: "/bin/sh",
			UID: 0, GID: 0, WorkingScopes: map[string]string{"workspace": "/tmp"},
			Network: "inherit", TimeoutSecondsMax: 30, OutputBytesMax: 8192,
			ConcurrentCommands: 1,
		},
	}
	if _, err := NormalizeAuthorityBrokerConfig(config, ""); err == nil {
		t.Fatal("macOS broker accepted a public Linux-style socket")
	}
}
