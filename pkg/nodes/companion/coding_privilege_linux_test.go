//go:build linux

package companion

import (
	"context"
	"errors"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
)

func TestCodingPrivilegeExecutorRunsThroughRealBrokerWorker(t *testing.T) {
	client, stop := startTestAuthorityBrokerServer(t, testAuthorityBrokerProcessRunner(t))
	defer stop()
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := snapshot.Profiles[0]
	executor := &codingPrivilegeExecutor{
		binding: privilege.Binding{
			Backend: privilege.BackendAuthorityBroker, Endpoint: client.socketPath,
			BrokerRevision: snapshot.Revision, Profile: profile.Alias,
			ProfileRevision: profile.Revision, WorkingScope: "workspace",
			TimeoutSecondsMax: profile.TimeoutSecondsMax, OutputBytesMax: profile.OutputBytesMax,
		},
		client: client,
	}
	result, err := executor.Execute(t.Context(), privilege.Request{
		InvocationID: "coding-root-real", Script: "id -u", TimeoutSeconds: 5,
	})
	if err != nil || result.ExitCode != 0 || result.Stdout == "" {
		t.Fatalf("real privileged coding result = %#v, %v", result, err)
	}
}

func TestPrepareCodingPrivilegeBindingRequiresExactBrokerRevision(t *testing.T) {
	client, stop := startTestAuthorityBrokerServer(t, &fakeAuthorityBrokerRunner{})
	defer stop()
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := snapshot.Profiles[0]
	policy := &CodingPrivilegePolicy{
		Backend: privilege.BackendAuthorityBroker, BrokerSocket: client.socketPath,
		BrokerRevision: snapshot.Revision, Profile: profile.Alias,
		ProfileRevision: profile.Revision, WorkingScope: "workspace",
	}
	binding, err := prepareCodingPrivilegeBindingWithBroker(t.Context(), policy, client)
	if err != nil || binding == nil || binding.BrokerRevision != snapshot.Revision {
		t.Fatalf("prepared coding privilege binding = %#v, %v", binding, err)
	}
	policy.BrokerRevision = "broker-stale"
	if _, err = prepareCodingPrivilegeBindingWithBroker(t.Context(), policy, client); err == nil {
		t.Fatal("stale coding broker revision was accepted")
	}
}

func TestCodingPrivilegeExecutorPreservesBrokerCancellationProof(t *testing.T) {
	runner := &fakeAuthorityBrokerRunner{started: make(chan struct{}), block: true}
	client, stop := startTestAuthorityBrokerServer(t, runner)
	defer stop()
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := snapshot.Profiles[0]
	executor := &codingPrivilegeExecutor{
		binding: privilege.Binding{
			Backend: privilege.BackendAuthorityBroker, Endpoint: client.socketPath,
			BrokerRevision: snapshot.Revision, Profile: profile.Alias,
			ProfileRevision: profile.Revision, WorkingScope: "workspace",
			TimeoutSecondsMax: profile.TimeoutSecondsMax, OutputBytesMax: profile.OutputBytesMax,
		},
		client: client,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, executeErr := executor.Execute(ctx, privilege.Request{
			InvocationID: "coding-root-cancel", Script: "while :; do :; done", TimeoutSeconds: 5,
		})
		done <- executeErr
	}()
	<-runner.started
	cancel()
	if err = <-done; !errors.Is(err, privilege.ErrCancellationConfirmed) {
		t.Fatalf("coding privilege cancellation = %v", err)
	}
}
