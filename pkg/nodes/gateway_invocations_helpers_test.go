package nodes

import (
	"encoding/json"
	"testing"
	"time"
)

func gatewayTestPlan(
	t *testing.T,
	invocationID string,
	idempotencyKey string,
	preparedAt time.Time,
) ExecutionPlan {
	t.Helper()
	request := invocationRequest(json.RawMessage(`{"argv":["git","status"]}`))
	request.InvocationID = invocationID
	request.IdempotencyKey = idempotencyKey
	plan, err := PrepareExecutionPlan(
		request,
		invocationDescriptor(RiskWrite),
		"local",
		"policy-1",
		preparedAt,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func gatewayTestDescriptor() CommandDescriptor {
	return invocationDescriptor(RiskWrite)
}

func gatewayTestPrincipal(plan ExecutionPlan) GatewayInvocationPrincipal {
	return GatewayInvocationPrincipal{
		AgentID:   plan.AgentID,
		SessionID: plan.SessionID,
		ActorID:   plan.ActorID,
	}
}

func gatewayTestOwner(
	target string,
	toolCallID string,
	plan ExecutionPlan,
) GatewayInvocationOwner {
	return GatewayInvocationOwner{
		Target:     target,
		AgentID:    plan.AgentID,
		SessionID:  plan.SessionID,
		ActorID:    plan.ActorID,
		ToolCallID: toolCallID,
	}
}
