package gateway

import (
	"context"
	"errors"
	"slices"

	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func (source *gatewayBrowserToolSource) loginDiscoveredCredential(ctx context.Context,
	selection browser.CredentialLoginRequest, session browser.Session, principal credentialbroker.Principal,
) browser.CredentialLoginResult {
	denied := browser.CredentialLoginResult{Outcome: "denied"}
	runtime := source.services.Credentials
	if runtime.discoveryAlias == "" || runtime.findLogins == nil || runtime.consumeWebsiteLogin == nil ||
		(selection.Account != "" && selection.Account != runtime.discoveryAlias) {
		return denied
	}
	step := selection.Fields.Step
	if !slices.Contains([]string{"discover", "credentials", "username", "password", "totp", "verify"}, step) {
		return denied
	}
	scope, vaultID, err := runtime.controller.DiscoveryRequest(ctx, principal,
		runtime.discoveryAlias, session.SnapshotOrigin)
	if err != nil {
		return denied
	}
	candidates, err := runtime.findLogins(ctx, vaultID, vault.DiscoveryQuery{
		Website: session.SnapshotOrigin, Name: selection.LoginName,
	})
	if err != nil || len(candidates) > 20 {
		return browser.CredentialLoginResult{Alias: scope.Account, Outcome: "unavailable"}
	}
	result := browser.CredentialLoginResult{Alias: scope.Account, Outcome: "not_found"}
	for _, candidate := range candidates {
		if candidate.VaultID != vaultID {
			return denied
		}
		result.Candidates = append(result.Candidates, browser.CredentialCandidate{
			ID: candidate.ItemID, Name: candidate.Name, Domains: slices.Clone(candidate.Domains),
		})
	}
	if len(candidates) == 0 {
		return result
	}
	if step == "discover" {
		result.Outcome = "found"
		if len(candidates) == 1 {
			result.CredentialID = candidates[0].ItemID
		}
		return result
	}
	itemID := selection.CredentialID
	if itemID == "" {
		if len(candidates) != 1 {
			result.Outcome = "choice_required"
			return result
		}
		itemID = candidates[0].ItemID
	}
	if !slices.ContainsFunc(candidates, func(candidate vault.Candidate) bool { return candidate.ItemID == itemID }) {
		return browser.CredentialLoginResult{Alias: scope.Account, Outcome: "not_found"}
	}
	lease, err := runtime.requestCredentialLease(ctx, scope)
	if errors.Is(err, errCredentialApprovalRequired) {
		return browser.CredentialLoginResult{Alias: scope.Account, CredentialID: itemID, Outcome: "approval_required"}
	}
	if err != nil {
		return denied
	}
	lease, err = lease.BindDiscovered(ctx, scope, itemID)
	if err != nil {
		return denied
	}
	codeUnavailable := false
	consume := func(useCtx context.Context, use func([]byte, []byte) error) error {
		return consumeCredentialLogin(useCtx, scope, lease,
			func(readCtx context.Context, binding vault.Binding, callback func([]byte, []byte) error) error {
				readErr := runtime.consumeWebsiteLogin(readCtx, binding, session.SnapshotOrigin, step, callback)
				codeUnavailable = step == "totp" && errors.Is(readErr, vault.ErrTOTPUnavailable)
				return readErr
			}, use)
	}
	selection.Account, selection.CredentialID = scope.Account, itemID
	selection.Principal, selection.PolicyRevision = principal, scope.PolicyRevision
	selection.Fields = browser.CredentialFields{
		Step: step, UsernameRef: selection.Fields.UsernameRef, PasswordRef: selection.Fields.PasswordRef,
		OTPRef: selection.Fields.OTPRef, SubmitRef: selection.Fields.SubmitRef,
		AuthenticatedRef: selection.Fields.AuthenticatedRef,
	}
	completed, err := withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.CredentialLoginResult, error) {
			return broker.LoginCredential(ctx, selection, lease, consume), nil
		})
	if err != nil {
		return denied
	}
	completed.Alias, completed.CredentialID = scope.Account, itemID
	if codeUnavailable && completed.Outcome == "denied" && lease.Check(ctx, scope) == nil {
		completed.Outcome = "handoff_required"
	}
	return completed
}
