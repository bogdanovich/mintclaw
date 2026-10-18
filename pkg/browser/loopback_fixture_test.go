package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func fixtureNetworkProfile(origin string) config.BrowserProfileConfig {
	return config.BrowserProfileConfig{
		Enabled: true, Revision: "fixture-v1", Mode: config.BrowserProfileEphemeral, DryRun: true,
		NetworkMode: config.BrowserNetworkLoopbackFixture, AllowedOrigins: []string{origin},
		CapabilityMode: config.BrowserCapabilityRestricted, ApprovalMode: config.BrowserApprovalPolicy,
		Policy: &browserpolicy.Policy{DefaultDecision: browserpolicy.DecisionDeny},
	}
}

func TestLoopbackFixtureNetworkPinsOneLiteralDestinationWithoutDNS(t *testing.T) {
	for _, host := range []string{"127.0.0.1:34567", "[::1]:34567"} {
		lookups := 0
		policy, err := newBrowserNetworkPolicy(fixtureNetworkProfile("http://"+host),
			func(context.Context, string, string) ([]net.IP, error) {
				lookups++
				return nil, errors.New("unexpected DNS")
			}, nil)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := policy.destination(t.Context(), "http", host)
		if err != nil || len(addresses) != 1 || addresses[0] != host {
			t.Fatal("literal fixture authority was not preserved", addresses, err)
		}
		for _, forbidden := range []string{
			"127.0.0.1:34568", "[::1]:34568", "localhost:34567", "fixture.example:34567",
			"127.0.0.2:34567", "[::ffff:127.0.0.1]:34567", "127.1:34567", "2130706433:34567",
			"0177.0.0.1:34567", "169.254.169.254:34567", "192.168.1.1:34567", "8.8.8.8:34567",
		} {
			if _, err = policy.destination(t.Context(), "http", forbidden); !errors.Is(err, ErrDenied) {
				t.Fatal("fixture grant escaped its one literal destination", forbidden, err)
			}
		}
		if _, err = policy.destination(t.Context(), "https", host); !errors.Is(err, ErrDenied) || lookups != 0 {
			t.Fatal("fixture policy permitted a CONNECT/DNS path", lookups, err)
		}
	}
}

func TestLoopbackFixtureProxyBlocksRedirectsHostMismatchAndConnectBeforeDial(t *testing.T) {
	var requests, dials, lookups atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://localhost:34567/foreign", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "owned-fixture")
	}))
	t.Cleanup(fixture.Close)
	proxy, err := startBrowserNetworkProxy(fixtureNetworkProfile(fixture.URL),
		func(context.Context, string, string) ([]net.IP, error) {
			lookups.Add(1)
			return nil, errors.New("unexpected DNS")
		}, func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, address)
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, attempt := range []struct {
		url, host string
		status    int
	}{
		{fixture.URL, "", http.StatusOK},
		{fixture.URL + "/redirect", "", http.StatusForbidden},
		{fixture.URL, "localhost:34567", http.StatusForbidden},
		{"http://127.0.0.1:1024", "", http.StatusForbidden},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, attempt.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = attempt.host
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.ReadAll(io.LimitReader(response.Body, 128))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != attempt.status {
			t.Fatal("proxy response exceeded fixture authority", response.StatusCode, readErr, closeErr)
		}
	}
	connection, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err = connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fixtureURL, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", fixtureURL.Host, fixtureURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || requests.Load() != 2 || dials.Load() != 2 || lookups.Load() != 0 {
		t.Fatal(
			"denied fixture request reached DNS/upstream",
			response.StatusCode,
			requests.Load(),
			dials.Load(),
			lookups.Load(),
		)
	}
}

func TestLoopbackFixtureHostFactoryRevalidatesNarrowProfile(t *testing.T) {
	root, runtimeConfig := ephemeralPlaywrightConfig(t, false)
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	target.Driver, target.DriverServer = config.BrowserDriverPlaywrightLibrary, ""
	target.DriverExecutable = "/opt/mintclaw/playwright-library/sidecar.cjs"
	profile := fixtureNetworkProfile("http://127.0.0.1:34567")
	profile.AllowedAgents, profile.AllowedActors = []string{"browser"}, []string{"telegram:owner"}
	profile.Runtime = runtimeConfig
	target.Profiles["ephemeral"] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	factory, err := NewPlaywrightProfileWorkerFactory(root, "gateway", "ephemeral")
	if err != nil {
		t.Fatal("configured local fixture factory refused", err)
	}
	base := PlaywrightHostConfig{
		Target: "gateway", Profile: "ephemeral", Driver: config.BrowserDriverPlaywrightLibrary,
		ProfileConfig: factory.profileConfig, ServerConfig: factory.serverConfig,
	}
	if _, err = NewPlaywrightHostFactory(base); err != nil {
		t.Fatal("valid host profile refused", err)
	}
	for _, mutation := range []string{"mcp", "managed", "multiple", "default-allow", "hook", "execute"} {
		t.Run(mutation, func(t *testing.T) {
			host := base
			host.ProfileConfig = clonePlaywrightProfileConfig(base.ProfileConfig)
			switch mutation {
			case "mcp":
				host.Driver = config.BrowserDriverPlaywrightMCP
			case "managed":
				host.ProfileConfig.Mode = config.BrowserProfileManaged
			case "multiple":
				host.ProfileConfig.AllowedOrigins = append(host.ProfileConfig.AllowedOrigins, "http://127.0.0.1:34568")
			case "default-allow":
				host.ProfileConfig.Policy.DefaultDecision = browserpolicy.DecisionAllow
			case "hook":
				host.ProfileConfig.Policy.Hook = &browserpolicy.Hook{Command: []string{"/bin/false"}, TimeoutMS: 100}
			case "execute":
				host.ProfileConfig.PrivilegedExecution.Enabled = true
			}
			if _, factoryErr := NewPlaywrightHostFactory(host); !errors.Is(factoryErr, ErrDenied) {
				t.Fatal("host bypassed loopback fixture admission", mutation, factoryErr)
			}
		})
	}
}

func TestLoopbackFixtureBrokerAndDriverKeepTheSameOriginBoundary(t *testing.T) {
	const origin = "http://127.0.0.1:34567"
	profile := fixtureNetworkProfile(origin)
	lookups := 0
	broker := &Broker{
		config: config.BrowserToolsConfig{Targets: map[string]config.BrowserTargetConfig{
			"gateway": {Profiles: map[string]config.BrowserProfileConfig{"fixture": profile}},
		}},
		lookupIP: func(context.Context, string, string) ([]net.IP, error) {
			lookups++
			return nil, errors.New("unexpected DNS")
		},
	}
	session := Session{Target: "gateway", Profile: "fixture"}
	if !broker.originAllowed(session, origin) || !broker.originNetworkAllowed(t.Context(), session, origin) {
		t.Fatal("broker rejected the exact loopback fixture")
	}
	for _, forbidden := range []string{"http://127.0.0.1:34568", "http://localhost:34567", "https://example.com"} {
		if broker.originAllowed(session, forbidden) || broker.originNetworkAllowed(t.Context(), session, forbidden) {
			t.Fatal("broker widened loopback authority", forbidden)
		}
	}
	if lookups != 0 {
		t.Fatal("fixture mode unexpectedly resolved DNS")
	}
	server, err := playwrightServerWithNetworkPolicy(config.MCPServerConfig{Type: "stdio", Command: "node"}, profile,
		"http://127.0.0.1:45678")
	if err != nil || server.Env["PLAYWRIGHT_MCP_ALLOWED_ORIGINS"] != origin ||
		server.Env["PLAYWRIGHT_MCP_PROXY_BYPASS"] != "<-loopback>" ||
		!strings.Contains(strings.Join(server.Args, " "), "--allowed-origins "+origin) {
		t.Fatal("driver did not retain exact fixture/proxy scope", err)
	}
	if err = validateExecutionNetworkAuthority(
		profile.NetworkMode,
		profile.AllowedOrigins,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatal("fixture mode gained privileged execution authority", err)
	}
	profile.Policy.DefaultDecision = browserpolicy.DecisionAllow
	if _, err = newBrowserNetworkPolicy(profile, nil, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("proxy bypassed fixture profile admission", err)
	}
	if _, err = playwrightServerWithNetworkPolicy(
		server,
		profile,
		"http://127.0.0.1:45678",
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatal("driver bypassed fixture profile admission", err)
	}
}
