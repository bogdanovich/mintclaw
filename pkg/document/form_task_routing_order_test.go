package document

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormTaskRoutingDelayedPromotionKeepsOriginalContextAndIngressOrder(t *testing.T) {
	store, options := newTestFormJobStore(t)
	request := intakeFixtureRequest(t, store)
	started, err := store.StartFormIntake(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	originalContext := request.Context
	route, err := store.BeginFormTaskRoute(t.Context(), FormTaskRouteRequest{
		JobID: request.JobID, ModeID: started.Progress.ModeID, Owner: request.Owner,
		ExpectedRevision: started.Job.Revision, MessageIdentity: "first-arrival-before-interpretation",
		Text: "PRIVATE_EARLIER_FACTS",
	})
	if err != nil {
		t.Fatal(err)
	}
	laterRequest := request
	laterRequest.Context = cloneFormIntakeContext(request.Context)
	laterRequest.Context.Question = "A different later question"
	laterRequest.Context.RoleContext = "Later role context, not the original question"
	later, err := store.UpdateFormIntakeContext(t.Context(), laterRequest, started.Progress.ModeID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AdmitFormIntakeMessage(t.Context(), intakeMessage(
		request,
		started.Progress.ModeID,
		later.Revision,
		"second-arrival-accepted-first",
		"PRIVATE_LATER_CLARIFICATION",
	))
	if err != nil {
		t.Fatal(err)
	}
	route = decideTaskRouteFixture(t, store, request, route, FormTaskContinue)
	first, err := store.AdmitFormIntakeMessage(t.Context(), FormIntakeMessageRequest{
		JobID: request.JobID, ModeID: route.ModeID, ExpectedRevision: route.Revision, Owner: request.Owner,
		Schema: request.Schema, MessageIdentity: route.Reference, RoutingReference: route.Reference,
		Text: "PRIVATE_EARLIER_FACTS",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	a, err := store.ReadFormIntakeInput(t.Context(), first.Receipt, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.ReadFormIntakeInput(t.Context(), second.Receipt, request.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if a.AdmissionOrder != 1 || b.AdmissionOrder != 2 || a.Context.Question != originalContext.Question ||
		a.Context.RoleContext != originalContext.RoleContext || b.Context.Question != laterRequest.Context.Question {
		t.Fatal("interpretation completion time replaced native ingress order or the original question context")
	}
	encoded, err := json.Marshal([]FormIntakeInput{a, b})
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") ||
		strings.Contains(string(encoded), "role context") {
		t.Fatal("private ordered input escaped ordinary JSON")
	}
}
