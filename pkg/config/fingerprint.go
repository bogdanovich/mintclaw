package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

// PublicFingerprint identifies the effective public configuration, not secret
// values, credential availability, external resources, or the binary itself.
func PublicFingerprint(cfg *Config) (string, error) {
	public, err := ProjectPublicConfig(cfg)
	if err != nil {
		return "", errors.New("public configuration fingerprint unavailable")
	}
	public.BuildInfo = BuildInfo{}
	data, err := json.Marshal(public)
	if err != nil {
		return "", errors.New("public configuration fingerprint unavailable")
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
