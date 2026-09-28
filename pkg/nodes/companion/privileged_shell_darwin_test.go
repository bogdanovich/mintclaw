//go:build darwin

package companion

import "testing"

func TestConfigNormalizesDarwinPrivilegedShellHelper(t *testing.T) {
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled:          true,
			PrivilegedHelper: &PrivilegedShellHelperConfig{},
		},
	}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerShell == nil || cfg.OwnerShell.PrivilegedHelper == nil ||
		cfg.OwnerShell.PrivilegedHelper.Endpoint != "" {
		t.Fatalf("owner shell config = %#v", cfg.OwnerShell)
	}
}

func TestConfigRejectsDarwinPrivilegedShellEndpoint(t *testing.T) {
	_, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled: true,
			PrivilegedHelper: &PrivilegedShellHelperConfig{
				Endpoint: "/tmp/public.sock",
			},
		},
	}).Normalize(t.TempDir())
	if err == nil {
		t.Fatal("Normalize() accepted caller-selected Darwin privileged helper endpoint")
	}
}

func TestConfigRejectsDarwinLegacyBrokerSocket(t *testing.T) {
	_, err := (Config{
		GatewayURL: "wss://gateway.example",
		OwnerShell: &OwnerShellConfig{
			Enabled:      true,
			BrokerSocket: "/tmp/public.sock",
		},
	}).Normalize(t.TempDir())
	if err == nil {
		t.Fatal("Normalize() accepted legacy public broker socket on macOS")
	}
}
