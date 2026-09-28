//go:build linux

package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func validAuthorityBrokerPlatformConfig(_ *testing.T, base string) AuthorityBrokerConfig {
	return AuthorityBrokerConfig{
		SocketPath:      filepath.Join(base, "broker.sock"),
		AllowedUID:      uint32(os.Getuid()),
		AllowedGID:      uint32(os.Getgid()),
		CompanionCgroup: "/system.slice/mintclaw-node.service",
	}
}

func TestLinuxAuthorityBrokerPolicyRejectsInvalidPeerBoundary(t *testing.T) {
	base := t.TempDir()
	for _, config := range []AuthorityBrokerConfig{
		{
			SocketPath:      filepath.Join(base, "broker.sock"),
			AllowedUID:      0,
			AllowedGID:      uint32(os.Getgid()),
			CompanionCgroup: "/system.slice/mintclaw-node.service",
		},
		{
			SocketPath:      filepath.Join(base, "broker.sock"),
			AllowedUID:      uint32(os.Getuid()),
			AllowedGID:      uint32(os.Getgid()),
			CompanionCgroup: "/system.slice/../user.slice",
		},
	} {
		config.Revision = "broker-v1"
		config.Profiles = map[string]AuthorityBrokerProfile{"owner-root": testAuthorityBrokerProfile(t, base)}
		if _, err := NormalizeAuthorityBrokerConfig(config, base); err == nil {
			t.Fatal("invalid Linux broker peer boundary was accepted")
		}
	}
}

func testAuthorityBrokerProfile(t *testing.T, base string) AuthorityBrokerProfile {
	t.Helper()
	shell := filepath.Join(base, "shell-linux")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return AuthorityBrokerProfile{
		Revision: "profile-v1", ShellPath: shell,
		UID: 0, GID: 0, WorkingScopes: map[string]string{"workspace": base},
		Network: "inherit", TimeoutSecondsMax: 30, OutputBytesMax: 8192,
		ConcurrentCommands: 1,
	}
}
