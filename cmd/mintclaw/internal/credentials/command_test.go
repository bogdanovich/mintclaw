package credentials

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

type privateTestTerminal struct{ bytes.Buffer }

func (*privateTestTerminal) Close() error { return nil }

func TestEnrollmentLinkOnlyReachesPrivateTerminal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Credentials = config.CredentialConfig{
		Enabled: true, OwnerID: "owner", Origin: "https://approval.example.org", StateDirectory: t.TempDir(),
		Accounts: []credentialbroker.AccountRule{
			{
				Alias:   "facebook",
				VaultID: "vault",
				ItemID:  "item",
				Origins: []string{"https://facebook.com"},
				Principals: []credentialbroker.Principal{
					{OwnerID: "owner", ActorID: "actor", AgentID: "agent", HostID: "host"},
				},
			},
		},
	}
	invite := cfg.Credentials.Origin + "/credentials/#enroll=" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/credentials/operator/enrollment" || request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer fixture-operator-token" {
			t.Error("private operator request lost authentication or routing")
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(writer, invite)
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal("fixture address unavailable")
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal("fixture port unavailable")
	}
	terminal := &privateTestTerminal{}
	command := newCredentialsCommand(commandDeps{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		readGateway: func() *pid.PidFileData {
			return &pid.PidFileData{Host: host, Port: number, Token: "fixture-operator-token"}
		},
		openTerminal: func() (io.WriteCloser, error) { return terminal, nil },
	})
	var standard bytes.Buffer
	command.SetOut(&standard)
	command.SetErr(&standard)
	command.SetArgs([]string{"enroll"})
	if command.Execute() != nil || !strings.Contains(terminal.String(), invite) || standard.Len() != 0 {
		t.Fatal("private enrollment did not stay outside stdout/stderr")
	}
}

func TestEnrollmentRejectsUnavailableTerminalBeforeRequest(t *testing.T) {
	requested := false
	command := newCredentialsCommand(commandDeps{
		openTerminal: func() (io.WriteCloser, error) { return nil, errSetupUnavailable },
		readGateway:  func() *pid.PidFileData { requested = true; return nil },
	})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"enroll"})
	if command.Execute() == nil || requested {
		t.Fatal("setup without a terminal issued an enrollment request")
	}
}
