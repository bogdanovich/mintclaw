package agent

import (
	"errors"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type codingFilesystemToolContributor struct {
	workspace string
	cfg       *config.Config
	readOnly  bool
}

func (codingFilesystemToolContributor) Name() string {
	return "coding.filesystem"
}

func (contributor codingFilesystemToolContributor) Contribute(plan tools.RuntimeToolContribution) error {
	maxReadFileSize := contributor.cfg.Tools.ReadFile.MaxReadFileSize
	for _, tool := range []toolshared.Tool{
		fstools.NewReadFileBytesTool(contributor.workspace, contributor.readOnly, maxReadFileSize, nil),
		fstools.NewListDirTool(contributor.workspace, contributor.readOnly, nil),
		fstools.NewSearchFilesTool(contributor.workspace, contributor.readOnly, maxReadFileSize, nil),
	} {
		if err := plan.Add(tool); err != nil {
			return err
		}
	}
	if contributor.readOnly {
		return nil
	}
	if err := plan.Add(fstools.NewAppendFileTool(contributor.workspace, false, nil)); err != nil {
		return err
	}
	writeTool := fstools.NewWriteFileTool(contributor.workspace, false, nil)
	writeTool.SetAlternativeTools([]string{"append_file"})
	if err := plan.Add(writeTool); err != nil {
		return err
	}
	return plan.Add(fstools.NewApplyPatchTool(contributor.workspace, false, nil))
}

type codingInteractionToolContributor struct {
	cfg *config.Config
}

func (codingInteractionToolContributor) Name() string {
	return "coding.interaction"
}

func (contributor codingInteractionToolContributor) Contribute(plan tools.RuntimeToolContribution) error {
	if !contributor.cfg.Tools.IsToolEnabled("request_user_input") {
		return nil
	}
	requestTool, err := tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{
		DefaultTimeout: contributor.cfg.Tools.RequestUserInput.DefaultTimeout(),
		MaxTimeout:     contributor.cfg.Tools.RequestUserInput.MaxTimeout(),
	})
	if err != nil {
		return fmt.Errorf("initialize coding request_user_input tool: %w", err)
	}
	return plan.Add(requestTool)
}

type codingExecutionToolContributor struct {
	workingDirectory   string
	execScratch        string
	cfg                *config.Config
	readOnly           bool
	privilegedExecutor privilege.Executor
}

func (codingExecutionToolContributor) Name() string {
	return "coding.execution"
}

func (contributor codingExecutionToolContributor) Contribute(plan tools.RuntimeToolContribution) error {
	if !contributor.readOnly {
		execCfg := *contributor.cfg
		execCfg.Tools = contributor.cfg.Tools
		execCfg.Tools.Exec = config.ExecConfig{TimeoutSeconds: contributor.cfg.Tools.Exec.TimeoutSeconds}
		execTool, err := tools.NewCodingExecToolWithRuntimeConfig(
			contributor.workingDirectory,
			contributor.execScratch,
			&execCfg,
		)
		if err != nil {
			return fmt.Errorf("initialize coding exec tool: %w", err)
		}
		if err := plan.Add(execTool); err != nil {
			return err
		}
	}
	if contributor.privilegedExecutor == nil {
		return nil
	}
	privilegedTool, err := tools.NewPrivilegedExecTool(contributor.privilegedExecutor)
	if err != nil {
		return fmt.Errorf("initialize coding privileged_exec tool: %w", err)
	}
	return plan.Add(privilegedTool)
}

type codingRepositoryToolContributor struct {
	repository *codingworkspace.Repository
}

func (codingRepositoryToolContributor) Name() string {
	return "coding.repository"
}

func (contributor codingRepositoryToolContributor) Contribute(plan tools.RuntimeToolContribution) error {
	for _, tool := range []toolshared.Tool{
		tools.NewUpdatePlanTool(),
		tools.NewRepositoryStatusTool(contributor.repository),
		tools.NewRepositoryDiffTool(contributor.repository),
	} {
		if err := plan.Add(tool); err != nil {
			return err
		}
	}
	return nil
}

type codingRemoteToolContributor struct {
	capability toolshared.Tool
	task       toolshared.Tool
}

func (codingRemoteToolContributor) Name() string {
	return "coding.remote"
}

func (contributor codingRemoteToolContributor) Contribute(plan tools.RuntimeToolContribution) error {
	if !runtimeDependencyIsNil(contributor.capability) {
		if contributor.capability.Name() != "remote_capability" {
			return errors.New("invalid trusted remote capability tool")
		}
		if err := plan.Add(contributor.capability); err != nil {
			return err
		}
	}
	if runtimeDependencyIsNil(contributor.task) {
		return nil
	}
	if contributor.task.Name() != "remote_coding_task" {
		return errors.New("invalid trusted remote coding task tool")
	}
	return plan.Add(contributor.task)
}

func buildCodingAgentTools(
	workspace string,
	workingDirectory string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
	repository *codingworkspace.Repository,
	readOnly bool,
	privilegedExecutor privilege.Executor,
	remoteCapability toolshared.Tool,
	remoteCodingTask toolshared.Tool,
) (*tools.ToolRegistry, error) {
	result, err := (tools.RuntimeToolPlan{
		Runtime: runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		Policy: func(name string) bool {
			return toolAllowedByPolicy(initCfg.toolPolicy, name)
		},
	}).Build(
		codingFilesystemToolContributor{workspace: workspace, cfg: cfg, readOnly: readOnly},
		codingInteractionToolContributor{cfg: cfg},
		codingExecutionToolContributor{
			workingDirectory:   workingDirectory,
			execScratch:        initCfg.execScratch,
			cfg:                cfg,
			readOnly:           readOnly,
			privilegedExecutor: privilegedExecutor,
		},
		codingRemoteToolContributor{capability: remoteCapability, task: remoteCodingTask},
		codingRepositoryToolContributor{repository: repository},
	)
	if err != nil {
		return nil, err
	}
	return result.Registry, nil
}
