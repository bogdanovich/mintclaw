//go:build linux

package companion

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
)

func TestCodingPrivilegeExecutorRequiresRootProfileThroughRealBroker(t *testing.T) {
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
	if profile.UID == 0 && profile.GID == 0 {
		if err != nil || result.ExitCode != 0 || result.Stdout == "" {
			t.Fatalf("real privileged coding result = %#v, %v", result, err)
		}
		return
	}
	if err == nil || result != (privilege.Result{}) {
		t.Fatalf("non-root real broker result = %#v, %v (uid=%d gid=%d)", result, err, profile.UID, profile.GID)
	}
}
