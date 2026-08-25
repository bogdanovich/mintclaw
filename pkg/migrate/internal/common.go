package internal

import (
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func ResolveTargetHome(override string) (string, error) {
	if override != "" {
		return fileutil.ExpandHome(override), nil
	}
	return config.GetHome(), nil
}

func ResolveWorkspace(homeDir string) string {
	return filepath.Join(homeDir, "workspace")
}

func PlanWorkspaceMigration(
	srcWorkspace, dstWorkspace string,
	files []WorkspaceFile,
	dirs []string,
	overwrite bool,
) ([]Action, error) {
	var actions []Action

	for _, file := range files {
		src := filepath.Join(srcWorkspace, file.Source)
		dst := filepath.Join(dstWorkspace, file.Target)
		action := planFileCopy(src, dst, overwrite)
		if action.Type != ActionSkip || action.Description != "" {
			actions = append(actions, action)
		}
	}

	for _, dirname := range dirs {
		srcDir := filepath.Join(srcWorkspace, dirname)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		dirActions, err := planDirCopy(srcDir, filepath.Join(dstWorkspace, dirname), overwrite)
		if err != nil {
			return nil, err
		}
		actions = append(actions, dirActions...)
	}

	return actions, nil
}

func planFileCopy(src, dst string, overwrite bool) Action {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return Action{
			Type:        ActionSkip,
			Source:      src,
			Target:      dst,
			Description: "source file not found",
		}
	}

	_, dstExists := os.Stat(dst)
	if dstExists == nil && !overwrite {
		return Action{
			Type:        ActionBackup,
			Source:      src,
			Target:      dst,
			Description: "destination exists, will backup and overwrite",
		}
	}

	return Action{
		Type:        ActionCopy,
		Source:      src,
		Target:      dst,
		Description: "copy file",
	}
}

func planDirCopy(srcDir, dstDir string, overwrite bool) ([]Action, error) {
	var actions []Action

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		dst := filepath.Join(dstDir, relPath)

		if info.IsDir() {
			actions = append(actions, Action{
				Type:        ActionCreateDir,
				Target:      dst,
				Description: "create directory",
			})
			return nil
		}

		action := planFileCopy(path, dst, overwrite)
		actions = append(actions, action)
		return nil
	})

	return actions, err
}

func RelPath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}

// CopyFile copies src to dst, preserving the source file's permission bits
// and committing the write atomically (via fileutil.CopyFile).
func CopyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return fileutil.CopyFile(src, dst, info.Mode().Perm())
}
