package eval

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"regexp"
)

const (
	generatePrivateFact        = "MC_EVAL_GENERATE_PRIVATE_V1"
	generatedPrivateFactPrefix = "MC_EVAL_"
	generatedFactGuardScope    = "generated_canary_format_v1"
)

var generatedPrivateFactPattern = regexp.MustCompile(`MC_EVAL_[0-9a-f]{16}`)

func privateFactPlaceholder(value string) bool {
	return value == generatePrivateFact || value == "{{generate}}"
}

// Generate per-trial values without changing the corpus or its source digest.
func materializePrivateFacts(facts map[string]string) (map[string]string, error) {
	result := maps.Clone(facts)
	for handle, value := range result {
		if !privateFactPlaceholder(value) {
			continue
		}
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			clear(result)
			return nil, err
		}
		result[handle] = generatedPrivateFactPrefix + hex.EncodeToString(random[:])
	}
	return result, nil
}
