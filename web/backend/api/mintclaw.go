package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	ppid "github.com/bogdanovich/mintclaw/pkg/pid"
)

// registerMintClawRoutes binds MintClaw Channel management endpoints to the ServeMux.
func (h *Handler) registerMintClawRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mintclaw/info", h.handleGetMintClawInfo)
	mux.HandleFunc("POST /api/mintclaw/token", h.handleRegenMintClawToken)
	mux.HandleFunc("POST /api/mintclaw/setup", h.handleMintClawSetup)

	// WebSocket proxy: forward /mintclaw/ws to gateway
	// This allows the frontend to connect via the same port as the web UI,
	// avoiding the need to expose extra ports for WebSocket communication.
	mux.HandleFunc("GET /mintclaw/ws", h.handleWebSocketProxy())
	mux.HandleFunc("GET /mintclaw/media/{id}", h.handleMintClawMediaProxy())
	mux.HandleFunc("HEAD /mintclaw/media/{id}", h.handleMintClawMediaProxy())
}

// createWsProxy creates a reverse proxy to the current gateway WebSocket endpoint.
// The gateway bind host and port are resolved from the latest configuration.
func (h *Handler) createWsProxy(origProtocol string, upstreamProtocol string) *httputil.ReverseProxy {
	wsProxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			r.SetURL(target)
			r.Out.Header.Del(protocolKey)
			if upstreamProtocol != "" {
				r.Out.Header.Set(protocolKey, upstreamProtocol)
			}
		},
		ModifyResponse: func(r *http.Response) error {
			if prot := r.Header.Values(protocolKey); len(prot) > 0 {
				r.Header.Del(protocolKey)
				if origProtocol != "" {
					r.Header.Set(protocolKey, origProtocol)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Errorf("Failed to proxy WebSocket: %v", err)
			http.Error(w, "Gateway unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	return wsProxy
}

func (h *Handler) createMintClawHTTPProxy(token string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			r.SetURL(target)
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Errorf("Failed to proxy MintClaw HTTP request: %v", err)
			http.Error(w, "Gateway unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
}

func (h *Handler) gatewayAvailableForProxy() bool {
	discovered := h.sanitizeGatewayPidData(ppid.ReadPidFileWithCheck(globalConfigDir()), nil)
	return h.gateway.availableForProxy(h.configPath, discovered)
}

func decodeMintClawSettings(cfg *config.Config) (config.MintClawSettings, bool) {
	if cfg == nil {
		return config.MintClawSettings{}, false
	}

	bc := cfg.Channels.GetByType(config.ChannelMintClaw)
	if bc == nil {
		return config.MintClawSettings{}, false
	}

	var mintclawCfg config.MintClawSettings
	if err := bc.Decode(&mintclawCfg); err != nil {
		return config.MintClawSettings{}, false
	}

	return mintclawCfg, bc.Enabled
}

func (h *Handler) writeMintClawInfoResponse(
	w http.ResponseWriter,
	r *http.Request,
	cfg *config.Config,
	changed *bool,
) {
	mintclawCfg, enabled := decodeMintClawSettings(cfg)

	resp := map[string]any{
		"ws_url":  h.buildWsURL(r),
		"enabled": enabled,
	}
	if changed != nil {
		resp["changed"] = *changed
	}
	if mintclawCfg.Token.String() != "" {
		resp["configured"] = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleWebSocketProxy wraps a reverse proxy to handle WebSocket connections.
// It relies on launcher dashboard auth, then injects the raw mintclaw token only
// on the upstream gateway request.
func (h *Handler) handleWebSocketProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.gatewayAvailableForProxy() {
			logger.Warnf("Gateway not available for WebSocket proxy")
			http.Error(w, "Gateway not available", http.StatusServiceUnavailable)
			return
		}

		upstreamProtocol := h.mintclawGatewayProtocol()
		if upstreamProtocol == "" {
			logger.Warn("MintClaw token unavailable for WebSocket proxy")
			http.Error(w, "MintClaw channel not configured", http.StatusServiceUnavailable)
			return
		}

		var origProtocol string
		if prot := r.Header.Values(protocolKey); len(prot) > 0 {
			origProtocol = prot[0]
		}

		h.createWsProxy(origProtocol, upstreamProtocol).ServeHTTP(w, r)
	}
}

func (h *Handler) handleMintClawMediaProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.gatewayAvailableForProxy() {
			logger.Warnf("Gateway not available for MintClaw media proxy")
			http.Error(w, "Gateway not available", http.StatusServiceUnavailable)
			return
		}

		mintclawToken := h.gateway.mintclawTokenValue()

		if mintclawToken == "" {
			logger.Warnf("Missing MintClaw token for media proxy")
			http.Error(w, "Invalid MintClaw token", http.StatusForbidden)
			return
		}

		h.createMintClawHTTPProxy(mintclawToken).ServeHTTP(w, r)
	}
}

// handleGetMintClawInfo returns non-secret MintClaw connection info for the launcher UI.
//
//	GET /api/mintclaw/info
func (h *Handler) handleGetMintClawInfo(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.readConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	h.writeMintClawInfoResponse(w, r, cfg, nil)
}

// handleRegenMintClawToken rotates the raw MintClaw WebSocket token and returns
// non-secret connection info for the launcher UI.
//
//	POST /api/mintclaw/token
func (h *Handler) handleRegenMintClawToken(w http.ResponseWriter, r *http.Request) {
	token := generateSecureToken()
	snapshot, err := h.updateConfig(func(cfg *config.Config) error {
		if bc := cfg.Channels.GetByType(config.ChannelMintClaw); bc != nil {
			decoded, decodeErr := bc.GetDecoded()
			if decodeErr == nil && decoded != nil {
				if settings, ok := decoded.(*config.MintClawSettings); ok {
					settings.Token = *config.NewSecureString(token)
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	h.gateway.setMintClawToken(token)

	h.writeMintClawInfoResponse(w, r, snapshot.Config, nil)
}

// EnsureMintClawChannel enables the MintClaw channel with sane defaults if it isn't
// already configured. Returns true when the config was modified.
func (h *Handler) EnsureMintClawChannel() (bool, error) {
	return ensureMintClawChannel(h.configRepository())
}

func ensureMintClawChannel(repository *config.Repository) (bool, error) {
	changed := false
	_, err := repository.Update(func(cfg *config.Config) error {
		bc := cfg.Channels.GetByType(config.ChannelMintClaw)
		if bc == nil {
			bc = &config.Channel{Type: config.ChannelMintClaw}
			cfg.Channels["mintclaw"] = bc
			changed = true
		}
		if !bc.Enabled {
			bc.Enabled = true
			changed = true
		}
		if decoded, decodeErr := bc.GetDecoded(); decodeErr == nil && decoded != nil {
			if mintclawCfg, ok := decoded.(*config.MintClawSettings); ok {
				if mintclawCfg.Token.String() == "" {
					mintclawCfg.Token = *config.NewSecureString(generateSecureToken())
					changed = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to save config: %w", err)
	}

	return changed, nil
}

// handleMintClawSetup automatically configures everything needed for the MintClaw Channel to work.
//
//	POST /api/mintclaw/setup
func (h *Handler) handleMintClawSetup(w http.ResponseWriter, r *http.Request) {
	changed, err := h.EnsureMintClawChannel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reload config (EnsureMintClawChannel may have modified it).
	cfg, err := h.readConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	h.writeMintClawInfoResponse(w, r, cfg, &changed)
}

// generateSecureToken creates a random 32-character hex string.
func generateSecureToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to something pseudo-random if crypto/rand fails
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
