package supervision

import (
	"errors"
	"strings"
)

var errUnavailable = errors.New("credential worker supervision unavailable")

// Kernel evidence must identify exactly one populated field. Unknown future
// numeric fields are permitted; ambiguity, truncation, or malformed data denies.
func populated(data string) (bool, error) {
	if len(data) == 0 || len(data) > 4096 || !strings.HasSuffix(data, "\n") {
		return false, errUnavailable
	}
	seen := make(map[string]bool)
	var present, live bool
	for line := range strings.SplitSeq(strings.TrimSuffix(data, "\n"), "\n") {
		parts := strings.Split(line, " ")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || seen[parts[0]] {
			return false, errUnavailable
		}
		seen[parts[0]] = true
		for _, value := range parts[0] {
			if (value < 'a' || value > 'z') && value != '_' {
				return false, errUnavailable
			}
		}
		for _, value := range parts[1] {
			if value < '0' || value > '9' {
				return false, errUnavailable
			}
		}
		if parts[0] == "populated" {
			if parts[1] != "0" && parts[1] != "1" {
				return false, errUnavailable
			}
			present, live = true, parts[1] == "1"
		}
	}
	if !present {
		return false, errUnavailable
	}
	return live, nil
}
