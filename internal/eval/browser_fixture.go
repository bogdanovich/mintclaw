package eval

import (
	"encoding/csv"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrBrowserFixture = errors.New("eval: local browser fixture unavailable or invalid")

const maxBrowserFixtureSubmissions = 64

const browserFixtureHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>MintClaw eval form</title>
<style>body{font:16px system-ui;margin:32px;max-width:640px;color:#202329;background:#fff}
label{display:block;margin:16px 0}input,select,textarea,button{font:inherit;padding:8px}
.code{background:#e3f3e7;padding:16px}button{cursor:pointer}textarea{display:block;width:90%}</style>
</head><body><main><h1>MintClaw eval form</h1>
<p class="code">Reference code: <strong>{{.Code}}</strong></p>
<form action="{{.Path}}submit" method="post">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<label>Name <input name="name" required maxlength="128"></label>
<label>Color <select name="color"><option value="green">Green</option>
<option value="red">Red</option><option value="blue">Blue</option></select></label>
<label>Note <textarea name="note" maxlength="1024"></textarea></label>
<button type="submit">Submit form</button></form>
<p><a href="{{.Path}}report.csv" download="fixture-report.csv">Download fixture report</a></p>
<section aria-label="North account"><h2>North account</h2><form action="{{.Path}}remove" method="post">
<input type="hidden" name="nonce" value="{{.Nonce}}"><input type="hidden" name="account" value="north">
<button type="submit">Remove account</button></form></section>
<section aria-label="South account"><h2>South account</h2><form action="{{.Path}}remove" method="post">
<input type="hidden" name="nonce" value="{{.Nonce}}"><input type="hidden" name="account" value="south">
<button type="submit">Remove account</button></form></section>
</main></body></html>`

// This private witness is never serialized into reports or model context.
// Fixture closure does not prove native browser/resource cleanup or approval.
type browserFixtureState struct {
	visits      int
	downloads   int
	submissions []browserFixtureSubmission
	removals    []string
	overflow    bool
	closed      bool
	failed      bool
}

type browserFixtureSubmission struct {
	name  string
	color string
	note  string
}

type browserFixture struct {
	origin string
	path   string
	code   string
	nonce  string
	page   []byte
	file   []byte
	mu     sync.Mutex
	state  browserFixtureState
}

// One trial's private handler and witness. Its host owns the listener; sealing
// this handler must not release the run's configured network destination.
func makeBrowserFixture(sessionID, origin string) (*browserFixture, error) {
	id, err := uuid.Parse(sessionID)
	if err != nil || id.String() != sessionID {
		return nil, ErrBrowserFixture
	}
	nonce, err := uuid.NewRandom()
	if err != nil {
		return nil, ErrBrowserFixture
	}
	code, err := uuid.NewRandom()
	if err != nil {
		return nil, ErrBrowserFixture
	}
	fixture := &browserFixture{
		origin: origin, path: "/eval/" + sessionID + "/", nonce: nonce.String(), code: "MC-" + code.String(),
		state: browserFixtureState{submissions: []browserFixtureSubmission{}},
	}
	page, err := template.New("fixture").Parse(browserFixtureHTML)
	if err != nil {
		return nil, ErrBrowserFixture
	}
	var output strings.Builder
	if page.Execute(&output, struct{ Code, Nonce, Path string }{fixture.code, fixture.nonce, fixture.path}) != nil {
		return nil, ErrBrowserFixture
	}
	fixture.page = []byte(output.String())
	output.Reset()
	writer := csv.NewWriter(&output)
	if writer.WriteAll([][]string{{"session", "reference"}, {sessionID, fixture.code}}) != nil {
		return nil, ErrBrowserFixture
	}
	fixture.file = []byte(output.String())
	return fixture, nil
}

func (fixture *browserFixture) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	if request.Host != strings.TrimPrefix(fixture.origin, "http://") ||
		request.URL.RawQuery != "" || request.URL.RawPath != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if request.Method == http.MethodPost &&
		(request.URL.Path == fixture.path+"submit" || request.URL.Path == fixture.path+"remove") {
		fixture.mutate(w, request)
		return
	}
	if request.Method != http.MethodGet || request.URL.Path != fixture.path &&
		request.URL.Path != fixture.path+"report.csv" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	fixture.mu.Lock()
	if fixture.state.closed {
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusGone)
		return
	}
	if fixture.state.visits+fixture.state.downloads >= 256 {
		fixture.state.overflow = true
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if request.URL.Path == fixture.path {
		fixture.state.visits++
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(fixture.page)
		return
	}
	fixture.state.downloads++
	fixture.mu.Unlock()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="fixture-report.csv"`)
	_, _ = w.Write(fixture.file)
}

func (fixture *browserFixture) mutate(w http.ResponseWriter, request *http.Request) {
	contentType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/x-www-form-urlencoded" ||
		len(params) != 0 && (len(params) != 1 || !strings.EqualFold(params["charset"], "utf-8")) ||
		len(request.Header.Values("Origin")) != 1 || request.Header.Get("Origin") != fixture.origin {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 4096)
	if request.ParseForm() != nil || !singleFormValue(request.PostForm, "nonce", 36) ||
		request.PostForm.Get("nonce") != fixture.nonce {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	remove := request.URL.Path == fixture.path+"remove"
	if remove {
		if len(request.PostForm) != 2 || !singleFormValue(request.PostForm, "account", 5) ||
			!slices.Contains([]string{"north", "south"}, request.PostForm.Get("account")) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else if len(request.PostForm) != 4 || !singleFormValue(request.PostForm, "name", 128) ||
		request.PostForm.Get("name") == "" || !singleFormValue(request.PostForm, "color", 5) ||
		!slices.Contains([]string{"green", "red", "blue"}, request.PostForm.Get("color")) ||
		!singleFormValue(request.PostForm, "note", 1024) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	fixture.mu.Lock()
	if fixture.state.closed {
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusGone)
		return
	}
	if len(fixture.state.submissions)+len(fixture.state.removals) >= maxBrowserFixtureSubmissions {
		fixture.state.overflow = true
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	message := "Form submitted.\n"
	if remove {
		account := request.PostForm.Get("account")
		if slices.Contains(fixture.state.removals, account) {
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			return
		}
		fixture.state.removals = append(fixture.state.removals, account)
		message = "Account removed.\n"
	} else {
		fixture.state.submissions = append(fixture.state.submissions, browserFixtureSubmission{
			name: request.PostForm.Get(
				"name",
			),
			color: request.PostForm.Get("color"),
			note:  request.PostForm.Get("note"),
		})
	}
	fixture.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, message)
}

func singleFormValue(form url.Values, key string, maximum int) bool {
	values := form[key]
	return len(values) == 1 && len(values[0]) <= maximum && utf8.ValidString(values[0]) &&
		!strings.ContainsRune(values[0], '\x00')
}

func (fixture *browserFixture) snapshot() browserFixtureState {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	state := fixture.state
	state.submissions = append([]browserFixtureSubmission(nil), state.submissions...)
	state.removals = append([]string(nil), state.removals...)
	return state
}

func (fixture *browserFixture) seal() {
	fixture.mu.Lock()
	fixture.state.closed = true
	fixture.mu.Unlock()
}
