package eval

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fixtureHostGet(t *testing.T, host *browserFixtureHost, path string, status int) []byte {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(host.origin + path)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
	closeErr := response.Body.Close()
	if response.StatusCode != status || readErr != nil || closeErr != nil || len(data) > 16384 {
		t.Fatal("fixture host response mismatch", response.StatusCode, readErr, closeErr)
	}
	return data
}

func TestBrowserFixtureHostKeepsOneOriginAndSeparatesSerialTrials(t *testing.T) {
	host := newBrowserFixtureHostForTest(t)
	firstID := uuid.NewString()
	first, err := host.newTrial(firstID)
	if err != nil {
		t.Fatal(err)
	}
	fixtureHostGet(t, host, first.path, http.StatusOK)
	if fixture, err := host.newTrial(uuid.NewString()); fixture != nil || !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("host admitted overlapping trials")
	}
	first.seal()
	fixtureHostGet(t, host, first.path, http.StatusGone)
	if listener, err := net.Listen("tcp", host.listener.Addr().String()); err == nil {
		_ = listener.Close()
		t.Fatal("sealing released the pinned origin")
	}
	if fixture, err := host.newTrial(firstID); fixture != nil || !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("retired trial identity was adopted or replaced")
	}
	second, err := host.newTrial(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if first.origin != second.origin || second.origin != host.origin || first.path == second.path ||
		first.nonce == second.nonce || first.code == second.code {
		t.Fatal("serial trials did not retain the origin and refresh private authority")
	}
	fixtureHostGet(t, host, second.path, http.StatusOK)
	fixtureHostGet(t, host, first.path, http.StatusGone)
	fixtureHostGet(t, host, "/eval/"+uuid.NewString()+"/", http.StatusNotFound)

	wrongNonce := browserFixtureForm(second)
	wrongNonce.Set("nonce", first.nonce)
	response := httptest.NewRecorder()
	host.ServeHTTP(response, browserFixtureRequest(second, "submit", wrongNonce))
	if response.Code != http.StatusBadRequest {
		t.Fatal("earlier trial nonce authorized the current form")
	}
	response = httptest.NewRecorder()
	host.ServeHTTP(response, browserFixtureRequest(first, "submit", browserFixtureForm(first)))
	if response.Code != http.StatusGone {
		t.Fatal("retired path accepted a late mutation")
	}
	response = httptest.NewRecorder()
	host.ServeHTTP(response, browserFixtureRequest(second, "submit", browserFixtureForm(second)))
	if response.Code != http.StatusOK || len(first.snapshot().submissions) != 0 ||
		len(second.snapshot().submissions) != 1 || first.snapshot().visits != 1 || second.snapshot().visits != 1 {
		t.Fatal("host mixed independent trial witnesses")
	}
	if encoded, err := json.Marshal(host); !errors.Is(err, ErrBrowserFixture) || len(encoded) != 0 {
		t.Fatal("private fixture owner became report data")
	}
}

func TestBrowserFixtureHostUsesCanonicalIPv6WhenAvailable(t *testing.T) {
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable on this host")
	}
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
	host, err := newBrowserFixtureHost("[::1]:0")
	if err != nil {
		t.Fatal("available canonical IPv6 was refused", err)
	}
	t.Cleanup(func() {
		if err := host.close(); err != nil {
			t.Error(err)
		}
	})
	fixture, err := host.newTrial(uuid.NewString())
	if err != nil || !strings.HasPrefix(fixture.origin, "http://[::1]:") {
		t.Fatal("IPv6 fixture origin is not canonical", err)
	}
	fixtureHostGet(t, host, fixture.path, http.StatusOK)
}

func TestBrowserFixtureHostRejectsAmbiguousRoutesWithoutDispatch(t *testing.T) {
	host := newBrowserFixtureHostForTest(t)
	fixture, err := host.newTrial(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Host = "localhost:34567" },
		func(r *http.Request) { r.Host = "127.0.0.2:34567" },
		func(r *http.Request) { r.URL.RawQuery = "private-canary" },
		func(r *http.Request) { r.URL.ForceQuery = true },
		func(r *http.Request) { r.URL.RawPath = "/%65val/private-canary/" },
	} {
		request := httptest.NewRequest(http.MethodGet, fixture.origin+fixture.path, nil)
		mutate(request)
		response := httptest.NewRecorder()
		host.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "private-canary") {
			t.Fatal("ambiguous fixture route reached a trial or echoed its input")
		}
	}
	for _, path := range []string{"/", "/eval/", "/eval/private-canary", "/eval/private-canary/", fixture.path + "other"} {
		response := httptest.NewRecorder()
		host.ServeHTTP(response, httptest.NewRequest(http.MethodGet, host.origin+path, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "private-canary") {
			t.Fatal("foreign route reached a trial or echoed its input")
		}
	}
	if state := fixture.snapshot(); state.visits != 0 || state.downloads != 0 || len(state.submissions) != 0 {
		t.Fatal("invalid route changed a fixture witness")
	}
}

type gatedFixtureBody struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	read    sync.Once
	close   sync.Once
}

func (body *gatedFixtureBody) Read(data []byte) (int, error) {
	body.read.Do(func() { close(body.entered) })
	<-body.release
	return body.reader.Read(data)
}

func (body *gatedFixtureBody) Close() error {
	body.close.Do(func() { close(body.release) })
	return nil
}

func TestBrowserFixtureHostLateBodyCannotWriteAcrossTrialTransition(t *testing.T) {
	host := newBrowserFixtureHostForTest(t)
	first, err := host.newTrial(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	request := browserFixtureRequest(first, "submit", browserFixtureForm(first))
	body := &gatedFixtureBody{reader: request.Body, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { _ = body.Close() })
	request.Body = body
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		host.ServeHTTP(response, request)
	}()
	select {
	case <-body.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("mutation did not enter body read")
	}
	first.seal()
	second, err := host.newTrial(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("late mutation handler did not finish")
	}
	if response.Code != http.StatusGone || len(first.snapshot().submissions) != 0 ||
		len(second.snapshot().submissions) != 0 {
		t.Fatal("late body wrote to a sealed or subsequent trial")
	}
}

func TestBrowserFixtureHostBoundsLifetimeAndJoinsConcurrentShutdown(t *testing.T) {
	host := newBrowserFixtureHostForTest(t)
	var fixtures []*browserFixture
	for range MaxCorpusCases {
		fixture, err := host.newTrial(uuid.NewString())
		if err != nil {
			t.Fatal("bounded cohort was refused", err)
		}
		fixtures = append(fixtures, fixture)
		fixture.seal()
	}
	if fixture, err := host.newTrial(uuid.NewString()); fixture != nil || !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("host discarded retired identities to exceed its lifetime bound")
	}
	closed := make(chan error, 4)
	var workers sync.WaitGroup
	for range cap(closed) {
		workers.Go(func() { closed <- host.close() })
	}
	workers.Wait()
	close(closed)
	for err := range closed {
		if err != nil {
			t.Fatal("concurrent host shutdown failed", err)
		}
	}
	select {
	case <-host.done:
	default:
		t.Fatal("shutdown did not join the server")
	}
	for _, fixture := range fixtures {
		if state := fixture.snapshot(); !state.closed || state.failed {
			t.Fatal("host lost a closed healthy trial witness")
		}
	}
	if fixture, err := host.newTrial(uuid.NewString()); fixture != nil || !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("closed host admitted a new trial")
	}
	listener, err := net.Listen("tcp", host.listener.Addr().String())
	if err != nil {
		t.Fatal("closed host retained its listener", err)
	}
	_ = listener.Close()
}

func TestBrowserFixtureHostServeFailureInvalidatesEvenSealedWitnesses(t *testing.T) {
	for _, scenario := range []string{"denied", "read", "artifact"} {
		t.Run(scenario, func(t *testing.T) {
			host, err := newBrowserFixtureHost("127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.close() })
			fixture, err := host.newTrial(uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			events := []Event{{ID: "event-1", Kind: "final", Content: "Reference: " + fixture.code}}
			var artifact []byte
			switch scenario {
			case "read":
				fixtureHostGet(t, host, fixture.path, http.StatusOK)
			case "artifact":
				artifact = fixtureHostGet(t, host, fixture.path+"report.csv", http.StatusOK)
			}
			fixture.seal()
			item := browserOracleCase(t, scenario)
			before, err := inspectBrowserFixture(item, fixture, events, artifact)
			if err != nil || before.Effects != Pass || scenario == "read" && before.Read != Pass ||
				scenario == "artifact" && before.Artifact != Pass {
				t.Fatal("healthy witness control did not pass before listener failure", err)
			}
			if err = host.listener.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-host.done:
			case <-time.After(2 * time.Second):
				t.Fatal("unexpected server failure was not observed")
			}
			if !fixture.snapshot().failed || !errors.Is(host.close(), ErrBrowserFixture) {
				t.Fatal("failed listener retained healthy fixture/cleanup evidence")
			}
			result, err := inspectBrowserFixture(item, fixture, events, artifact)
			if err != nil || result.Effects != Inconclusive || result.Read == Pass || result.Artifact == Pass ||
				result.ArtifactSHA256 != "" {
				t.Fatal("failed host retained a passing predicate or artifact digest", err)
			}
			events[0].Content = "Wrong reference"
			if scenario == "artifact" {
				artifact = append(artifact, '\n')
			}
			wrong, err := inspectBrowserFixture(item, fixture, events, artifact)
			if err != nil || scenario == "read" && wrong.Read != Fail ||
				scenario == "artifact" && wrong.Artifact != Fail {
				t.Fatal("host failure erased an observed mismatch", err)
			}
		})
	}
}
