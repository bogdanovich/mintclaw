package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bogdanovich/mintclaw/pkg/migrate/internal"
)

func TestNewMigrateInstance(t *testing.T) {
	opts := Options{
		Source: "openclaw",
	}
	instance := NewMigrateInstance(opts)
	require.NotNil(t, instance)
	assert.Equal(t, "openclaw", instance.options.Source)
}

func TestMigrateInstanceRegister(t *testing.T) {
	instance := NewMigrateInstance(Options{})
	require.NotNil(t, instance)

	mockHandler := &mockOperation{}
	instance.Register("test-source", mockHandler)

	handler, ok := instance.handlers["test-source"]
	require.True(t, ok)
	assert.Equal(t, mockHandler, handler)
}

func TestMigrateInstanceGetCurrentHandler(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	err := os.WriteFile(configPath, []byte("{}"), 0o644)
	require.NoError(t, err)

	instance := NewMigrateInstance(Options{SourceHome: tmpDir})
	require.NotNil(t, instance)

	handler, err := instance.getCurrentHandler()
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, "openclaw", handler.GetSourceName())
}

func TestMigrateInstanceGetCurrentHandlerWithSource(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	err := os.WriteFile(configPath, []byte("{}"), 0o644)
	require.NoError(t, err)

	opts := Options{
		Source:     "openclaw",
		SourceHome: tmpDir,
	}
	instance := NewMigrateInstance(opts)

	handler, err := instance.getCurrentHandler()
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, "openclaw", handler.GetSourceName())
}

func TestMigrateInstanceGetCurrentHandlerNotFound(t *testing.T) {
	instance := &MigrateInstance{
		options:  Options{},
		handlers: make(map[string]Operation),
	}

	_, err := instance.getCurrentHandler()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMigrateInstancePlanWithInvalidSource(t *testing.T) {
	instance := &MigrateInstance{
		options:  Options{},
		handlers: make(map[string]Operation),
	}

	_, _, err := instance.Plan(Options{}, "/tmp/source", "/tmp/target")
	require.Error(t, err)
}

func TestMigrateInstancePlansOpenclawAgentDefinitionForCurrentFilename(t *testing.T) {
	sourceHome := t.TempDir()
	targetHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "openclaw.json"), []byte("{}"), 0o600))
	sourceWorkspace := filepath.Join(sourceHome, "workspace")
	require.NoError(t, os.MkdirAll(sourceWorkspace, 0o755))
	sourceAgent := filepath.Join(sourceWorkspace, "AGENTS.md")
	require.NoError(t, os.WriteFile(sourceAgent, []byte("# Imported agent"), 0o600))

	instance := NewMigrateInstance(Options{SourceHome: sourceHome})
	actions, warnings, err := instance.Plan(
		Options{WorkspaceOnly: true},
		sourceHome,
		targetHome,
	)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.NotEmpty(t, actions)
	assert.Equal(t, sourceAgent, actions[0].Source)
	assert.Equal(t, filepath.Join(targetHome, "workspace", "AGENT.md"), actions[0].Target)
}

func TestMigrateInstancePlansMappedAgentDefinitionCollision(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		wantAction ActionType
	}{
		{
			name:       "forced import backs up current file",
			opts:       Options{WorkspaceOnly: true, Force: true},
			wantAction: ActionBackup,
		},
		{
			name:       "refresh overwrites current file",
			opts:       Options{WorkspaceOnly: true, Refresh: true},
			wantAction: ActionCopy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceHome := t.TempDir()
			targetHome := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "openclaw.json"), []byte("{}"), 0o600))
			sourceWorkspace := filepath.Join(sourceHome, "workspace")
			targetWorkspace := filepath.Join(targetHome, "workspace")
			require.NoError(t, os.MkdirAll(sourceWorkspace, 0o755))
			require.NoError(t, os.MkdirAll(targetWorkspace, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(sourceWorkspace, "AGENTS.md"), []byte("source"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(targetWorkspace, "AGENT.md"), []byte("target"), 0o600))

			instance := NewMigrateInstance(Options{SourceHome: sourceHome})
			actions, _, err := instance.Plan(tt.opts, sourceHome, targetHome)
			require.NoError(t, err)
			require.NotEmpty(t, actions)
			assert.Equal(t, tt.wantAction, actions[0].Type)
		})
	}
}

func TestMigrateInstancePlanConfigOnlyAndWorkspaceOnlyMutuallyExclusive(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	err := os.WriteFile(configPath, []byte("{}"), 0o644)
	require.NoError(t, err)

	instance := NewMigrateInstance(Options{SourceHome: tmpDir})
	require.NotNil(t, instance)

	_, err = instance.Run(Options{
		ConfigOnly:    true,
		WorkspaceOnly: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestMigrateInstancePlanRefreshSetsWorkspaceOnly(t *testing.T) {
	opts := Options{
		Refresh:    true,
		SourceHome: "/tmp/nonexistent",
	}
	instance := NewMigrateInstance(opts)
	require.NotNil(t, instance)

	_, err := instance.Run(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMigrateInstancePlanSourceNotFound(t *testing.T) {
	opts := Options{
		SourceHome: "/tmp/nonexistent-source-home",
	}
	instance := NewMigrateInstance(opts)

	_, err := instance.Run(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMigrateInstanceExecute(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	targetDir := filepath.Join(tmpDir, "target")
	workspaceDir := filepath.Join(sourceDir, "workspace")

	err := os.MkdirAll(workspaceDir, 0o755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(workspaceDir, "test.txt"), []byte("test"), 0o644)
	require.NoError(t, err)

	instance := &MigrateInstance{
		options:  Options{Source: "mock"},
		handlers: make(map[string]Operation),
	}
	instance.Register("mock", &mockOperation{sourceHome: sourceDir, sourceWs: workspaceDir})

	actions := []Action{
		{
			Type:        ActionCopy,
			Source:      filepath.Join(workspaceDir, "test.txt"),
			Target:      filepath.Join(targetDir, "workspace", "test.txt"),
			Description: "copy file",
		},
	}

	result := instance.Execute(actions, workspaceDir, targetDir)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.FilesCopied)

	_, err = os.Stat(filepath.Join(targetDir, "workspace", "test.txt"))
	assert.NoError(t, err)
}

func TestMigrateInstanceExecuteWithInvalidSource(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	err := os.MkdirAll(sourceDir, 0o755)
	require.NoError(t, err)

	instance := &MigrateInstance{
		options:  Options{Source: "mock"},
		handlers: make(map[string]Operation),
	}
	instance.Register("mock", &mockOperation{sourceHome: sourceDir})

	actions := []Action{
		{
			Type:        ActionCopy,
			Source:      filepath.Join(sourceDir, "nonexistent.txt"),
			Target:      filepath.Join(tmpDir, "target.txt"),
			Description: "copy file",
		},
	}

	result := instance.Execute(actions, sourceDir, tmpDir)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.FilesCopied)
	assert.Greater(t, len(result.Errors), 0)
}

func TestMigrateInstanceExecuteCreateDir(t *testing.T) {
	tmpDir := t.TempDir()

	instance := &MigrateInstance{
		options:  Options{Source: "mock"},
		handlers: make(map[string]Operation),
	}
	instance.Register("mock", &mockOperation{})

	actions := []Action{
		{
			Type:        ActionCreateDir,
			Target:      filepath.Join(tmpDir, "new", "dir"),
			Description: "create directory",
		},
	}

	result := instance.Execute(actions, "", "")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.DirsCreated)

	_, err := os.Stat(filepath.Join(tmpDir, "new", "dir"))
	assert.NoError(t, err)
}

func TestMigrateInstanceExecuteBackup(t *testing.T) {
	tmpDir := t.TempDir()

	sourceFile := filepath.Join(tmpDir, "source.txt")
	targetFile := filepath.Join(tmpDir, "target.txt")

	err := os.WriteFile(sourceFile, []byte("source"), 0o644)
	require.NoError(t, err)

	err = os.WriteFile(targetFile, []byte("target"), 0o644)
	require.NoError(t, err)

	instance := &MigrateInstance{
		options:  Options{Source: "mock"},
		handlers: make(map[string]Operation),
	}
	instance.Register("mock", &mockOperation{})

	actions := []Action{
		{
			Type:        ActionBackup,
			Source:      sourceFile,
			Target:      targetFile,
			Description: "backup and overwrite",
		},
	}

	result := instance.Execute(actions, tmpDir, tmpDir)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.BackupsCreated)
	assert.Equal(t, 1, result.FilesCopied)

	bakFile := targetFile + ".bak"
	_, err = os.Stat(bakFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(targetFile)
	assert.NoError(t, err)
	assert.Equal(t, "source", string(content))
}

func TestMigrateInstanceExecuteSkip(t *testing.T) {
	instance := &MigrateInstance{
		options:  Options{Source: "mock"},
		handlers: make(map[string]Operation),
	}
	instance.Register("mock", &mockOperation{})

	actions := []Action{
		{
			Type:        ActionSkip,
			Source:      "/tmp/source.txt",
			Target:      "/tmp/target.txt",
			Description: "skip file",
		},
	}

	result := instance.Execute(actions, "", "")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.FilesSkipped)
}

func TestMigrateInstancePrintSummary(t *testing.T) {
	instance := NewMigrateInstance(Options{})

	result := &Result{
		FilesCopied:    5,
		ConfigMigrated: true,
		BackupsCreated: 2,
		FilesSkipped:   3,
		Warnings:       []string{"warning 1"},
		Errors:         []error{},
	}

	instance.PrintSummary(result)
}

func TestMigrateInstancePrintSummaryWithErrors(t *testing.T) {
	instance := NewMigrateInstance(Options{})

	result := &Result{
		FilesCopied:    0,
		ConfigMigrated: false,
		BackupsCreated: 0,
		FilesSkipped:   0,
		Warnings:       []string{},
		Errors:         []error{assert.AnError},
	}

	instance.PrintSummary(result)
}

func TestMigrateInstancePrintSummaryNoActions(t *testing.T) {
	instance := NewMigrateInstance(Options{})

	result := &Result{
		FilesCopied:    0,
		ConfigMigrated: false,
		BackupsCreated: 0,
		FilesSkipped:   0,
		Warnings:       []string{},
		Errors:         []error{},
	}

	instance.PrintSummary(result)
}

func TestPrintPlan(t *testing.T) {
	actions := []Action{
		{
			Type:        ActionConvertConfig,
			Source:      "/source/config.json",
			Target:      "/target/config.json",
			Description: "convert config",
		},
		{
			Type:        ActionCopy,
			Source:      "/source/workspace/AGENTS.md",
			Target:      "/target/workspace/AGENT.md",
			Description: "copy file",
		},
		{
			Type:        ActionBackup,
			Source:      "/source/existing.txt",
			Target:      "/target/existing.txt",
			Description: "backup and overwrite",
		},
		{
			Type:        ActionSkip,
			Source:      "/source/skipped.txt",
			Target:      "/target/skipped.txt",
			Description: "skip file",
		},
		{
			Type:        ActionCreateDir,
			Target:      "/target/newdir",
			Description: "create directory",
		},
	}

	warnings := []string{
		"Warning: source directory not found",
	}

	var output bytes.Buffer
	PrintPlan(&output, actions, warnings)
	assert.Contains(t, output.String(), "[copy]    AGENTS.md -> AGENT.md")
	assert.Equal(
		t,
		"workspace/AGENTS.md -> workspace/AGENT.md",
		fileActionLabel(actions[1], "/source", "/target"),
	)
}

func TestPrintPlanEmpty(t *testing.T) {
	PrintPlan(&bytes.Buffer{}, []Action{}, []string{})
}

type mockOperation struct {
	sourceHome     string
	sourceConfig   string
	sourceWs       string
	workspaceFiles []internal.WorkspaceFile
	workspaceDirs  []string
}

func (m *mockOperation) GetSourceName() string { return "mock" }
func (m *mockOperation) GetSourceHome() (string, error) {
	if m.sourceHome != "" {
		return m.sourceHome, nil
	}
	return "/tmp/mock", nil
}

func (m *mockOperation) GetSourceWorkspace() (string, error) {
	if m.sourceWs != "" {
		return m.sourceWs, nil
	}
	if m.sourceHome != "" {
		return filepath.Join(m.sourceHome, "workspace"), nil
	}
	return "/tmp/mock/workspace", nil
}

func (m *mockOperation) GetSourceConfigFile() (string, error) {
	if m.sourceConfig != "" {
		return m.sourceConfig, nil
	}
	return "/tmp/mock/config.json", nil
}
func (m *mockOperation) ExecuteConfigMigration(src, dst string) error { return nil }
func (m *mockOperation) WorkspaceFiles() []internal.WorkspaceFile {
	if m.workspaceFiles != nil {
		return m.workspaceFiles
	}
	return nil
}

func (m *mockOperation) WorkspaceDirs() []string {
	if m.workspaceDirs != nil {
		return m.workspaceDirs
	}
	return nil
}
