package agent

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/commands"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

func (al *AgentLoop) bindGoalCommandCapabilities(
	rt *commands.Runtime,
	modelBinding effectiveModelBinding,
	opts *turnSpec,
) {
	if al.state == nil || opts == nil {
		return
	}
	routeSessionKey := strings.TrimSpace(opts.Dispatch.RouteSessionKey)
	if routeSessionKey == "" {
		routeSessionKey = strings.TrimSpace(modelBinding.RouteSessionKey)
	}
	if routeSessionKey == "" {
		return
	}

	rt.GetGoal = func() (commands.GoalInfo, bool, error) {
		goal, found := al.state.GetSessionGoal(routeSessionKey)
		return commandGoalInfo(goal), found, nil
	}
	rt.CreateGoal = func(objective string) (commands.GoalInfo, error) {
		goal, err := al.state.CreateSessionGoal(routeSessionKey, objective)
		return commandGoalInfo(goal), err
	}
	rt.EditGoal = func(objective string) (commands.GoalInfo, error) {
		goal, err := al.state.EditSessionGoal(routeSessionKey, objective)
		return commandGoalInfo(goal), err
	}
	rt.SetGoalStatus = func(status, note string) (commands.GoalInfo, error) {
		goal, err := al.state.SetSessionGoalStatus(routeSessionKey, state.SessionGoalStatus(status), note)
		return commandGoalInfo(goal), err
	}
	rt.ClearGoal = func() error {
		return al.state.ClearSessionGoal(routeSessionKey)
	}
}

func commandGoalInfo(goal state.SessionGoal) commands.GoalInfo {
	return commands.GoalInfo{
		Objective:   goal.Objective,
		Status:      string(goal.Status),
		Note:        goal.Note,
		CreatedAt:   goal.CreatedAt,
		UpdatedAt:   goal.UpdatedAt,
		BlockedAt:   goal.BlockedAt,
		CompletedAt: goal.CompletedAt,
	}
}
