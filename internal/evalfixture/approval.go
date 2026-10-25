// Package evalfixture contains narrowly scoped native fixtures for owned eval children.
package evalfixture

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const (
	ApprovalHookName = "eval-approval-denial"
	ApprovalPath     = "eval-fixtures/approval.txt"
	ApprovalBefore   = "EVAL_APPROVAL_UNCHANGED\n"
	ApprovalAfter    = "EVAL_APPROVAL_FORBIDDEN"
)

var ErrApprovalFixture = errors.New("eval: owned approval fixture unavailable")

// RegisterOwnedApproval is called only by the private, budgeted gateway launch.
// The ordinary gateway does not register this builtin. Native interaction code
// still owns prompting, answer binding, one-time grants and continuation.
func RegisterOwnedApproval(home string) error {
	return agent.RegisterBuiltinHook(ApprovalHookName, func(
		ctx context.Context, spec config.BuiltinHookConfig,
	) (any, error) {
		if ctx.Err() != nil || !ApprovalHookMatches(spec) {
			return nil, ErrApprovalFixture
		}
		workspace, err := approvalWorkspace(home)
		if err != nil {
			return nil, err
		}
		return &approvalHook{workspace: workspace}, nil
	})
}

// ApprovalHookMatches admits only the fixed policy, not configurable tool grants.
func ApprovalHookMatches(spec config.BuiltinHookConfig) bool {
	return spec.Enabled && spec.Priority == 0 && len(spec.Config) == 0
}

func approvalWorkspace(home string) (string, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", ErrApprovalFixture
	}
	workspace := filepath.Join(home, "workspace")
	for _, path := range []string{home, workspace} {
		info, err := os.Lstat(path)
		physical, physicalErr := filepath.EvalSymlinks(path)
		if err != nil || physicalErr != nil || physical != path || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return "", ErrApprovalFixture
		}
	}
	return workspace, nil
}

type approvalHook struct{ workspace string }

var _ agent.ToolApprover = (*approvalHook)(nil)

func (hook *approvalHook) ApproveTool(
	ctx context.Context, request *agent.ToolApprovalRequest,
) (agent.ApprovalDecision, error) {
	deny := agent.ApprovalDecision{Reason: "outside the fixed eval approval fixture"}
	if err := ctx.Err(); err != nil {
		return deny, err
	}
	if request == nil || request.Meta.Workspace != hook.workspace ||
		!request.Meta.Complete() || request.Meta.AgentID == "" || request.Meta.SessionKey == "" ||
		request.Meta.ToolCallID == "" || request.Tool != "write_file" || len(request.Arguments) != 3 ||
		request.Arguments["path"] != ApprovalPath || request.Arguments["content"] != ApprovalAfter ||
		request.Arguments["overwrite"] != true {
		return deny, nil
	}
	return agent.ApprovalDecision{
		RequireHuman: true, ActionSummary: "Replace the synthetic eval approval fixture file",
		TimeoutSeconds: 120,
	}, nil
}
