//go:build linux || darwin

package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestEnforcingProxyChromiumFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_CREDENTIAL_BROWSER_FIXTURE") != "1" {
		t.Skip("synthetic real Chromium qualification only")
	}
	for _, mode := range []string{"login", "mfa", "bad-pin"} {
		t.Run(mode, func(t *testing.T) { runProxyBrowser(t, mode) })
	}
}

func runProxyBrowser(t *testing.T, mode string) {
	t.Helper()
	var receivedSubmission, receivedCookie atomic.Bool
	var approvedConnects atomic.Int32
	f := newProxyFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/login":
			_, _ = io.WriteString(writer, `<!doctype html><html><head><link rel="icon" href="data:,"></head><body>`+
				`<form id="signin" method="POST" action="/session"><input id="username" name="username" type="email">`+
				`<input id="password" name="password" type="password"><button>Login</button></form></body></html>`)
		case "/session":
			if request.Method != http.MethodPost || request.ParseForm() != nil ||
				request.PostForm.Get("username") != "synthetic@example.org" ||
				request.PostForm.Get("password") != "SYNTHETIC_PRIVATE_PROXY_PASSWORD" {
				t.Error("synthetic browser form changed")
				writer.WriteHeader(400)
				return
			}
			receivedSubmission.Store(true)
			writer.Header().Set("Set-Cookie", "session=SYNTHETIC_PRIVATE_PROXY_COOKIE; Secure; HttpOnly; Path=/")
			location := "/success"
			if mode == "mfa" {
				location = "/mfa"
			}
			writer.Header().Set("Location", location)
			writer.WriteHeader(http.StatusSeeOther)
		case "/success", "/mfa":
			cookie, err := request.Cookie("session")
			if err != nil || cookie.Value != "SYNTHETIC_PRIVATE_PROXY_COOKIE" {
				t.Error("private synthetic browser cookie unavailable")
				writer.WriteHeader(403)
				return
			}
			receivedCookie.Store(true)
			marker := "signed-in"
			if mode == "mfa" {
				marker = "mfa"
			}
			_, _ = io.WriteString(
				writer,
				`<!doctype html><html><head><link rel="icon" href="data:,"></head><body><p id="`+marker+`">Ready</p></body></html>`,
			)
		default:
			writer.WriteHeader(404)
		}
	}, func(classification string) {
		if classification == "approved-connect" {
			approvedConnects.Add(1)
		}
	})
	_, pin, err := f.proxy.Endpoint(context.Background())
	if err != nil {
		t.Fatal("private synthetic browser endpoint unavailable")
	}
	if mode == "bad-pin" {
		pin = base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	config := struct {
		Endpoint string                     `json:"endpoint"`
		Pin      string                     `json:"pin"`
		Mode     string                     `json:"mode"`
		Flow     credentialbroker.LoginFlow `json:"flow"`
	}{f.endpoint, pin, mode, proxyTestFlow}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal("synthetic browser configuration unavailable")
	}
	defer clear(encoded)
	filename, err := filepath.Abs("../../../../runtime/browser/credential-login/proxy-browser-fixture.cjs")
	if err != nil {
		t.Fatal("synthetic browser fixture path unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filename)
	command.Env = []string{
		"MINTCLAW_SYNTHETIC_CREDENTIAL_BROWSER_FIXTURE=1",
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"TZ=UTC",
	}
	command.Stderr = io.Discard
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal("synthetic private input unavailable")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal("synthetic fixed outcome unavailable")
	}
	if command.Start() != nil {
		t.Fatal("synthetic browser process unavailable")
	}
	finished := make(chan error, 1)
	t.Cleanup(func() { cancel(); _ = input.Close(); _ = command.Process.Kill() })
	lines := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		finished <- command.Wait()
	}()
	if _, err = input.Write(append(encoded, '\n')); err != nil {
		t.Fatal("synthetic private configuration write failed")
	}
	expect := func(expected string) {
		t.Helper()
		t.Logf("waiting for fixed browser outcome: %s", expected)
		select {
		case line, ok := <-lines:
			if !ok || line != expected {
				for _, fixed := range []string{"failed_configuration", "failed_launch", "failed_session", "failed_prepare", "failed_injection", "failed_submission"} {
					if line == fixed {
						t.Logf(
							"synthetic fixture stage: %s; upstream GETs: %d; POSTs: %d",
							fixed,
							f.getCount.Load(),
							f.postCount.Load(),
						)
					}
				}
				t.Fatalf("synthetic browser fixed outcome mismatch at %s", expected)
			}
		case <-ctx.Done():
			t.Fatalf("synthetic browser operation exceeded bound at %s", expected)
		}
	}
	if mode == "bad-pin" {
		expect("denied")
	} else {
		expect("prepared")
		if f.proxy.LockForInjection(ctx) != nil {
			t.Fatal("synthetic private injection lock failed")
		}
		if _, err = io.WriteString(input, "inject\n"); err != nil {
			t.Fatal("synthetic injection command failed")
		}
		expect("filled")
		if f.proxy.PermitSubmit(ctx) != nil {
			t.Fatal("synthetic trusted submit gate failed")
		}
		if _, err = io.WriteString(input, "submit\n"); err != nil {
			t.Fatal("synthetic submission command failed")
		}
		outcome := "logged_in"
		if mode == "mfa" {
			outcome = "handoff_required"
		}
		expect(outcome)
	}
	_ = input.Close()
	select {
	case result := <-finished:
		if result != nil {
			t.Fatal("synthetic browser cleanup failed")
		}
	case <-ctx.Done():
		t.Fatal("synthetic browser cleanup exceeded bound")
	}
	if mode == "bad-pin" {
		if approvedConnects.Load() == 0 {
			t.Fatal("incorrect pin denial did not reach the private TLS boundary")
		}
		if f.base.dials.Load() != 0 || receivedSubmission.Load() || receivedCookie.Load() {
			t.Fatal("untrusted proxy certificate reached upstream")
		}
	} else if !receivedSubmission.Load() || !receivedCookie.Load() || f.postCount.Load() != 1 || f.base.dials.Load() != 3 {
		t.Fatal("private browser credential or cookie lifecycle incomplete")
	}
	for _, value := range []string{"SYNTHETIC_PRIVATE_PROXY_PASSWORD", "SYNTHETIC_PRIVATE_PROXY_COOKIE", "synthetic@example.org"} {
		if strings.Contains(outcomeProxy(f.proxy), value) {
			t.Fatal("proxy public formatting exposed synthetic authentication material")
		}
	}
}

func outcomeProxy(proxy *LoginProxy) string {
	encoded, _ := json.Marshal(proxy)
	return string(encoded)
}
