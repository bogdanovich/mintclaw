package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestConfigNormalizesCompanionBrowserProfileWithoutProjectingHostDetails(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.CapabilityMode = browserpolicy.CapabilityFullAccess
	profile.ApprovalMode = browserpolicy.ApprovalModelRequested
	profile.SensitiveFields = []string{"  Display   Name  "}
	originalAgents := append([]string(nil), profile.AllowedAgents...)
	originalActions := append([]string(nil), profile.AllowedActions...)
	originalSensitiveFields := append([]string(nil), profile.SensitiveFields...)
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{
			"managed": profile,
		},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	ready := cfg.BrowserProfiles["managed"]
	if !ready.Enabled || !filepath.IsAbs(ready.DriverExecutable) ||
		!filepath.IsAbs(ready.ProfileDirectory) || !filepath.IsAbs(ready.LockFile) ||
		strings.Join(ready.AllowedAgents, ",") != "browser,marketplace" ||
		strings.Join(ready.AllowedActions, ",") != "check,click,download,file_chooser,hover,navigate,uncheck" ||
		ready.CapabilityMode != browserpolicy.CapabilityFullAccess ||
		ready.ApprovalMode != browserpolicy.ApprovalModelRequested ||
		strings.Join(ready.SensitiveFields, ",") != "display name" {
		t.Fatalf("normalized browser profile = %#v", ready)
	}
	if strings.Join(profile.AllowedAgents, ",") != strings.Join(originalAgents, ",") ||
		strings.Join(profile.AllowedActions, ",") != strings.Join(originalActions, ",") ||
		strings.Join(profile.SensitiveFields, ",") != strings.Join(originalSensitiveFields, ",") {
		t.Fatal("Normalize() mutated caller-owned browser profile slices")
	}
	descriptors, err := browserProfileDescriptors(cfg.BrowserProfiles)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("browserProfileDescriptors() = %#v, %v", descriptors, err)
	}
	if descriptors[0].CapabilityMode != browserpolicy.CapabilityFullAccess ||
		descriptors[0].ApprovalMode != browserpolicy.ApprovalModelRequested {
		t.Fatalf("browser descriptor modes = %#v", descriptors[0])
	}
	encoded, err := json.Marshal(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		ready.DriverExecutable, ready.ProfileDirectory, ready.LockFile,
		ready.DriverExecutableSHA256, "display name", "sensitive_fields", "driver_arguments", "allowed_agents",
		"allowed_actors",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("browser descriptor leaked companion-local value %q", private)
		}
	}
	commands, err := nodes.BrowserCommandDescriptors(descriptors)
	if err != nil || len(commands) != 8 {
		t.Fatalf("BrowserCommandDescriptors() count = %d, error = %v", len(commands), err)
	}
	for _, command := range commands {
		if command.ModelContract != nil {
			t.Fatalf("internal command %q became model-visible", command.Name)
		}
	}
}

func TestBrowserProfileDescriptorsHideDragFromDryRunProfiles(t *testing.T) {
	profile := companionBrowserProfileFixture(t, t.TempDir())
	profile.AllowedActions = []string{"drag", "navigate"}
	profile.Limits = nodes.BrowserLimits{}.Effective()
	descriptors, err := browserProfileDescriptors(map[string]BrowserProfilePolicy{"managed": profile})
	if err != nil || len(descriptors) != 1 || strings.Join(descriptors[0].Actions, ",") != "navigate" {
		t.Fatalf("dry-run descriptors = %#v, %v", descriptors, err)
	}
	profile.DryRun = false
	profile.AllowApprovedActions = true
	descriptors, err = browserProfileDescriptors(map[string]BrowserProfilePolicy{"managed": profile})
	if err != nil || len(descriptors) != 1 || strings.Join(descriptors[0].Actions, ",") != "drag,navigate" {
		t.Fatalf("approved descriptors = %#v, %v", descriptors, err)
	}
}

func TestConfigAcceptsExplicitApprovedActionBrowserProfile(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.DryRun = false
	profile.AllowApprovedActions = true

	cfg, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatalf("Normalize() approved-action mode error = %v", err)
	}
	ready := cfg.BrowserProfiles["managed"]
	if ready.DryRun || !ready.AllowApprovedActions {
		t.Fatalf("normalized approved-action profile = %#v", ready)
	}
	descriptors, err := browserProfileDescriptors(cfg.BrowserProfiles)
	if err != nil || len(descriptors) != 1 || descriptors[0].DryRun ||
		!descriptors[0].AllowApprovedActions {
		t.Fatalf("approved-action descriptors = %#v, %v", descriptors, err)
	}
}

func TestConfigValidatesExplicitBrowserRuntimeExecutable(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	runtimeExecutable := filepath.Join(baseDir, "browser-runtime")
	if err := os.WriteFile(runtimeExecutable, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.DriverArguments = append(profile.DriverArguments, "--executable-path="+runtimeExecutable)

	cfg, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	ready := cfg.BrowserProfiles["managed"]
	canonicalRuntime, err := filepath.EvalSymlinks(runtimeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if ready.browserExecutablePath != canonicalRuntime ||
		ready.DriverArguments[len(ready.DriverArguments)-1] != "--executable-path="+canonicalRuntime {
		t.Fatalf("normalized browser runtime = %#v", ready)
	}
	replacement := filepath.Join(baseDir, "replacement-runtime")
	if err = os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, runtimeExecutable); err != nil {
		t.Fatal(err)
	}
	if err = verifyBrowserProfileRuntimeIdentity(ready); err == nil ||
		!strings.Contains(err.Error(), "browser runtime executable identity changed") {
		t.Fatalf("runtime identity error = %v", err)
	}
}

func TestConfigRejectsIdenticalBrowserRuntimeReplacement(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	runtimeExecutable := filepath.Join(baseDir, "browser-runtime")
	const runtimeContents = "identical runtime"
	if err := os.WriteFile(runtimeExecutable, []byte(runtimeContents), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.DriverArguments = append(profile.DriverArguments, "--executable-path="+runtimeExecutable)
	cfg, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(baseDir, "replacement-runtime")
	if err = os.WriteFile(replacement, []byte(runtimeContents), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, runtimeExecutable); err != nil {
		t.Fatal(err)
	}
	if err = verifyBrowserProfileRuntimeIdentity(cfg.BrowserProfiles["managed"]); err == nil ||
		!strings.Contains(err.Error(), "browser runtime executable identity changed") {
		t.Fatalf("identical replacement runtime identity error = %v", err)
	}
}

func TestConfigRejectsBrowserRuntimeSymlinkIntoUnsafeDirectory(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	trustedDir := filepath.Join(baseDir, "trusted")
	unsafeDir := filepath.Join(baseDir, "unsafe")
	if err := os.Mkdir(trustedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(unsafeDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeDir, 0o777); err != nil {
		t.Fatal(err)
	}
	runtimeExecutable := filepath.Join(unsafeDir, "browser-runtime")
	if err := os.WriteFile(runtimeExecutable, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(trustedDir, "browser-runtime")
	if err := os.Symlink(runtimeExecutable, link); err != nil {
		t.Fatal(err)
	}
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.DriverArguments = append(profile.DriverArguments, "--executable-path="+link)

	_, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err == nil || !strings.Contains(err.Error(), "target directory is not trusted") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestConfigRejectsMissingExplicitBrowserRuntimeExecutable(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.DriverArguments = append(profile.DriverArguments, "--executable-path=missing-browser")

	_, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err == nil || !strings.Contains(err.Error(), "validate browser executable path") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestNormalizeBrowserRuntimeExecutableSupportsSplitPathsAndRejectsDuplicates(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	runtimeExecutable := filepath.Join(baseDir, "browser runtime=value")
	if err := os.WriteFile(runtimeExecutable, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	arguments, executable, digest, info, err := normalizeBrowserRuntimeExecutable(
		[]string{"--browser=chromium", "--executable-path", runtimeExecutable},
		baseDir,
	)
	if err != nil || executable == "" || digest == "" || info == nil || len(arguments) != 3 ||
		arguments[1] != "--executable-path" || arguments[2] != executable {
		t.Fatalf(
			"normalizeBrowserRuntimeExecutable() = %#v, %q, %q, %#v, %v",
			arguments, executable, digest, info, err,
		)
	}
	for _, invalid := range [][]string{
		{"--executable-path="},
		{"--executable-path=" + runtimeExecutable, "--executable-path", runtimeExecutable},
	} {
		if _, _, _, _, err = normalizeBrowserRuntimeExecutable(invalid, baseDir); err == nil {
			t.Fatalf("normalizeBrowserRuntimeExecutable(%#v) succeeded", invalid)
		}
	}
}

func TestConfigRejectsUnsafeCompanionBrowserProfiles(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	tests := []struct {
		name   string
		mutate func(*BrowserProfilePolicy, string)
		want   string
	}{
		{
			name:   "non dry run",
			mutate: func(profile *BrowserProfilePolicy, _ string) { profile.DryRun = false },
			want:   "exactly one of dry_run or allow_approved_actions",
		},
		{
			name: "conflicting action modes",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.AllowApprovedActions = true
			},
			want: "exactly one of dry_run or allow_approved_actions",
		},
		{
			name:   "attached mode",
			mutate: func(profile *BrowserProfilePolicy, _ string) { profile.Mode = "attached_user" },
			want:   "managed mode",
		},
		{
			name: "raw evaluation action",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.AllowedActions = []string{"evaluate"}
			},
			want: "unsupported action",
		},
		{
			name: "raw driver endpoint",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.DriverArguments = append(
					profile.DriverArguments,
					"--cdp-endpoint=http://127.0.0.1:9222",
				)
			},
			want: "option reserved for the companion browser host",
		},
		{
			name:   "missing actor ceiling",
			mutate: func(profile *BrowserProfilePolicy, _ string) { profile.AllowedActors = nil },
			want:   "must be non-empty",
		},
		{
			name: "wrong executable digest",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.DriverExecutableSHA256 = strings.Repeat("0", sha256.Size*2)
			},
			want: "does not match",
		},
		{
			name: "profile nested lock",
			mutate: func(profile *BrowserProfilePolicy, root string) {
				profile.LockFile = filepath.Join(root, "profile", "browser.lock")
			},
			want: "outside profile_directory",
		},
		{
			name: "missing network mode",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.NetworkMode = ""
			},
			want: "network_mode is required",
		},
		{
			name: "private exact origin",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.NetworkMode = nodes.BrowserNetworkExactOrigins
				profile.AllowedOrigins = []string{"http://127.0.0.1"}
			},
			want: "outside the public network policy",
		},
		{
			name: "negative limit",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.Limits.DownloadBytes = -1
			},
			want: "malformed browser limits",
		},
		{
			name: "limit expansion",
			mutate: func(profile *BrowserProfilePolicy, _ string) {
				profile.Limits.DownloadBytes = nodes.MaxBrowserDownloadBytes + 1
			},
			want: "malformed browser limits",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseDir := t.TempDir()
			profile := companionBrowserProfileFixture(t, baseDir)
			test.mutate(&profile, baseDir)
			_, err := (Config{
				GatewayURL:      "wss://gateway.example",
				BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
			}).Normalize(baseDir)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Normalize() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConfigRejectsPermissiveCompanionBrowserProfileDirectory(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	profile := companionBrowserProfileFixture(t, baseDir)
	if err := os.Chmod(profile.ProfileDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err == nil || !strings.Contains(err.Error(), "must not grant group or world access") {
		t.Fatalf("Normalize() permissive directory error = %v", err)
	}
}

func TestConfigRejectsPermissiveCompanionBrowserLockDirectory(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	lockDir := filepath.Join(baseDir, "shared-locks")
	if err := os.Mkdir(lockDir, 0o750); err != nil {
		t.Fatal(err)
	}
	profile := companionBrowserProfileFixture(t, baseDir)
	profile.LockFile = filepath.Join(lockDir, "browser.lock")
	_, err := (Config{
		GatewayURL:      "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{"managed": profile},
	}).Normalize(baseDir)
	if err == nil || !strings.Contains(err.Error(), "lock_file parent must be private") {
		t.Fatalf("Normalize() permissive lock directory error = %v", err)
	}
}

func TestConfigKeepsCompanionBrowserProfilesDisabledByDefault(t *testing.T) {
	cfg, err := (Config{GatewayURL: "wss://gateway.example"}).Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := browserProfileDescriptors(cfg.BrowserProfiles)
	if err != nil || len(descriptors) != 0 {
		t.Fatalf("browserProfileDescriptors() = %#v, %v", descriptors, err)
	}
	if _, err = (Config{
		GatewayURL: "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{
			"managed": {Revision: "hidden-authority"},
		},
	}).Normalize(t.TempDir()); err == nil || !strings.Contains(err.Error(), "disabled browser profile") {
		t.Fatalf("disabled authority error = %v", err)
	}
}

func TestBrowserProfileRuntimeIdentityFailsClosedAfterConfiguration(t *testing.T) {
	requireBrowserProfileIdentitySupport(t)
	baseDir := t.TempDir()
	cfg, err := (Config{
		GatewayURL: "wss://gateway.example",
		BrowserProfiles: map[string]BrowserProfilePolicy{
			"managed": companionBrowserProfileFixture(t, baseDir),
		},
	}).Normalize(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.BrowserProfiles["managed"]
	if err = verifyBrowserProfileRuntimeIdentity(profile); err != nil {
		t.Fatalf("initial runtime identity error = %v", err)
	}
	if err = os.Chmod(profile.ProfileDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = verifyBrowserProfileRuntimeIdentity(profile); err == nil ||
		!strings.Contains(err.Error(), "profile identity changed") {
		t.Fatalf("changed runtime identity error = %v", err)
	}
}

func companionBrowserProfileFixture(t *testing.T, baseDir string) BrowserProfilePolicy {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	profileDir := filepath.Join(baseDir, "profile")
	if err = os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(baseDir, "locks")
	if err = os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return BrowserProfilePolicy{
		Enabled: true, Revision: "managed-v1",
		AllowedAgents:    []string{"marketplace", "browser"},
		AllowedActors:    []string{"telegram:owner"},
		Driver:           nodes.BrowserDriverPlaywrightMCP,
		DriverExecutable: executable, DriverExecutableSHA256: hex.EncodeToString(digest[:]),
		DriverArguments:  []string{"--browser=chrome"},
		ProfileDirectory: profileDir, LockFile: filepath.Join(lockDir, "browser.lock"),
		Mode: nodes.BrowserProfileManaged, NetworkMode: nodes.BrowserNetworkAnyHTTP,
		DryRun: true,
		AllowedActions: []string{
			"navigate", "click", "download", "file_chooser", "check", "uncheck", "hover",
		},
		Headed: true,
	}
}

func requireBrowserProfileIdentitySupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("browser profile identity validation is admitted for Darwin and Linux companions")
	}
}
