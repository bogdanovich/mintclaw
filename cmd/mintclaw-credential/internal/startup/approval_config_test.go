package startup

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

const syntheticApprovalConfig = `{
"version":1,"service_uid":65532,"service_gid":65532,"owner_id":"owner",
"origin":"https://approval.example.org","listen_address":"127.0.0.1:18443",
"state_directory":"/qual-approval-service/state",
"certificate_file":"/qual-approval-service/tls.crt","private_key_file":"/qual-approval-service/tls.key",
"accounts":[{"alias":"test-login","vault_id":"synthetic-vault","item_id":"synthetic-login",
"origins":["https://login.example.org"],"principals":[{
"owner_id":"owner","actor_id":"operator","agent_id":"mintclaw","host_id":"isolated"}]}]}`

func TestApprovalConfigStrictAuthorityAndSchema(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux approval service uses POSIX paths")
	}
	config, err := decodeApprovalConfig([]byte(syntheticApprovalConfig))
	if err != nil || config.Version != 1 || config.ServiceUID != 65532 || len(config.Accounts) != 1 {
		t.Fatal("synthetic approval configuration denied")
	}
	for _, body := range []string{
		"", "null", "{}", "[]", syntheticApprovalConfig + "{}", strings.Repeat("a", maxApprovalConfigBytes+1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"Version":1`, 1),
		strings.Replace(syntheticApprovalConfig, `"actor_id":"operator"`, `"actor_id":"operator","actor_id":"operator"`, 1),
		strings.Replace(syntheticApprovalConfig, `"actor_id":"operator"`, `"Actor_ID":"operator"`, 1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"version":2`, 1),
		strings.Replace(syntheticApprovalConfig, `"service_uid":65532`, `"service_uid":0`, 1),
		strings.Replace(syntheticApprovalConfig, `"service_gid":65532`, `"service_gid":0`, 1),
		strings.Replace(syntheticApprovalConfig, `"service_uid":65532`, `"service_uid":-1`, 1),
		strings.Replace(syntheticApprovalConfig, `"service_uid":65532`, `"service_uid":4294967296`, 1),
		strings.Replace(syntheticApprovalConfig, "https://approval.example.org", "http://approval.example.org", 1),
		strings.Replace(syntheticApprovalConfig, "https://approval.example.org", "https://approval.example.org/", 1),
		strings.Replace(syntheticApprovalConfig, "https://approval.example.org", "https://127.0.0.1", 1),
		strings.Replace(syntheticApprovalConfig, "https://approval.example.org", "https://approval.example.org?", 1),
		strings.Replace(syntheticApprovalConfig, "127.0.0.1:18443", "localhost:18443", 1),
		strings.Replace(syntheticApprovalConfig, "127.0.0.1:18443", "127.0.0.1:443", 1),
		strings.Replace(syntheticApprovalConfig, "/qual-approval-service/state", "/qual-approval-service/../state", 1),
		strings.Replace(syntheticApprovalConfig, "tls.key", "tls.crt", 1),
		strings.Replace(syntheticApprovalConfig, `"owner_id":"owner","actor_id"`, `"owner_id":"other","actor_id"`, 1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"token":"SYNTHETIC_PRIVATE_TOKEN","version":1`, 1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"credentials_directory":"/private","version":1`, 1),
		strings.Replace(syntheticApprovalConfig, `"version":1`, `"accounts":null,"version":1`, 1),
	} {
		if _, denied := decodeApprovalConfig([]byte(body)); !errors.Is(denied, errUnavailable) {
			t.Fatal("ambiguous or expanded approval configuration admitted")
		}
	}
}

func TestApprovalStartupDeniesCancellationAndTokenEnvironment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, denied := range []context.Context{nil, ctx} {
		if !errors.Is(ServeApproval(denied), errUnavailable) {
			t.Fatal("canceled approval startup admitted")
		}
	}
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "SYNTHETIC_PRIVATE_TOKEN")
	if err := ServeApproval(context.Background()); !errors.Is(err, errUnavailable) ||
		strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_TOKEN") {
		t.Fatal("environment token entered approval startup")
	}
}
