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
)

var (
	errCredentialDriver = errors.New("private browser login unavailable")
	credentialCapsuleID = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// CredentialFields is trusted runtime metadata, containing no login values.
// References come from the current browser snapshot; outcome evidence comes
// from immutable operator account configuration.
type CredentialFields struct {
	Step             string
	WebsiteDomain    string
	Origin           string
	UsernameRef      string
	PasswordRef      string
	SubmitRef        string
	SuccessURL       string
	SuccessMarkerID  string
	HandoffURL       string
	HandoffMarkerID  string
	OTPRef           string
	AuthenticatedRef string
}

type privateCredentialDriver interface {
	prepareCredential(context.Context, CredentialFields) (string, error)
	submitCredential(context.Context, string, []byte, []byte) (credentialDriverResult, error)
	discardCredential(context.Context, string) error
	bindCredentialOutputs(func([]string) error) error
}

func (client *playwrightLibraryClient) bindCredentialOutputs(protect func([]string) error) error {
	client.closeMu.Lock()
	defer client.closeMu.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	if protect == nil || client.credentialOutputs != nil || client.closed {
		return errCredentialDriver
	}
	client.credentialOutputs = protect
	return nil
}

// Cookie values travel in an envelope that stays inside the private parser.
// Register them before releasing any observation, error or catalog to its
// normal consumer. Binary outputs have no qualified credential projection.
func decodeCredentialCall(result *sdkmcp.CallToolResult, err error, protect func([]string) error) (
	*sdkmcp.CallToolResult, error,
) {
	text, ok := credentialDriverText(result, err)
	if !ok || protect == nil {
		return nil, ErrWorkerLost
	}
	var response struct {
		Result  playwrightLibraryWireResult `json:"result"`
		Cookies []string                    `json:"cookies"`
	}
	if decodeCredentialEnvelope(text, &response) != nil || response.Cookies == nil || protect(response.Cookies) != nil {
		return nil, ErrWorkerLost
	}
	for _, content := range response.Result.Content {
		if content.Type != "text" {
			return nil, ErrWorkerLost
		}
	}
	return decodePlaywrightLibraryResult(playwrightLibraryWireResponse{Result: &response.Result})
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

func (client *playwrightLibraryClient) prepareCredential(ctx context.Context, fields CredentialFields) (string, error) {
	result, err := client.call(ctx, "mintclaw_credential_prepare", map[string]any{
		"origin": fields.Origin, "username_ref": fields.UsernameRef, "password_ref": fields.PasswordRef,
		"submit_ref": fields.SubmitRef, "success_url": fields.SuccessURL, "success_marker_id": fields.SuccessMarkerID,
		"handoff_url": fields.HandoffURL, "handoff_marker_id": fields.HandoffMarkerID,
		"step": fields.Step, "otp_ref": fields.OTPRef, "authenticated_ref": fields.AuthenticatedRef,
		"website_domain": fields.WebsiteDomain,
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
	case "logged_in", "handoff_required", "denied", "failed", "outcome_unknown", "submitted", "unverified":
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
