package privilege

import (
	"context"
	"strings"
	"testing"
)

type testExecutor struct{ binding Binding }

func (executor testExecutor) Binding() Binding { return executor.binding }
func (testExecutor) Execute(context.Context, Request) (Result, error) {
	return Result{}, nil
}

func TestBindingAndRequestValidation(t *testing.T) {
	binding := Binding{
		Backend: BackendAuthorityBroker, Endpoint: "/run/mintclaw/root.sock",
		BrokerRevision: "broker-one", Profile: "root", ProfileRevision: "profile-one",
		WorkingScope: "machine", TimeoutSecondsMax: 60, OutputBytesMax: 4096,
	}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	request := Request{InvocationID: "call-one", Script: "id -u", TimeoutSeconds: 30}
	if err := request.Validate(binding); err != nil {
		t.Fatal(err)
	}
	request.Script = strings.Repeat("x", MaxScriptBytes+1)
	if err := request.Validate(binding); err == nil {
		t.Fatal("oversized privileged script was accepted")
	}
}

func TestRequireFailsClosed(t *testing.T) {
	if _, err := Require(nil); err == nil {
		t.Fatal("nil privileged executor was accepted")
	}
	if _, err := Require(testExecutor{}); err == nil {
		t.Fatal("invalid privileged executor binding was accepted")
	}
}
