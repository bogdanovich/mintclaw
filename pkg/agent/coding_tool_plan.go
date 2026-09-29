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

func newCodingFilesystemToolContributor(
	workspace string,
	cfg *config.Config,
	readOnly bool,
) runtimeToolSetContributor {
	maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
	candidates := []runtimeToolCandidate{
		{tool: fstools.NewReadFileBytesTool(workspace, readOnly, maxReadFileSize, nil)},
		{tool: fstools.NewListDirTool(workspace, readOnly, nil)},
		{tool: fstools.NewSearchFilesTool(workspace, readOnly, maxReadFileSize, nil)},
	}
	if readOnly {
		return newRuntimeToolSetContributor("coding.filesystem", candidates...)
	}
	candidates = append(candidates, runtimeToolCandidate{
		tool: fstools.NewAppendFileTool(workspace, false, nil),
	})
	writeTool := fstools.NewWriteFileTool(workspace, false, nil)
	writeTool.SetAlternativeTools([]string{"append_file"})
	candidates = append(
		candidates,
		runtimeToolCandidate{tool: writeTool},
		runtimeToolCandidate{tool: fstools.NewApplyPatchTool(workspace, false, nil)},
	)
	return newRuntimeToolSetContributor("coding.filesystem", candidates...)
}

func newCodingInteractionToolContributor(cfg *config.Config) (runtimeToolSetContributor, error) {
	contributor := newRuntimeToolSetContributor("coding.interaction")
	if !cfg.Tools.IsToolEnabled("request_user_input") {
		return contributor, nil
	}
	requestTool, err := tools.NewRequestUserInputTool(tools.RequestUserInputToolOptions{
		DefaultTimeout: cfg.Tools.RequestUserInput.DefaultTimeout(),
		MaxTimeout:     cfg.Tools.RequestUserInput.MaxTimeout(),
	})
	if err != nil {
		return runtimeToolSetContributor{}, fmt.Errorf("initialize coding request_user_input tool: %w", err)
	}
	return newRuntimeToolSetContributor(
		"coding.interaction",
		runtimeToolCandidate{tool: requestTool},
	), nil
}

func newCodingExecutionToolContributor(
	workingDirectory string,
	execScratch string,
	cfg *config.Config,
	readOnly bool,
	privilegedExecutor privilege.Executor,
) (runtimeToolSetContributor, error) {
	candidates := make([]runtimeToolCandidate, 0, 2)
	if !readOnly {
		execCfg := *cfg
		execCfg.Tools = cfg.Tools
		execCfg.Tools.Exec = config.ExecConfig{TimeoutSeconds: cfg.Tools.Exec.TimeoutSeconds}
		execTool, err := tools.NewCodingExecToolWithRuntimeConfig(
			workingDirectory,
			execScratch,
			&execCfg,
		)
		if err != nil {
			return runtimeToolSetContributor{}, fmt.Errorf("initialize coding exec tool: %w", err)
		}
		candidates = append(candidates, runtimeToolCandidate{tool: execTool})
	}
	if privilegedExecutor != nil {
		privilegedTool, err := tools.NewPrivilegedExecTool(privilegedExecutor)
		if err != nil {
			return runtimeToolSetContributor{}, fmt.Errorf("initialize coding privileged_exec tool: %w", err)
		}
		candidates = append(candidates, runtimeToolCandidate{tool: privilegedTool})
	}
	return newRuntimeToolSetContributor("coding.execution", candidates...), nil
}

func newCodingRepositoryToolContributor(repository *codingworkspace.Repository) runtimeToolSetContributor {
	return newRuntimeToolSetContributor(
		"coding.repository",
		runtimeToolCandidate{tool: tools.NewUpdatePlanTool()},
		runtimeToolCandidate{tool: tools.NewRepositoryStatusTool(repository)},
		runtimeToolCandidate{tool: tools.NewRepositoryDiffTool(repository)},
	)
}

func newCodingRemoteToolContributor(
	capability toolshared.Tool,
	task toolshared.Tool,
) (runtimeToolSetContributor, error) {
	candidates := make([]runtimeToolCandidate, 0, 2)
	if !runtimeDependencyIsNil(capability) {
		if capability.Name() != "remote_capability" {
			return runtimeToolSetContributor{}, errors.New("invalid trusted remote capability tool")
		}
		candidates = append(candidates, runtimeToolCandidate{tool: capability})
	}
	if !runtimeDependencyIsNil(task) {
		if task.Name() != "remote_coding_task" {
			return runtimeToolSetContributor{}, errors.New("invalid trusted remote coding task tool")
		}
		candidates = append(candidates, runtimeToolCandidate{tool: task})
	}
	return newRuntimeToolSetContributor("coding.remote", candidates...), nil
}

func newCodingRemoteBrowserToolContributor(
	browserTools []toolshared.Tool,
) (runtimeToolSetContributor, error) {
	candidates := make([]runtimeToolCandidate, 0, len(browserTools))
	seen := make(map[string]struct{}, len(browserTools))
	for _, browserTool := range browserTools {
		if runtimeDependencyIsNil(browserTool) || !validCodingRemoteBrowserToolName(browserTool.Name()) {
			return runtimeToolSetContributor{}, errors.New("invalid trusted remote browser tool")
		}
		if _, duplicate := seen[browserTool.Name()]; duplicate {
			return runtimeToolSetContributor{}, errors.New("duplicate trusted remote browser tool")
		}
		seen[browserTool.Name()] = struct{}{}
		candidates = append(candidates, runtimeToolCandidate{tool: browserTool})
	}
	return newRuntimeToolSetContributor("coding.browser", candidates...), nil
}

func buildCodingAgentToolComposer(
	workspace string,
	workingDirectory string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
	repository *codingworkspace.Repository,
	readOnly bool,
	privilegedExecutor privilege.Executor,
	remoteCapability toolshared.Tool,
	remoteCodingTask toolshared.Tool,
	remoteBrowserTools []toolshared.Tool,
) (*runtimeToolComposer, error) {
	interaction, err := newCodingInteractionToolContributor(cfg)
	if err != nil {
		return nil, err
	}
	execution, err := newCodingExecutionToolContributor(
		workingDirectory,
		initCfg.execScratch,
		cfg,
		readOnly,
		privilegedExecutor,
	)
	if err != nil {
		return nil, err
	}
	remote, err := newCodingRemoteToolContributor(remoteCapability, remoteCodingTask)
	if err != nil {
		return nil, err
	}
	browser, err := newCodingRemoteBrowserToolContributor(remoteBrowserTools)
	if err != nil {
		return nil, err
	}
	return newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindCoding}),
		func(name string) bool {
			return toolAllowedByPolicy(initCfg.toolPolicy, name)
		},
		newCodingFilesystemToolContributor(workspace, cfg, readOnly),
		interaction,
		execution,
		remote,
		browser,
		newCodingRepositoryToolContributor(repository),
	)
}
