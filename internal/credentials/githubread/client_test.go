package githubread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (roundTrip testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGitHubReadUsesFixedEndpointAndProtectsProjectionAcrossActualHTTP(t *testing.T) {
	const token = "synthetic-github-read-token-V54"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/repo/pulls" ||
			r.URL.RawQuery != "state=open&per_page=20" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("private GitHub request changed its fixed scope")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number": 7, "state": "open", "title": "Update " + token,
			"body": token, "html_url": "https://untrusted.invalid/" + token, "draft": true,
		}})
	}))
	defer server.Close()
	client := New(testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatal("connector changed its public API destination")
		}
		local := r.Clone(r.Context())
		copyURL := *r.URL
		local.URL = &copyURL
		local.URL.Scheme = "http"
		local.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return server.Client().Transport.RoundTrip(local)
	}))
	result, err := client.ListPullRequests(t.Context(), "Owner/Repo", []byte(token))
	encoded, encodeErr := json.Marshal(result)
	if err != nil || encodeErr != nil || calls != 1 || len(result) != 1 || strings.Contains(string(encoded), token) ||
		result[0].URL != "https://github.com/owner/repo/pull/7" || !result[0].Draft {
		t.Fatal("GitHub request failed or private response escaped projection")
	}
}

func TestGitHubReadNeverFollowsRedirectsOrReturnsPrivateFailures(t *testing.T) {
	const token = "synthetic-github-failure-token-N36"
	for _, mode := range []string{"redirect", "http-error", "transport-error", "null", "malformed", "oversized", "closed"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			client := New(testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Request: r}
				response.Header.Set("Content-Type", "application/json")
				body := "null"
				switch mode {
				case "redirect":
					response.StatusCode = 302
					response.Header.Set("Location", "https://other.invalid/"+token)
				case "http-error":
					response.StatusCode = 401
					body = token
				case "transport-error":
					return nil, errors.New(token)
				case "malformed":
					body = token
				case "oversized":
					body = strings.Repeat("x", 256*1024+1)
				case "closed":
					body = `[{"number":1,"state":"closed","title":"ignored"}]`
				}
				response.Body = io.NopCloser(strings.NewReader(body))
				return response, nil
			}))
			result, err := client.ListPullRequests(t.Context(), "owner/repo", []byte(token))
			if result != nil || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() || calls != 1 {
				t.Fatal("private HTTP failure escaped or triggered another request")
			}
		})
	}
	called := false
	client := New(
		testTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, context.Canceled }),
	)
	if _, err := client.ListPullRequests(t.Context(), "owner/repo?x=1", []byte(token)); err == nil || called {
		t.Fatal("invalid resource reached HTTP")
	}
}
