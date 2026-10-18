package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
)

// Redirect only the fixed production API to a local synthetic HTTP peer. No
// runtime URL override or real GitHub/provider/vault credential is involved.
type credentialGitHubFixtureTransport struct{ api *httptest.Server }

func (transport credentialGitHubFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" {
		return nil, errCredentialsUnavailable
	}
	endpoint, err := url.Parse(transport.api.URL)
	if err != nil {
		return nil, errCredentialsUnavailable
	}
	local := request.Clone(request.Context())
	copyURL := *request.URL
	local.URL = &copyURL
	local.URL.Scheme, local.URL.Host = endpoint.Scheme, endpoint.Host
	return transport.api.Client().Transport.RoundTrip(local)
}
