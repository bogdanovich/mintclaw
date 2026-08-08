package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type ReleaseAuthority struct {
	Profile                  string   `json:"profile"`
	ProfileRevision          string   `json:"profile_revision"`
	ReleaseAlias             string   `json:"release_alias"`
	Tag                      string   `json:"tag"`
	Version                  string   `json:"version"`
	BaseURL                  string   `json:"base_url"`
	RedirectHosts            []string `json:"redirect_hosts"`
	Channel                  Channel  `json:"channel"`
	AllowDowngrade           bool     `json:"allow_downgrade"`
	KeyID                    string   `json:"key_id"`
	RequirePlatformSignature bool     `json:"require_platform_signature"`
}

func HashReleaseAuthority(authority ReleaseAuthority) (string, error) {
	authority.RedirectHosts = append([]string(nil), authority.RedirectHosts...)
	sort.Strings(authority.RedirectHosts)
	transcript := struct {
		Domain    string           `json:"domain"`
		Authority ReleaseAuthority `json:"authority"`
	}{Domain: "mintclaw-node-update-authority-v1", Authority: authority}
	data, err := json.Marshal(transcript)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
