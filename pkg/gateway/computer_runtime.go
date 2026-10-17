package gateway

import (
	"context"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/computer"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	"github.com/bogdanovich/mintclaw/pkg/tools"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

type computerRuntime struct {
	client      *nodeComputerClient
	revision    string
	workspaceID string
}

type gatewayComputerToolSource struct {
	services    *services
	revision    string
	workspaceID string
}

func setupComputerTools(cfg *config.Config, loop *agent.AgentLoop, running *services) error {
	if cfg == nil || loop == nil || running == nil {
		return computer.ErrInvalid
	}
	if err := setupComputerRuntime(cfg, running); err != nil {
		return err
	}
	for _, name := range []string{"computer_targets", "computer_session", "computer_observe"} {
		if err := loop.RegisterRuntimeTool(name, func(reload *config.Config) (toolshared.Tool, error) {
			if reload.ValidateComputerConfig() != nil {
				return nil, computer.ErrDenied
			}
			source := &gatewayComputerToolSource{
				services: running, revision: reload.Tools.Computer.PolicyRevision(reload.Execution.Targets),
				workspaceID: computerNodeID("workspace", reload.WorkspacePath()),
			}
			options := tools.NewComputerToolOptions(reload.Tools.Computer)
			switch name {
			case "computer_targets":
				return tools.NewComputerTargetsTool(options, source), nil
			case "computer_session":
				return tools.NewComputerSessionTool(options, source), nil
			default:
				return tools.NewComputerObserveTool(options, source), nil
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func setupComputerRuntime(cfg *config.Config, running *services) error {
	if cfg == nil || running == nil {
		return computer.ErrInvalid
	}
	running.computerMu.Lock()
	defer running.computerMu.Unlock()
	if running.Computer != nil {
		return computer.ErrBusy
	}
	if !cfg.Tools.Computer.Enabled {
		return nil
	}
	client, err := newNodeComputerClient(cfg, running.NodeAdmission)
	if err != nil {
		return err
	}
	running.Computer = &computerRuntime{
		client:      client,
		revision:    cfg.Tools.Computer.PolicyRevision(cfg.Execution.Targets),
		workspaceID: client.workspaceID,
	}
	return nil
}

func (source *gatewayComputerToolSource) current() *nodeComputerClient {
	if source == nil || source.services == nil {
		return nil
	}
	runtime := source.services.Computer
	if runtime == nil || runtime.revision != source.revision || runtime.workspaceID != source.workspaceID {
		return nil
	}
	return runtime.client
}

func (source *gatewayComputerToolSource) Available() bool {
	if source == nil || source.services == nil {
		return false
	}
	source.services.computerMu.RLock()
	defer source.services.computerMu.RUnlock()
	return source.current() != nil
}

func computerRuntimeLock(ctx context.Context, try func() bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if try() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func withComputerClient[T any](
	ctx context.Context, source *gatewayComputerToolSource, call func(*nodeComputerClient) (T, error),
) (T, error) {
	var zero T
	if source == nil || source.services == nil {
		return zero, computer.ErrUnavailable
	}
	lock := &source.services.computerMu
	if err := computerRuntimeLock(ctx, lock.TryRLock); err != nil {
		return zero, err
	}
	defer lock.RUnlock()
	client := source.current()
	if client == nil || ctx.Err() != nil {
		return zero, computer.ErrStale
	}
	return call(client)
}

func (source *gatewayComputerToolSource) Targets(
	ctx context.Context,
	principal runtimecap.Principal,
	request string,
) ([]computer.NamedTarget, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) ([]computer.NamedTarget, error) {
		return client.Targets(ctx, principal, request)
	})
}

func (source *gatewayComputerToolSource) Open(
	ctx context.Context,
	principal runtimecap.Principal,
	target, request string,
) (computer.Session, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) (computer.Session, error) {
		return client.Open(ctx, principal, target, request)
	})
}

func (source *gatewayComputerToolSource) Status(
	ctx context.Context,
	principal runtimecap.Principal,
	id, request string,
) (computer.Session, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) (computer.Session, error) {
		return client.Status(ctx, principal, id, request)
	})
}

func (source *gatewayComputerToolSource) Control(
	ctx context.Context,
	principal runtimecap.Principal,
	id, request string,
	event computer.ControlEvent,
) (computer.Session, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) (computer.Session, error) {
		return client.Control(ctx, principal, id, request, event)
	})
}

func (source *gatewayComputerToolSource) Observe(
	ctx context.Context,
	principal runtimecap.Principal,
	id, request string,
) (computer.Observation, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) (computer.Observation, error) {
		return client.Observe(ctx, principal, id, request)
	})
}

func (source *gatewayComputerToolSource) OwnedSessions(
	ctx context.Context,
	principal runtimecap.Principal,
) ([]computer.Session, error) {
	return withComputerClient(ctx, source, func(client *nodeComputerClient) ([]computer.Session, error) {
		return client.OwnedSessions(ctx, principal)
	})
}

func closeComputerRuntime(ctx context.Context, running *services) error {
	if running == nil {
		return nil
	}
	if err := computerRuntimeLock(ctx, running.computerMu.TryLock); err != nil {
		return err
	}
	defer running.computerMu.Unlock()
	if running.Computer == nil {
		return nil
	}
	if err := running.Computer.client.Shutdown(ctx); err != nil {
		return err
	}
	running.Computer = nil
	return nil
}

var _ tools.ComputerToolSource = (*gatewayComputerToolSource)(nil)
