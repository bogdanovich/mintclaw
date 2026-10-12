package approval

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed phone/index.html phone/app.js phone/app.css
var phoneAssets embed.FS

type phoneBoundary struct {
	origin, authority, hostname string
	pagePath                    string
	tlsProxy                    bool
	slots                       chan struct{}
}

// PhoneHandler never holds OwnerSetup or Enrollment. It can only begin/finish
// scope-bound assertions for approval or revocation. It creates no listener.
type PhoneHandler struct {
	boundary   phoneBoundary
	controller *Controller
}

func NewPhoneHandler(controller *Controller) (*PhoneHandler, error) {
	if controller == nil {
		return nil, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.usable(context.Background()) {
		return nil, ErrUnavailable
	}
	return &PhoneHandler{
		boundary:   newPhoneBoundary(controller.origin, controller.pagePath, controller.tlsProxy),
		controller: controller,
	}, nil
}

func newPhoneBoundary(origin, pagePath string, tlsProxy bool) phoneBoundary {
	parsed, _ := url.Parse(origin) // Origin was already validated by the controller.
	return phoneBoundary{
		origin:    origin,
		authority: parsed.Host,
		hostname:  parsed.Hostname(),
		pagePath:  pagePath,
		tlsProxy:  tlsProxy,
		slots:     make(chan struct{}, 8),
	}
}

// The proxy option is explicit operator configuration for a local HTTPS
// terminator. Forwarded headers alone never establish trusted transport.
func (boundary phoneBoundary) secureTransport(request *http.Request) bool {
	if request.TLS != nil {
		return request.TLS.Version >= tls.VersionTLS13 && request.TLS.ServerName == boundary.hostname
	}
	if !boundary.tlsProxy || len(request.Header.Values("X-Forwarded-Proto")) != 1 ||
		request.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func (boundary phoneBoundary) routePath(request *http.Request) string {
	if !strings.HasPrefix(request.URL.Path, boundary.pagePath) {
		return ""
	}
	return "/" + strings.TrimPrefix(request.URL.Path, boundary.pagePath)
}

func (boundary phoneBoundary) admit(writer http.ResponseWriter, request *http.Request, post bool) bool {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Strict-Transport-Security", "max-age=31536000")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; "+
		"connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if boundary.origin == "" || !boundary.secureTransport(request) ||
		request.Host != boundary.authority ||
		request.URL == nil ||
		request.URL.RawQuery != "" ||
		request.URL.ForceQuery ||
		request.URL.RawPath != "" ||
		request.URL.Fragment != "" ||
		request.URL.IsAbs() ||
		len(request.Header.Values("Origin")) > 1 {
		writePhoneError(writer, http.StatusForbidden)
		return false
	}
	origin := request.Header.Get("Origin")
	if (post && origin != boundary.origin) || (origin != "" && origin != boundary.origin) {
		writePhoneError(writer, http.StatusForbidden)
		return false
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writePhoneError(writer, http.StatusForbidden)
		return false
	}
	if post {
		media, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || len(parameters) > 1 ||
			(len(parameters) == 1 && parameters["charset"] == "") ||
			(parameters["charset"] != "" && strings.ToLower(parameters["charset"]) != "utf-8") {
			writePhoneError(writer, http.StatusBadRequest)
			return false
		}
	}
	return true
}

// Deadlines bound slow bodies and blocked writes as well as controller work.
// Startup must use net/http's deadline-capable HTTPS server, with header limits
// and TLS handshake timeouts. No listener or reverse-proxy trust is introduced.
func (boundary phoneBoundary) startPost(
	writer http.ResponseWriter,
	request *http.Request,
) (context.Context, func(), bool) {
	select {
	case boundary.slots <- struct{}{}:
	default:
		writePhoneError(writer, http.StatusServiceUnavailable)
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	deadline, _ := ctx.Deadline()
	control := http.NewResponseController(writer)
	if err := errors.Join(control.SetReadDeadline(deadline), control.SetWriteDeadline(deadline)); err != nil {
		cancel()
		<-boundary.slots
		writePhoneError(writer, http.StatusServiceUnavailable)
		return nil, nil, false
	}
	return ctx, func() { cancel(); <-boundary.slots }, true
}

type phoneRequest struct {
	ID, Token  string
	Credential json.RawMessage
}
type phoneResponse struct {
	Status    string          `json:"status"`
	ID        string          `json:"id,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
	Scope     *Scope          `json:"scope,omitempty"`
	GrantID   string          `json:"grant_id,omitempty"`
	ExpiresAt string          `json:"expires_at,omitempty"`
}

func decodePhoneRequest(writer http.ResponseWriter, request *http.Request, fields ...string) (phoneRequest, error) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxResponseBytes+1024))
	if err != nil {
		return phoneRequest{}, ErrDenied
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return phoneRequest{}, ErrDenied
	}
	values := make(map[string]json.RawMessage, len(fields))
	for decoder.More() {
		key, keyErr := decoder.Token()
		name, valid := key.(string)
		allowed := false
		for _, field := range fields {
			if name == field {
				allowed = true
			}
		}
		if keyErr != nil || !valid || !allowed {
			return phoneRequest{}, ErrDenied
		}
		if _, duplicate := values[name]; duplicate {
			return phoneRequest{}, ErrDenied
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return phoneRequest{}, ErrDenied
		}
		values[name] = value
	}
	if len(values) != len(fields) {
		return phoneRequest{}, ErrDenied
	}
	if last, tokenErr := decoder.Token(); tokenErr != nil || last != json.Delim('}') {
		return phoneRequest{}, ErrDenied
	}
	if decoder.Decode(new(any)) != io.EOF {
		return phoneRequest{}, ErrDenied
	}
	var result phoneRequest
	for name, value := range values {
		switch name {
		case "id":
			if json.Unmarshal(value, &result.ID) != nil {
				return phoneRequest{}, ErrDenied
			}
		case "token":
			if json.Unmarshal(value, &result.Token) != nil {
				return phoneRequest{}, ErrDenied
			}
		case "credential":
			if len(value) == 0 || value[0] != '{' || len(value) > maxResponseBytes {
				return phoneRequest{}, ErrDenied
			}
			result.Credential = value
		}
	}
	return result, nil
}

func writePhoneError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(`{"status":"unavailable"}`))
}

func writePhoneResponse(writer http.ResponseWriter, response phoneResponse) {
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxResponseBytes+1024 {
		writePhoneError(writer, http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(encoded)
}

func serveCeremony(writer http.ResponseWriter, ceremony Ceremony, err error) {
	if err != nil {
		writePhoneError(writer, http.StatusForbidden)
		return
	}
	writePhoneResponse(
		writer,
		phoneResponse{Status: "pending", ID: ceremony.ID, Options: ceremony.Options, Scope: &ceremony.Scope},
	)
}

func (handler *PhoneHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || handler.controller == nil {
		writePhoneError(writer, http.StatusServiceUnavailable)
		return
	}
	post := request.Method == http.MethodPost
	if !handler.boundary.admit(writer, request, post) {
		return
	}
	routePath := handler.boundary.routePath(request)
	if request.Method == http.MethodGet {
		var asset, contentType string
		switch routePath {
		case "/":
			asset, contentType = "phone/index.html", "text/html; charset=utf-8"
		case "/app.js":
			asset, contentType = "phone/app.js", "text/javascript; charset=utf-8"
		case "/app.css":
			asset, contentType = "phone/app.css", "text/css; charset=utf-8"
		default:
			writePhoneError(writer, http.StatusNotFound)
			return
		}
		data, err := phoneAssets.ReadFile(asset)
		if err != nil {
			writePhoneError(writer, http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", contentType)
		_, _ = writer.Write(data)
		return
	}
	if !post {
		writePhoneError(writer, http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel, admitted := handler.boundary.startPost(writer, request)
	if !admitted {
		return
	}
	defer cancel()
	fields := []string{"id"}
	finish := routePath == "/api/approve/finish" || routePath == "/api/revoke/finish"
	if finish {
		fields = append(fields, "credential")
	}
	body, err := decodePhoneRequest(writer, request, fields...)
	if err != nil {
		writePhoneError(writer, http.StatusBadRequest)
		return
	}
	switch routePath {
	case "/api/approve/begin":
		ceremony, beginErr := handler.controller.BeginApproval(ctx, body.ID)
		serveCeremony(writer, ceremony, beginErr)
	case "/api/revoke/begin":
		ceremony, beginErr := handler.controller.BeginRevocation(ctx, body.ID)
		serveCeremony(writer, ceremony, beginErr)
	case "/api/approve/finish":
		grant, finishErr := handler.controller.FinishApproval(ctx, body.ID, body.Credential)
		if finishErr != nil {
			writePhoneError(writer, http.StatusForbidden)
			return
		}
		writePhoneResponse(
			writer,
			phoneResponse{Status: "approved", GrantID: grant.ID, ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339)},
		)
	case "/api/revoke/finish":
		if handler.controller.FinishRevocation(ctx, body.ID, body.Credential) != nil {
			writePhoneError(writer, http.StatusForbidden)
			return
		}
		writePhoneResponse(writer, phoneResponse{Status: "revoked"})
	default:
		writePhoneError(writer, http.StatusNotFound)
	}
}

// ServeHTTP admits enrollment only with the private console bootstrap token.
// There is no HTTP route to issue or refresh one. Startup mounts this handler
// separately from PhoneHandler, after qualifying the private console boundary.
func (enrollment *Enrollment) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !enrollment.valid() {
		writePhoneError(writer, http.StatusServiceUnavailable)
		return
	}
	if !enrollment.boundary.admit(writer, request, request.Method == http.MethodPost) {
		return
	}
	if request.Method != http.MethodPost {
		writePhoneError(writer, http.StatusMethodNotAllowed)
		return
	}
	routePath := enrollment.boundary.routePath(request)
	ctx, cancel, admitted := enrollment.boundary.startPost(writer, request)
	if !admitted {
		return
	}
	defer cancel()
	fields := []string{"token"}
	if routePath == "/api/enroll/finish" {
		fields = append(fields, "id", "credential")
	}
	body, err := decodePhoneRequest(writer, request, fields...)
	if err != nil {
		writePhoneError(writer, http.StatusBadRequest)
		return
	}
	switch routePath {
	case "/api/enroll/begin":
		ceremony, beginErr := enrollment.begin(ctx, body.Token)
		serveCeremony(writer, ceremony, beginErr)
	case "/api/enroll/finish":
		if enrollment.finish(ctx, body.Token, body.ID, body.Credential) != nil {
			writePhoneError(writer, http.StatusForbidden)
			return
		}
		writePhoneResponse(writer, phoneResponse{Status: "enrolled"})
	default:
		writePhoneError(writer, http.StatusNotFound)
	}
}
