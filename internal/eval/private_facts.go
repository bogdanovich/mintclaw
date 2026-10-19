package eval

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
)

const generatePrivateFact = "MC_EVAL_GENERATE_PRIVATE_V1"

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
		result[handle] = "MC_EVAL_" + hex.EncodeToString(random[:])
	}
	return result, nil
}
