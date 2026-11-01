package tools

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

func browserLoginString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func projectBrowserLoginStep(args map[string]any) (map[string]any, error) {
	step, _ := args["operation"].(string)
	if _, explicit := args["operation"]; !explicit {
		step = "credentials"
	}
	refs := map[string][]string{
		"discover": {}, "authorize": {}, "credentials": {"username_ref", "password_ref", "submit_ref"},
		"username": {"username_ref", "submit_ref"}, "password": {"password_ref", "submit_ref"},
		"totp": {"otp_ref", "submit_ref"}, "verify": {"authenticated_ref"},
	}
	selected, valid := refs[step]
	if !valid {
		return nil, browser.ErrInvalid
	}
	required := append([]string{"browser_session_id", "tab_id", "snapshot_id"}, selected...)
	allowed := map[string]bool{
		"operation": true, "snapshot_generation": true,
		"account_alias": true, "credential_id": step != "discover", "login_name": true,
	}
	for _, key := range required {
		allowed[key] = true
	}
	result := map[string]any{"operation": step}
	for key, raw := range args {
		if !allowed[key] {
			return nil, browser.ErrInvalid
		}
		if key == "snapshot_generation" || key == "operation" {
			continue
		}
		value, ok := raw.(string)
		maximum := 128
		if key == "login_name" {
			maximum = 256
		}
		if !ok || len(value) == 0 || len(value) > maximum || strings.TrimSpace(value) != value ||
			!utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, browser.ErrInvalid
		}
		if (key == "account_alias" || key == "credential_id") && !intent.ValidAlias(value) {
			return nil, browser.ErrInvalid
		}
		result[key] = value
	}
	for _, key := range required {
		if result[key] == nil {
			return nil, browser.ErrInvalid
		}
	}
	generation, ok := browserInteger(args["snapshot_generation"])
	if !ok || generation < 1 {
		return nil, browser.ErrInvalid
	}
	result["snapshot_generation"] = generation
	return result, nil
}

func validBrowserLoginProjection(result browser.CredentialLoginResult) bool {
	if (result.Alias != "" && !intent.ValidAlias(result.Alias)) ||
		(result.CredentialID != "" && !intent.ValidAlias(result.CredentialID)) || len(result.Candidates) > 20 {
		return false
	}
	for _, candidate := range result.Candidates {
		if !intent.ValidAlias(candidate.ID) || len(candidate.Name) > 1024 || !utf8.ValidString(candidate.Name) ||
			strings.ContainsFunc(candidate.Name, unicode.IsControl) || len(candidate.Domains) > 64 {
			return false
		}
		for _, domain := range candidate.Domains {
			if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, "/:@?#\\\x00\r\n ") {
				return false
			}
		}
	}
	return true
}
