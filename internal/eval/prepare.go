package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

var ErrPreparation = errors.New("eval: isolated profile preparation failed; inspect any reported private home")

const maxPreparedInputBytes = 1024 * 1024

type Preparation struct {
	Version            int    `json:"version"`
	Outcome            string `json:"outcome"`
	Home               string `json:"home"`
	ConfigPath         string `json:"config_path"`
	Workspace          string `json:"workspace"`
	PresetPath         string `json:"preset_path"`
	PublicConfigSHA256 string `json:"public_config_sha256,omitempty"`
	WorkspaceFileCount int    `json:"workspace_file_count"`
	RuntimeRun         bool   `json:"runtime_run"`
}

// PrepareProfile copies only an admitted isolated seed, never production state.
// It does not construct providers, start a gateway, or change process globals.
// After allocation, failures retain and report the private partial home rather
// than hiding credential-bearing leftovers behind a best-effort rollback.
func PrepareProfile(ctx context.Context, preset Preset, home, parent string) (Preparation, error) {
	var result Preparation
	if ctx == nil || ctx.Err() != nil || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return result, ErrPreparation
	}
	seed, before, err := inspectPreparedProfile(preset, home)
	if err != nil || validateConversationPolicy(seed.config, preset, seed.workspace) != nil {
		return result, ErrPreparation
	}
	physicalParent, err := privateHome(parent)
	if err != nil || physicalParent == seed.home || pathWithin(seed.home, physicalParent) ||
		pathWithin(physicalParent, seed.home) {
		return result, ErrPreparation
	}
	// Durable reads deliberately ignore ambient config/credential overrides. The
	// public policy gate above precedes native lock/recovery and credential work.
	snapshot, err := config.NewRepository(preset.ConfigPath).ReadDurable()
	if err != nil || !profileConfigUnchanged(seed.home, before) ||
		validateConversationProfile(snapshot.Config, preset, seed.workspace) != nil ||
		!slices.Contains([]string{"warn", "error", "fatal"}, config.EffectiveGatewayLogLevel(snapshot.Config)) {
		return result, ErrPreparation
	}
	seed.config = snapshot.Config
	inputs, err := readPreparationInputs(ctx, seed, preset.WorkspaceFiles)
	if err != nil {
		return result, ErrPreparation
	}
	clone, err := clonePreparationConfig(seed.config, preset)
	if err != nil {
		return result, ErrPreparation
	}
	public, err := config.ProjectPublicConfig(clone)
	if err != nil {
		return result, ErrPreparation
	}
	metadata, err := json.Marshal(struct {
		Config *config.Config
		Preset Preset
		Parent string
	}{public, preset, physicalParent})
	if err != nil || seed.config.SensitiveDataReplacer().Replace(string(metadata)) != string(metadata) {
		return result, ErrPreparation
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return result, ErrPreparation
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if listener.Close() != nil || ctx.Err() != nil {
		return result, ErrPreparation
	}
	parentRoot, err := os.OpenRoot(physicalParent)
	if err != nil {
		return result, ErrPreparation
	}
	defer func() { _ = parentRoot.Close() }()
	name := "eval-" + uuid.NewString()
	if parentRoot.Mkdir(name, 0o700) != nil {
		return result, ErrPreparation
	}
	newHome := filepath.Join(physicalParent, name)
	result = Preparation{
		Version: Version, Outcome: "incomplete", Home: newHome,
		ConfigPath: filepath.Join(newHome, "config.json"), Workspace: filepath.Join(newHome, "workspace"),
		PresetPath: filepath.Join(newHome, "reports", "preset.json"),
	}
	root, err := parentRoot.OpenRoot(name)
	if err != nil {
		return result, ErrPreparation
	}
	defer func() { _ = root.Close() }()
	for _, directory := range []string{"workspace", "reports", ".security"} {
		if root.Mkdir(directory, 0o700) != nil {
			return result, ErrPreparation
		}
	}
	clone.Agents.Defaults.Workspace = result.Workspace
	clone.Agents.List[0].Workspace = result.Workspace
	clone.Gateway.Host, clone.Gateway.Port = "127.0.0.1", port
	for modelIndex, model := range clone.ModelList {
		model.Workspace = ""
		values := model.APIKeys.Values()
		model.APIKeys = nil
		for keyIndex, value := range values {
			keyPath := fmt.Sprintf(".security/model-%d-key-%d", modelIndex, keyIndex)
			if writePreparedFile(root, keyPath, []byte(value)) != nil {
				return result, ErrPreparation
			}
			model.APIKeys = append(model.APIKeys, config.NewSecureString("file://"+filepath.ToSlash(keyPath)))
		}
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil ||
		writePreparedFile(root, ".security/channel-token", []byte(hex.EncodeToString(token[:]))) != nil {
		return result, ErrPreparation
	}
	channel := clone.Channels.GetByType(config.ChannelMintClaw)
	decoded, err := channel.GetDecoded()
	settings, ok := decoded.(*config.MintClawSettings)
	if err != nil || !ok {
		return result, ErrPreparation
	}
	settings.SetToken("file://.security/channel-token")
	for index, input := range inputs {
		if ctx.Err() != nil {
			return result, ErrPreparation
		}
		path := filepath.Join("workspace", preset.WorkspaceFiles[index].Path)
		if root.MkdirAll(filepath.Dir(path), 0o700) != nil || writePreparedFile(root, path, input) != nil {
			return result, ErrPreparation
		}
		result.WorkspaceFileCount++
	}
	prepared := preset
	prepared.IsolationRoot, prepared.ConfigPath, prepared.ExpectedWorkspace = newHome, result.ConfigPath, result.Workspace
	if ctx.Err() != nil {
		return result, ErrPreparation
	}
	saved, err := config.NewRepository(result.ConfigPath).Save(clone)
	if err != nil || validateConversationProfile(saved.Config, prepared, result.Workspace) != nil ||
		validateFreshOwnedProfile(
			preparedProfile{config: saved.Config, home: newHome, workspace: result.Workspace},
		) != nil {
		return result, ErrPreparation
	}
	fingerprint, err := config.PublicFingerprint(saved.Config)
	if err != nil {
		return result, ErrPreparation
	}
	data, err := json.Marshal(prepared)
	if err != nil || writePreparedFile(root, "reports/preset.json", append(data, '\n')) != nil || ctx.Err() != nil {
		return result, ErrPreparation
	}
	result.Outcome, result.PublicConfigSHA256 = "prepared", fingerprint
	return result, nil
}

func clonePreparationConfig(source *config.Config, preset Preset) (*config.Config, error) {
	clone, err := config.ProjectPublicConfig(source)
	if err != nil || len(source.ModelList) > 16 {
		return nil, ErrPreparation
	}
	publicModels := clone.ModelList
	clone.ModelList = nil
	for index, model := range source.ModelList {
		if !model.Enabled {
			continue
		}
		endpoint, parseErr := url.Parse(model.APIBase)
		keys := model.APIKeys.Values()
		if model.AuthMethod != "" || model.Proxy != "" || len(model.CustomHeaders) != 0 ||
			len(model.Fallbacks) != 0 || model.Capabilities != nil || parseErr != nil ||
			endpoint.User != nil || endpoint.RawQuery != "" ||
			endpoint.Fragment != "" || len(keys) == 0 || len(keys) > 8 {
			return nil, ErrPreparation
		}
		for _, key := range keys {
			if strings.TrimSpace(key) != key || key == "" || len(key) > 16*1024 {
				return nil, ErrPreparation
			}
		}
		copyModel := publicModels[index]
		copyModel.APIKeys = config.SimpleSecureStrings(keys...)
		clone.ModelList = append(clone.ModelList, copyModel)
	}
	for _, alias := range []string{preset.UserModel, preset.JudgeModel} {
		if alias != "" {
			if _, err = clone.GetModelConfig(alias); err != nil {
				return nil, ErrPreparation
			}
		}
	}
	for name, channel := range clone.Channels {
		if channel == nil || !channel.Enabled {
			delete(clone.Channels, name)
		}
	}
	return clone, nil
}

func readPreparationInputs(ctx context.Context, seed preparedProfile, files []WorkspaceFile) ([][]byte, error) {
	root, err := os.OpenRoot(seed.workspace)
	if err != nil {
		return nil, ErrPreparation
	}
	defer func() { _ = root.Close() }()
	inputs := make([][]byte, 0, len(files))
	total := 0
	filter := seed.config.SensitiveDataReplacer()
	for _, file := range files {
		if ctx.Err() != nil {
			return nil, ErrPreparation
		}
		parts := strings.Split(file.Path, "/")
		for index := range parts {
			info, statErr := root.Lstat(filepath.Join(parts[:index+1]...))
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 ||
				index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
				return nil, ErrPreparation
			}
		}
		data, readErr := readBoundedFile(root, file.Path, MaxManifestBytes)
		total += len(data)
		if readErr != nil || total > maxPreparedInputBytes || digest(data) != file.SHA256 ||
			filter.Replace(string(data)) != string(data) {
			return nil, ErrPreparation
		}
		inputs = append(inputs, data)
	}
	return inputs, nil
}

func writePreparedFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrPreparation
	}
	written, writeErr := file.Write(data)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || written != len(data) || syncErr != nil || closeErr != nil {
		return ErrPreparation
	}
	return nil
}
