package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/internal/credentials/githubread"
	"github.com/bogdanovich/mintclaw/internal/credentials/vault"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

const (
	credentialPagePath       = "/credentials/"
	credentialEnrollmentPath = credentialPagePath + "operator/enrollment"
)

var errCredentialsUnavailable = errors.New("credential approval unavailable")

type credentialRoutes interface {
	RegisterHTTPHandler(string, http.Handler) error
	UnregisterHTTPHandler(string)
}

// credentialRuntime is the gateway's sole approval owner. It creates neither
// a separate listener nor a model-facing secret retrieval API.
type credentialRuntime struct {
	controller          *approval.Controller
	routes              credentialRoutes
	once                sync.Once
	closeErr            error
	consumeLogin        func(context.Context, vault.Binding, func([]byte, []byte) error) error
	findLogins          func(context.Context, string, vault.DiscoveryQuery) ([]vault.Candidate, error)
	consumeWebsiteLogin func(context.Context, vault.Binding, string, string, func([]byte, []byte) error) error
	discoveryAlias      string
	consumeAPIKey       func(context.Context, vault.Binding, func([]byte) error) error
	loginEnabled        bool
	githubReadEnabled   bool
	githubReader        credentialGitHubReader
	ownerID             string
	hostID              string
	origin              string
	notify              credentialNotifier
}

func setupCredentialRuntime(
	cfg *config.Config,
	token string,
	routes credentialRoutes,
	notifiers ...credentialNotifier,
) (*credentialRuntime, error) {
	if cfg == nil || !cfg.Credentials.Enabled {
		return nil, nil
	}
	if token == "" || routes == nil || cfg.ValidateCredentials() != nil {
		return nil, errCredentialsUnavailable
	}
	controller, setup, err := approval.New(approval.Config{
		OwnerID: cfg.Credentials.OwnerID, Origin: cfg.Credentials.Origin,
		StateDirectory: cfg.Credentials.StateDirectory, Accounts: cfg.Credentials.Accounts,
		PagePath: credentialPagePath, TrustLoopbackTLSProxy: cfg.Credentials.TrustLoopbackTLSProxy,
	})
	if err != nil {
		return nil, errCredentialsUnavailable
	}
	runtime := &credentialRuntime{controller: controller, routes: routes}
	runtime.loginEnabled = cfg.Credentials.BrowserLoginEnabled
	for _, account := range cfg.Credentials.Accounts {
		if account.DiscoverLogins {
			runtime.discoveryAlias = account.Alias
		}
	}
	runtime.githubReadEnabled = cfg.Credentials.GitHubReadEnabled
	if runtime.githubReadEnabled {
		runtime.githubReader = githubread.New(nil)
	}
	runtime.ownerID, runtime.hostID, runtime.origin = cfg.Credentials.OwnerID, cfg.Credentials.HostID, cfg.Credentials.Origin
	if len(notifiers) == 1 {
		runtime.notify = notifiers[0]
	}
	// Approval setup remains available before the operator provisions a token.
	// An unavailable SDK never provides a login consumer or value access.
	if provider, vaultErr := newCredentialVault(cfg.Credentials.StateDirectory, vault.New); vaultErr == nil {
		runtime.consumeLogin = provider.UseLogin
		runtime.findLogins = provider.FindLogins
		runtime.consumeWebsiteLogin = provider.UseWebsiteLogin
		runtime.consumeAPIKey = provider.UseAPIKey
	}
	phone, err := approval.NewPhoneHandler(controller)
	if err != nil {
		_ = controller.Close()
		return nil, errCredentialsUnavailable
	}
	var enrollment *approval.Enrollment
	if setup != nil {
		enrollment, err = approval.NewEnrollment(setup)
		if err != nil {
			_ = controller.Close()
			return nil, errCredentialsUnavailable
		}
	}
	handler := &credentialHTTPHandler{phone: phone, enrollment: enrollment, token: token}
	if routes.RegisterHTTPHandler(credentialPagePath, handler) != nil {
		_ = controller.Close()
		return nil, errCredentialsUnavailable
	}
	return runtime, nil
}

func (runtime *credentialRuntime) Close(context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.once.Do(func() {
		runtime.routes.UnregisterHTTPHandler(credentialPagePath)
		if runtime.controller.Close() != nil {
			runtime.closeErr = errCredentialsUnavailable
		}
	})
	return runtime.closeErr
}

type credentialHTTPHandler struct {
	phone      *approval.PhoneHandler
	enrollment *approval.Enrollment
	token      string
}

func (handler *credentialHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case credentialEnrollmentPath:
		handler.issueEnrollment(writer, request)
	case credentialPagePath + "api/enroll/begin", credentialPagePath + "api/enroll/finish":
		if handler.enrollment == nil {
			writeCredentialHTTPError(writer, http.StatusForbidden)
			return
		}
		handler.enrollment.ServeHTTP(writer, request)
	default:
		handler.phone.ServeHTTP(writer, request)
	}
}

// Initial enrollment is local operator administration using the existing
// gateway PID-file token. The invite is returned only to the private setup
// command, never a model tool or notification. It cannot approve a grant.
func (handler *credentialHTTPHandler) issueEnrollment(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	ip := net.ParseIP(host)
	token, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	if err != nil || ip == nil || !ip.IsLoopback() || !ok || handler.token == "" ||
		len(request.Header.Values("Authorization")) != 1 || request.Header.Get("Origin") != "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(handler.token)) != 1 || request.URL.RawQuery != "" {
		writeCredentialHTTPError(writer, http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost || handler.enrollment == nil {
		writeCredentialHTTPError(writer, http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 32))
	if err != nil || strings.TrimSpace(string(body)) != "{}" {
		writeCredentialHTTPError(writer, http.StatusBadRequest)
		return
	}
	invite, err := handler.enrollment.Issue(request.Context())
	if err != nil {
		writeCredentialHTTPError(writer, http.StatusServiceUnavailable)
		return
	}
	defer clear(invite)
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write(invite)
}

func writeCredentialHTTPError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(`{"status":"unavailable"}`))
}
