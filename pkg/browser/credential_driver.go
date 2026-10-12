package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

var (
	errCredentialDriver = errors.New("private browser login unavailable")
	credentialCapsuleID = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// CredentialFields is trusted runtime metadata, containing no login values.
// References come from the current browser snapshot; outcome evidence comes
// from immutable operator account configuration.
type CredentialFields struct {
	Origin          string
	UsernameRef     string
	PasswordRef     string
	SubmitRef       string
	SuccessURL      string
	SuccessMarkerID string
	HandoffURL      string
	HandoffMarkerID string
}

type privateCredentialDriver interface {
	prepareCredential(context.Context, CredentialFields) (string, error)
	submitCredential(context.Context, string, []byte, []byte) (credentialDriverResult, error)
	discardCredential(context.Context, string) error
}

type credentialDriverResult struct {
	code    string
	cookies []string
}

func (credentialDriverResult) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private browser credential result]")
}

func (credentialDriverResult) MarshalJSON() ([]byte, error) {
	return []byte(`"[private browser credential result]"`), nil
}

func (result credentialDriverResult) protectCookies() (func(), error) {
	var releases []func()
	release := func() {
		for _, end := range releases {
			end()
		}
	}
	for _, cookie := range result.cookies {
		end, err := secretvalues.Register([]byte(cookie))
		if err != nil {
			release()
			return nil, errCredentialDriver
		}
		releases = append(releases, end)
	}
	return release, nil
}

func (client *playwrightLibraryClient) prepareCredential(ctx context.Context, fields CredentialFields) (string, error) {
	result, err := client.call(ctx, "mintclaw_credential_prepare", map[string]any{
		"origin": fields.Origin, "username_ref": fields.UsernameRef, "password_ref": fields.PasswordRef,
		"submit_ref": fields.SubmitRef, "success_url": fields.SuccessURL, "success_marker_id": fields.SuccessMarkerID,
		"handoff_url": fields.HandoffURL, "handoff_marker_id": fields.HandoffMarkerID,
	})
	text, ok := credentialDriverText(result, err)
	if !ok {
		return "", errCredentialDriver
	}
	var ready struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
	}
	if decodeCredentialEnvelope(text, &ready) != nil || !credentialCapsuleID.MatchString(ready.ID) ||
		ready.Origin != fields.Origin {
		return "", errCredentialDriver
	}
	return ready.ID, nil
}

func (client *playwrightLibraryClient) submitCredential(
	ctx context.Context,
	id string,
	username, password []byte,
) (credentialDriverResult, error) {
	if !credentialCapsuleID.MatchString(id) {
		return credentialDriverResult{}, errCredentialDriver
	}
	params := map[string]any{"id": id, "username": string(username), "password": string(password)}
	defer clear(params)
	result, err := client.call(ctx, "mintclaw_credential_submit", params)
	text, ok := credentialDriverText(result, err)
	if !ok {
		return credentialDriverResult{}, errCredentialDriver
	}
	var response struct {
		Code    string   `json:"code"`
		Cookies []string `json:"cookies"`
	}
	if decodeCredentialEnvelope(text, &response) != nil || len(response.Cookies) > 128 {
		return credentialDriverResult{}, errCredentialDriver
	}
	switch response.Code {
	case "logged_in", "handoff_required", "denied", "failed", "outcome_unknown":
	default:
		return credentialDriverResult{}, errCredentialDriver
	}
	for _, cookie := range response.Cookies {
		if len(cookie) == 0 || len(cookie) > 4096 {
			return credentialDriverResult{}, errCredentialDriver
		}
	}
	return credentialDriverResult{code: response.Code, cookies: response.Cookies}, nil
}

func (client *playwrightLibraryClient) discardCredential(ctx context.Context, id string) error {
	result, err := client.call(ctx, "mintclaw_credential_discard", map[string]any{"id": id})
	text, ok := credentialDriverText(result, err)
	if !ok || text != "discarded" {
		return errCredentialDriver
	}
	return nil
}

func credentialDriverText(result *sdkmcp.CallToolResult, err error) (string, bool) {
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		return "", false
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || text == nil || len(text.Text) > 600*1024 {
		return "", false
	}
	return text.Text, true
}

func decodeCredentialEnvelope(text string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errCredentialDriver
	}
	return nil
}
