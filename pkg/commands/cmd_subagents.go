package commands

import (
	"context"
	"fmt"
)

// TurnInfo is a mirrored struct from agent.TurnInfo to avoid circular dependencies.
type TurnInfo struct {
	TurnID       string
	ParentTurnID string
	Depth        int
	ChildTurnIDs []string
	IsFinished   bool
}

func subagentsCommand() Definition {
	return Definition{
		Name:        "subagents",
		Description: "Show running subagents and task tree",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			getTurnFn := rt.GetCurrentTurn
			if getTurnFn == nil {
				return req.Reply("Runtime does not support querying active turns.")
			}

			turn := getTurnFn()
			if turn == nil {
				return req.Reply("No active tasks running in this session.")
			}
			return req.Reply(fmt.Sprintf("🤖 **Active Subagents List**\n```text\n%+v\n```", turn))
		},
	}
}
