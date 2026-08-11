package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

var errConfigPreconditionRequired = errors.New("config revision precondition is required")

type configMutationRequestError struct {
	status int
	err    error
}

func (e *configMutationRequestError) Error() string { return e.err.Error() }

func writeConfigMutationError(w http.ResponseWriter, err error) bool {
	var requestErr *configMutationRequestError
	if !errors.As(err, &requestErr) {
		return false
	}
	http.Error(w, requestErr.Error(), requestErr.status)
	return true
}

func (h *Handler) configRepository() *config.Repository {
	return config.NewRepository(h.configPath)
}

func (h *Handler) readConfigSnapshot() (config.Snapshot, error) {
	return h.configRepository().ReadOnly()
}

func (h *Handler) readConfig() (*config.Config, error) {
	snapshot, err := h.readConfigSnapshot()
	return snapshot.Config, err
}

func (h *Handler) updateConfig(mutate func(*config.Config) error) (config.Snapshot, error) {
	return h.configRepository().Update(mutate)
}

func configRevisionETag(revision config.Revision) string {
	return `"` + string(revision) + `"`
}

func writeConfigRevision(w http.ResponseWriter, revision config.Revision) {
	w.Header().Set("ETag", configRevisionETag(revision))
}

func expectedConfigRevision(r *http.Request) (config.Revision, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return "", errConfigPreconditionRequired
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", errors.New("If-Match must contain one quoted config revision")
	}
	value := raw[1 : len(raw)-1]
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("invalid config revision %q", value)
	}
	return config.Revision(value), nil
}

func writeConfigConflict(w http.ResponseWriter, conflict *config.ConflictError) {
	writeConfigRevision(w, conflict.Actual)
	http.Error(w, "Configuration changed since it was loaded", http.StatusPreconditionFailed)
}
