package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.mau.fi/util/shlex"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw/internal"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func newEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the MintClaw config in $EDITOR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			editor := strings.TrimSpace(os.Getenv("EDITOR"))
			if editor == "" {
				return fmt.Errorf("$EDITOR is not set")
			}

			if _, err := loadConfig(); err != nil {
				return err
			}
			snapshot, err := loadDurableConfig()
			if err != nil {
				return err
			}

			editorArgs, err := shlex.Split(editor)
			if err != nil {
				return fmt.Errorf("failed to parse $EDITOR: %w", err)
			}
			if len(editorArgs) == 0 {
				return fmt.Errorf("$EDITOR is empty")
			}

			tempDir, err := os.MkdirTemp("", "mintclaw-mcp-edit-*")
			if err != nil {
				return fmt.Errorf("failed to create temporary config directory: %w", err)
			}
			defer func() { _ = os.RemoveAll(tempDir) }()

			tempPath := filepath.Join(tempDir, filepath.Base(internal.GetConfigPath()))
			tempRepository := config.NewRepository(tempPath)
			if _, err = tempRepository.Save(snapshot.Config); err != nil {
				return fmt.Errorf("failed to prepare editable config: %w", err)
			}

			editorArgs = append(editorArgs, tempPath)
			process := editorCommand(editorArgs[0], editorArgs[1:]...)
			process.Stdin = cmd.InOrStdin()
			process.Stdout = cmd.OutOrStdout()
			process.Stderr = cmd.ErrOrStderr()

			if err = editorProcessRun(process); err != nil {
				return fmt.Errorf("failed to start editor: %w", err)
			}

			edited, err := tempRepository.ReadDurable()
			if err != nil {
				return fmt.Errorf("failed to load edited config: %w", err)
			}
			if err = reconcileEditedModelCredentials(snapshot.Config, edited.Config); err != nil {
				return err
			}
			normalizedCfg, err := normalizeAndValidateConfig(edited.Config)
			if err != nil {
				return err
			}
			if _, err = mcpConfigRepository().Replace(snapshot.Revision, normalizedCfg); err != nil {
				if errors.Is(err, config.ErrConfigConflict) {
					return fmt.Errorf("config changed while editor was open: %w", err)
				}
				return fmt.Errorf("failed to save config: %w", err)
			}

			return nil
		},
	}
}

func reconcileEditedModelCredentials(original, edited *config.Config) error {
	if original == nil || edited == nil {
		return fmt.Errorf("config is nil")
	}
	originalByName := make(map[string]*config.ModelConfig, len(original.ModelList))
	editedNames := make(map[string]struct{}, len(edited.ModelList))
	for _, model := range original.ModelList {
		if model != nil {
			originalByName[model.ModelName] = model
		}
	}
	var addedNames []string
	for _, model := range edited.ModelList {
		if model == nil {
			continue
		}
		editedNames[model.ModelName] = struct{}{}
		originalModel, exists := originalByName[model.ModelName]
		if !exists {
			addedNames = append(addedNames, model.ModelName)
			continue
		}
		if len(model.APIKeys) == 0 {
			model.APIKeys = originalModel.APIKeys
		}
	}
	if len(addedNames) == 0 {
		return nil
	}
	for _, model := range original.ModelList {
		if model == nil || len(model.APIKeys) == 0 {
			continue
		}
		if _, exists := editedNames[model.ModelName]; !exists {
			return fmt.Errorf(
				"cannot rename credential-bearing model %q to %q in mcp edit; model credentials are stored separately",
				model.ModelName,
				addedNames[0],
			)
		}
	}
	return nil
}
