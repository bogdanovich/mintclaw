//go:build linux || darwin

package gateway

import (
	"errors"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

// A scripted local provider chooses only public metadata and fresh browser refs.
// It never receives or computes the fixture password or one-time code.
type credentialWebsiteProviderScript struct {
	sessionID        string
	retiredSessionID string
	credentialID     string
	observation      browser.Observation
	login            browser.CredentialLoginResult
}

func (script *credentialWebsiteProviderScript) next(phase int, decode func(any) bool) (string, map[string]any, error) {
	failed := func() (string, map[string]any, error) {
		return "", nil, errors.New("provider website login transition unavailable")
	}
	observe := func() (string, map[string]any, error) {
		return "browser_observe", map[string]any{"browser_session_id": script.sessionID}, nil
	}
	switch phase {
	case 0:
		return "browser_session", map[string]any{
			"operation":            "open",
			"target":               config.BrowserDefaultTarget,
			"profile":              "ephemeral",
			"interaction_language": "en",
		}, nil
	case 1:
		var opened struct {
			ID string `json:"browser_session_id"`
		}
		if !decode(&opened) || opened.ID == "" || opened.ID == script.retiredSessionID {
			return failed()
		}
		script.sessionID = opened.ID
		return observe()
	case 2:
		if !decode(&script.observation) || script.observation.SnapshotID == "" {
			return failed()
		}
		return "browser_login", credentialFixtureStepArguments(script.observation, "discover"), nil
	case 3:
		var found browser.CredentialLoginResult
		if !decode(&found) || found.Outcome != "found" || len(found.Candidates) != 1 || found.CredentialID == "" {
			return failed()
		}
		script.credentialID = found.CredentialID
		args, err := credentialFixtureLoginArguments(script.observation)
		if err != nil {
			return failed()
		}
		delete(args, "account_alias")
		args["operation"], args["credential_id"] = "credentials", script.credentialID
		return "browser_login", args, nil
	case 4, 6:
		var submitted browser.CredentialLoginResult
		if !decode(&submitted) || submitted.Outcome != "submitted" || submitted.CredentialID != script.credentialID {
			return failed()
		}
		return observe()
	case 5, 7:
		var observation browser.Observation
		if !decode(&observation) || observation.SnapshotID == "" {
			return failed()
		}
		step := "totp"
		if phase == 7 {
			step = "verify"
		}
		args, err := credentialFixtureWebsiteArguments(observation, step)
		if err != nil {
			return failed()
		}
		args["credential_id"] = script.credentialID
		return "browser_login", args, nil
	case 8:
		if !decode(&script.login) || script.login.Outcome != "logged_in" ||
			script.login.CredentialID != script.credentialID {
			return failed()
		}
		return observe()
	case 9:
		var observation browser.Observation
		if !decode(&observation) || observation.SnapshotID == "" {
			return failed()
		}
		return "browser_session", map[string]any{"operation": "close", "browser_session_id": script.sessionID}, nil
	case 10:
		var closed struct {
			State browser.SessionState `json:"state"`
		}
		if !decode(&closed) || !closed.State.Terminal() {
			return failed()
		}
		return "", nil, nil
	default:
		return failed()
	}
}
