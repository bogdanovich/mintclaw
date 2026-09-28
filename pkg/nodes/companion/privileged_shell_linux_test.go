//go:build linux

package companion

import (
	"path/filepath"
	"testing"
)

func TestConfigNormalizesLinuxPrivilegedShellHelper(t *testing.T) {
	baseDir := t.TempDir()
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled: true,
			PrivilegedHelper: &PrivilegedShellHelperConfig{
				Endpoint: "authority.sock",
			},
		},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerShell == nil || cfg.OwnerShell.PrivilegedHelper == nil ||
		cfg.OwnerShell.PrivilegedHelper.Endpoint != filepath.Join(baseDir, "authority.sock") {
		t.Fatalf("owner shell config = %#v", cfg.OwnerShell)
	}
}

func TestConfigRejectsLinuxPrivilegedShellWithoutEndpoint(t *testing.T) {
	_, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled:          true,
			PrivilegedHelper: &PrivilegedShellHelperConfig{},
		},
	}).Normalize(t.TempDir())
	if err == nil {
		t.Fatal("Normalize() accepted Linux privileged helper without endpoint")
	}
}

func TestConfigMigratesLinuxBrokerSocketToPrivilegedHelper(t *testing.T) {
	baseDir := t.TempDir()
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled:      true,
			BrokerSocket: "authority.sock",
		},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerShell == nil || cfg.OwnerShell.PrivilegedHelper == nil ||
		cfg.OwnerShell.PrivilegedHelper.Endpoint != filepath.Join(baseDir, "authority.sock") ||
		cfg.OwnerShell.BrokerSocket != "" {
		t.Fatalf("normalized legacy owner shell = %#v", cfg.OwnerShell)
	}
}
