package agent

import (
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	fstools "github.com/bogdanovich/mintclaw/pkg/tools/fs"
)

func newGatewayFilesystemToolContributor(
	workspace string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
) runtimeToolSetContributor {
	candidates := make([]runtimeToolCandidate, 0, 6)
	if cfg.Tools.IsToolEnabled("read_file") {
		maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
		if cfg.Tools.ReadFile.EffectiveMode() == config.ReadFileModeLines {
			candidates = append(candidates, runtimeToolCandidate{tool: fstools.NewReadFileLinesTool(
				workspace,
				initCfg.readRestrict,
				maxReadFileSize,
				initCfg.allowRead,
			)})
		} else {
			candidates = append(candidates, runtimeToolCandidate{tool: fstools.NewReadFileBytesTool(
				workspace,
				initCfg.readRestrict,
				maxReadFileSize,
				initCfg.allowRead,
			)})
		}
	}
	if cfg.Tools.IsToolEnabled("append_file") {
		candidates = append(candidates, runtimeToolCandidate{
			tool: fstools.NewAppendFileTool(workspace, initCfg.restrict, initCfg.allowWrite),
		})
	}
	if cfg.Tools.IsToolEnabled("write_file") {
		writeTool := fstools.NewWriteFileTool(workspace, initCfg.restrict, initCfg.allowWrite)
		var alternatives []string
		if cfg.Tools.IsToolEnabled("append_file") && toolAllowedByPolicy(initCfg.toolPolicy, "append_file") {
			alternatives = append(alternatives, "append_file")
		}
		writeTool.SetAlternativeTools(alternatives)
		candidates = append(candidates, runtimeToolCandidate{tool: writeTool})
	}
	if cfg.Tools.IsToolEnabled("list_dir") {
		candidates = append(candidates, runtimeToolCandidate{
			tool: fstools.NewListDirTool(workspace, initCfg.readRestrict, initCfg.allowRead),
		})
	}
	if cfg.Tools.IsToolEnabled("search_files") {
		candidates = append(candidates, runtimeToolCandidate{tool: fstools.NewSearchFilesTool(
			workspace,
			initCfg.readRestrict,
			cfg.Tools.ReadFile.MaxReadFileSize,
			initCfg.allowRead,
		)})
	}
	if cfg.Tools.IsToolEnabled("apply_patch") {
		candidates = append(candidates, runtimeToolCandidate{
			tool: fstools.NewApplyPatchTool(workspace, initCfg.restrict, initCfg.allowWrite),
		})
	}
	return newRuntimeToolSetContributor("gateway.filesystem", candidates...)
}

func newGatewayExecutionToolContributor(
	workspace string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
) runtimeToolSetContributor {
	contributor := newRuntimeToolSetContributor("gateway.execution")
	if !cfg.Tools.IsToolEnabled("exec") {
		return contributor
	}
	var (
		execTool *tools.ExecTool
		err      error
	)
	if initCfg.execScratch != "" {
		execTool, err = tools.NewExecToolWithRuntimeConfig(
			workspace,
			initCfg.execScratch,
			initCfg.restrict,
			cfg,
			initCfg.allowRead,
		)
	} else {
		execTool, err = tools.NewExecTool(workspace, initCfg.restrict, cfg, initCfg.allowRead)
	}
	if err != nil {
		logger.ErrorCF(
			"agent",
			"Failed to initialize exec tool; continuing without exec",
			map[string]any{"error": err.Error()},
		)
		return contributor
	}
	return newRuntimeToolSetContributor(
		"gateway.execution",
		runtimeToolCandidate{tool: execTool},
	)
}

func buildGatewayAgentToolComposer(
	workspace string,
	cfg *config.Config,
	initCfg agentToolInitConfig,
) (*runtimeToolComposer, error) {
	return newRuntimeToolComposer(
		runtimecap.NewContext(runtimecap.Inputs{Kind: runtimecap.KindGateway}),
		func(name string) bool {
			return toolAllowedByPolicy(initCfg.toolPolicy, name)
		},
		newGatewayFilesystemToolContributor(workspace, cfg, initCfg),
		newGatewayExecutionToolContributor(workspace, cfg, initCfg),
	)
}
