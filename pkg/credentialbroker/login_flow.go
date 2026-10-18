package credentialbroker

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	htmlIDPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)
	loginPathPattern = regexp.MustCompile(`^[A-Za-z0-9/_~.-]*$`)
)

// LoginFlow is protected operator configuration for one top-frame, single-page
// login. It contains no password, page content, or executable selector/code.
// Every field contributes to the account's canonical approval revision.
type LoginFlow struct {
	NavigationURL   string `json:"navigation_url"`
	EntryOrigin     string `json:"entry_origin"`
	SubmitURL       string `json:"submit_url"`
	SuccessURL      string `json:"success_url"`
	SuccessMarkerID string `json:"success_marker_id"`
	HandoffURL      string `json:"handoff_url,omitempty"`
	HandoffMarkerID string `json:"handoff_marker_id,omitempty"`
}

// NormalizeLoginFlow validates complete bounded flow configuration. Consumers
// must additionally establish live document/element authority and a current
// grant. Normalized URLs have no query, fragment, user info or escaped path.
func NormalizeLoginFlow(flow LoginFlow, origins []string) (LoginFlow, error) {
	entry, err := credentialOrigin(flow.EntryOrigin)
	if err != nil || len(origins) == 0 || len(origins) > MaxOriginsPerAccount || !slices.Contains(origins, entry) ||
		!htmlIDPattern.MatchString(flow.SuccessMarkerID) ||
		(flow.HandoffURL == "") != (flow.HandoffMarkerID == "") ||
		(flow.HandoffMarkerID != "" && !htmlIDPattern.MatchString(flow.HandoffMarkerID)) {
		return LoginFlow{}, ErrInvalidPolicy
	}
	flow.EntryOrigin = entry
	for _, target := range []*string{&flow.NavigationURL, &flow.SubmitURL, &flow.SuccessURL, &flow.HandoffURL} {
		if target == &flow.HandoffURL && *target == "" {
			continue
		}
		canonical, origin, urlErr := loginURL(*target)
		if urlErr != nil || !slices.Contains(origins, origin) ||
			((target == &flow.NavigationURL || target == &flow.SubmitURL) && origin != entry) {
			return LoginFlow{}, ErrInvalidPolicy
		}
		*target = canonical
	}
	if flow.NavigationURL == flow.SuccessURL || flow.SuccessURL == flow.HandoffURL ||
		flow.NavigationURL == flow.HandoffURL {
		return LoginFlow{}, ErrInvalidPolicy
	}
	return flow, nil
}

func loginURL(raw string) (string, string, error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return "", "", ErrInvalidPolicy
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.ContainsAny(raw, "#?\\%") || parsed.RawPath != "" || !loginPathPattern.MatchString(parsed.Path) {
		return "", "", ErrInvalidPolicy
	}
	for _, component := range strings.Split(parsed.Path, "/") {
		if component == "." || component == ".." {
			return "", "", ErrInvalidPolicy
		}
	}
	origin, err := credentialOrigin((&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String())
	if err != nil {
		return "", "", ErrInvalidPolicy
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	return origin + path, origin, nil
}

// LoginFlow returns a value copy only for a permitted current request at the
// configured credential-entry origin. It authorizes no retrieval or browser IO.
// Legacy rules without an explicit flow remain ineligible for browser login.
func (policy *Policy) LoginFlow(request Request) (LoginFlow, error) {
	if !policy.permits(request) {
		return LoginFlow{}, ErrDenied
	}
	flow := policy.accounts[request.Account].login
	origin, err := credentialOrigin(request.Origin)
	if flow == nil || err != nil || origin != flow.EntryOrigin {
		return LoginFlow{}, ErrDenied
	}
	return *flow, nil
}
