package eval

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newBrowserFixtureForTest(t *testing.T) *browserFixture {
	t.Helper()
	fixture, err := newBrowserFixture(uuid.NewString(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func browserFixtureForm(fixture *browserFixture) url.Values {
	return url.Values{
		"nonce": {fixture.nonce}, "name": {"synthetic-private-name"},
		"color": {"blue"}, "note": {"synthetic-private-note"},
	}
}

func browserFixtureRequest(fixture *browserFixture, suffix string, values url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, fixture.origin+fixture.path+suffix,
		strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", fixture.origin)
	return request
}

func TestBrowserFixtureRejectsUnsafeBindAndOccupiedPort(t *testing.T) {
	for _, address := range []string{
		"", ":0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0", "localhost:0", "example.test:0",
		"127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:0,0.0.0.0:0",
	} {
		fixture, err := newBrowserFixture(uuid.NewString(), address)
		if fixture != nil || !errors.Is(err, ErrBrowserFixture) {
			t.Fatal("unsafe or ambiguous listener admitted")
		}
	}
	for _, identity := range []string{"", "../private", strings.ToUpper(uuid.NewString()), uuid.NewString() + "/"} {
		fixture, err := newBrowserFixture(identity, "127.0.0.1:0")
		if fixture != nil || !errors.Is(err, ErrBrowserFixture) {
			t.Fatal("noncanonical trial identity admitted")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	fixture, err := newBrowserFixture(uuid.NewString(), listener.Addr().String())
	if fixture != nil || !errors.Is(err, ErrBrowserFixture) {
		t.Fatal("fixture attached to or replaced an occupied listener")
	}
}

func TestBrowserFixtureHTTPPageAndCSVHaveFreshIndependentWitnesses(t *testing.T) {
	fixture := newBrowserFixtureForTest(t)
	other := newBrowserFixtureForTest(t)
	if fixture.code == other.code || fixture.nonce == other.nonce || fixture.origin == other.origin ||
		fixture.path == other.path {
		t.Fatal("trial fixture authority or evidence reused")
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for _, suffix := range []string{"", "report.csv"} {
		response, err := client.Get(fixture.origin + fixture.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
		_ = response.Body.Close()
		if readErr != nil || len(body) > 16384 || response.StatusCode != http.StatusOK ||
			response.Header.Get("Cache-Control") != "no-store" ||
			response.Header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(response.Header.Get("Content-Security-Policy"), "form-action 'self'") {
			t.Fatal("fixture response unavailable or incorrectly bounded")
		}
		if suffix == "" {
			if !bytes.Contains(body, []byte(fixture.code)) || !bytes.Contains(body, []byte(fixture.nonce)) ||
				bytes.Count(body, []byte(">Remove account<")) != 2 ||
				bytes.Contains(body, []byte("synthetic-private-name")) {
				t.Fatal("fixture page missing controls or prefilled with private facts")
			}
		} else {
			rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
			if err != nil || len(rows) != 2 || len(rows[1]) != 2 || rows[1][1] != fixture.code ||
				fixture.path != "/eval/"+rows[1][0]+"/" || !bytes.Equal(body, fixture.file) ||
				response.Header.Get("Content-Type") != "text/csv" {
				t.Fatal("download did not match the independent immutable fixture")
			}
		}
	}
	state := fixture.snapshot()
	if state.visits != 1 || state.downloads != 1 || len(state.submissions) != 0 ||
		len(state.removals) != 0 || state.failed || state.closed || state.overflow {
		t.Fatal("read-only fixture access invented mutations or lost witnesses")
	}
	request := browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture))
	request.RequestURI = ""
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || len(fixture.snapshot().submissions) != 1 {
		t.Fatal("real HTTP form submission did not reach the independent witness")
	}
}

func TestBrowserFixtureRecordsActualMutationsWithoutEchoingPrivateInput(t *testing.T) {
	fixture := newBrowserFixtureForTest(t)
	values := browserFixtureForm(fixture)
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", values))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), values.Get("name")) ||
		strings.Contains(response.Body.String(), values.Get("note")) {
		t.Fatal("submission failed or echoed private inputs")
	}
	remove := url.Values{"nonce": {fixture.nonce}, "account": {"south"}}
	response = httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "remove", remove))
	if response.Code != http.StatusOK {
		t.Fatal("synthetic removal failed")
	}
	state := fixture.snapshot()
	if len(state.submissions) != 1 || state.submissions[0] != (browserFixtureSubmission{
		name: values.Get("name"), color: "blue", note: values.Get("note"),
	}) || len(state.removals) != 1 || state.removals[0] != "south" {
		t.Fatal("server witness disagrees with the actual submitted values or selected account")
	}
	state.submissions[0].name = "replaced"
	state.removals[0] = "north"
	current := fixture.snapshot()
	if current.submissions[0].name != values.Get("name") || current.removals[0] != "south" {
		t.Fatal("snapshot exposed mutable oracle state")
	}
	stateType := reflect.TypeOf(current)
	for index := range stateType.NumField() {
		if stateType.Field(index).IsExported() {
			t.Fatal("private fixture state exposed report or model fields")
		}
	}
	response = httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "remove", remove))
	if response.Code != http.StatusConflict || len(fixture.snapshot().removals) != 1 {
		t.Fatal("repeated account removal changed state twice")
	}
}

func TestBrowserFixtureRejectsForeignMalformedOrOversizedMutation(t *testing.T) {
	fixture := newBrowserFixtureForTest(t)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Host = "foreign.test" },
		func(r *http.Request) { r.URL.RawQuery = "private=sentinel" },
		func(r *http.Request) { r.URL.RawPath = "/eval/%2fsubmit" },
		func(r *http.Request) { r.URL.Path = fixture.path + "private-state" },
		func(r *http.Request) { r.Method = http.MethodPut },
		func(r *http.Request) { r.Header.Del("Origin") },
		func(r *http.Request) { r.Header.Set("Origin", "http://foreign.test") },
		func(r *http.Request) { r.Header.Add("Origin", fixture.origin) },
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded; x=private") },
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=latin1")
		},
	} {
		request := browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture))
		mutate(request)
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, request)
		if response.Code < 400 || response.Body.Len() != 0 {
			t.Fatal("foreign or malformed request accepted or echoed")
		}
	}
	for _, mutate := range []func(url.Values){
		func(v url.Values) { v.Set("nonce", uuid.NewString()) },
		func(v url.Values) { v.Add("nonce", v.Get("nonce")) },
		func(v url.Values) { v.Del("note") },
		func(v url.Values) { v.Set("extra", "private") },
		func(v url.Values) { v.Set("name", "") },
		func(v url.Values) { v.Add("name", "another-private-name") },
		func(v url.Values) { v.Set("name", strings.Repeat("n", 129)) },
		func(v url.Values) { v.Set("name", "nul\x00value") },
		func(v url.Values) { v.Set("name", string([]byte{0xff})) },
		func(v url.Values) { v.Set("color", "other") },
		func(v url.Values) { v.Set("note", strings.Repeat("n", 1025)) },
		func(v url.Values) { v.Set("note", strings.Repeat("n", 4097)) },
	} {
		values := browserFixtureForm(fixture)
		mutate(values)
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", values))
		if response.Code != http.StatusBadRequest || response.Body.Len() != 0 {
			t.Fatal("invalid or oversized form accepted or echoed")
		}
	}
	for _, account := range []string{"", "other", "north\x00"} {
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, browserFixtureRequest(fixture, "remove",
			url.Values{"nonce": {fixture.nonce}, "account": {account}}))
		if response.Code != http.StatusBadRequest {
			t.Fatal("unknown removal target admitted")
		}
	}
	state := fixture.snapshot()
	if len(state.submissions) != 0 || len(state.removals) != 0 || state.overflow {
		t.Fatal("rejected requests had side effects")
	}
}

func TestBrowserFixtureWitnessBoundsAndConcurrentClosure(t *testing.T) {
	fixture := newBrowserFixtureForTest(t)
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			response := httptest.NewRecorder()
			fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture)))
		})
	}
	workers.Wait()
	if len(fixture.snapshot().submissions) != 20 {
		t.Fatal("concurrent mutations lost server witnesses")
	}
	for range maxBrowserFixtureSubmissions - 20 {
		fixture.ServeHTTP(httptest.NewRecorder(),
			browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture)))
	}
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture)))
	if response.Code != http.StatusTooManyRequests || !fixture.snapshot().overflow ||
		len(fixture.snapshot().submissions) != maxBrowserFixtureSubmissions {
		t.Fatal("mutation witness overflow was unbounded or unreported")
	}
	for range 256 {
		request := httptest.NewRequest(http.MethodGet, fixture.origin+fixture.path, nil)
		fixture.ServeHTTP(httptest.NewRecorder(), request)
	}
	response = httptest.NewRecorder()
	fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fixture.origin+fixture.path, nil))
	if response.Code != http.StatusTooManyRequests || fixture.snapshot().visits != 256 {
		t.Fatal("read witness overflow was unbounded or unreported")
	}
	closed := make(chan error, 4)
	for range 4 {
		workers.Go(func() { closed <- fixture.close() })
	}
	workers.Wait()
	close(closed)
	for err := range closed {
		if err != nil {
			t.Fatal("concurrent fixture shutdown failed")
		}
	}
	if fixture.close() != nil || !fixture.snapshot().closed || fixture.snapshot().failed {
		t.Fatal("fixture shutdown was not settled and idempotent")
	}
	response = httptest.NewRecorder()
	fixture.ServeHTTP(response, browserFixtureRequest(fixture, "submit", browserFixtureForm(fixture)))
	if response.Code != http.StatusGone || len(fixture.snapshot().submissions) != maxBrowserFixtureSubmissions {
		t.Fatal("closed fixture admitted a late mutation")
	}
	listener, err := net.Listen("tcp", strings.TrimPrefix(fixture.origin, "http://"))
	if err != nil {
		t.Fatal("fixture retained its owned listener after closure")
	}
	_ = listener.Close()
}
