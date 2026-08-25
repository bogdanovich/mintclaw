package providers

import (
	"context"

	oauthprovider "github.com/bogdanovich/mintclaw/pkg/providers/oauth"
)

type (
	AntigravityProvider  = oauthprovider.AntigravityProvider
	AntigravityModelInfo = oauthprovider.AntigravityModelInfo
	ClaudeProvider       = oauthprovider.ClaudeProvider
	CodexProvider        = oauthprovider.CodexProvider
)

const (
	CodexDefaultModel            = oauthprovider.CodexDefaultModel
	CodexDefaultContextWindow    = oauthprovider.CodexDefaultContextWindow
	CodexDefaultMaxContextWindow = oauthprovider.CodexDefaultMaxContextWindow
	CodexModelsClientVersion     = oauthprovider.CodexModelsClientVersion
)

type CodexModelInfo = oauthprovider.CodexModelInfo

func NewAntigravityProvider() *AntigravityProvider {
	return oauthprovider.NewAntigravityProvider()
}

func NewClaudeProvider(token string) *ClaudeProvider {
	return oauthprovider.NewClaudeProvider(token)
}

func NewClaudeProviderWithBaseURL(token, apiBase string) *ClaudeProvider {
	return oauthprovider.NewClaudeProviderWithBaseURL(token, apiBase)
}

func NewClaudeProviderWithTokenSource(token string, tokenSource func() (string, error)) *ClaudeProvider {
	return oauthprovider.NewClaudeProviderWithTokenSource(token, tokenSource)
}

func NewClaudeProviderWithTokenSourceAndBaseURL(
	token string, tokenSource func() (string, error), apiBase string,
) *ClaudeProvider {
	return oauthprovider.NewClaudeProviderWithTokenSourceAndBaseURL(token, tokenSource, apiBase)
}

func NewCodexProvider(token, accountID string) *CodexProvider {
	return oauthprovider.NewCodexProvider(token, accountID)
}

func NewCodexProviderWithTokenSource(
	token, accountID string, tokenSource func() (string, string, error),
) *CodexProvider {
	return oauthprovider.NewCodexProviderWithTokenSource(token, accountID, tokenSource)
}

func BundledCodexModel(model string) (CodexModelInfo, bool) {
	return oauthprovider.BundledCodexModel(model)
}

func DefaultCodexModelInfo() CodexModelInfo {
	return oauthprovider.DefaultCodexModelInfo()
}

func PreferredCodexModel(models []CodexModelInfo) CodexModelInfo {
	return oauthprovider.PreferredCodexModel(models)
}

func FetchCodexModels(
	ctx context.Context,
	token, accountID string,
) ([]CodexModelInfo, error) {
	return oauthprovider.FetchCodexModels(ctx, token, accountID)
}

// FetchAntigravityProjectID retrieves the Google Cloud project ID with a
// background context.
func FetchAntigravityProjectID(accessToken string) (string, error) {
	return oauthprovider.FetchAntigravityProjectID(accessToken)
}

// FetchAntigravityProjectIDWithContext retrieves the Google Cloud project ID,
// propagating ctx.
func FetchAntigravityProjectIDWithContext(ctx context.Context, accessToken string) (string, error) {
	return oauthprovider.FetchAntigravityProjectIDWithContext(ctx, accessToken)
}

// FetchAntigravityModels fetches available models with a background context.
func FetchAntigravityModels(accessToken, projectID string) ([]AntigravityModelInfo, error) {
	return oauthprovider.FetchAntigravityModels(accessToken, projectID)
}

// FetchAntigravityModelsWithContext fetches available models, propagating ctx.
func FetchAntigravityModelsWithContext(
	ctx context.Context,
	accessToken, projectID string,
) ([]AntigravityModelInfo, error) {
	return oauthprovider.FetchAntigravityModelsWithContext(ctx, accessToken, projectID)
}

func createClaudeTokenSource() func() (string, error) {
	return oauthprovider.CreateClaudeTokenSource(getCredential)
}

func createCodexTokenSource() func() (string, string, error) {
	return oauthprovider.CreateCodexTokenSource()
}
