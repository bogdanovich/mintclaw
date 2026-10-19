package eval

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// Guard the complete public v1 format, including other trials' generated
// values, without retaining any private value. Bare public prefixes are safe.
// Literal/private-domain formats without this invariant remain ineligible.
func offlineGradeFactsEligible(item Case) bool {
	if len(item.Driver.Facts) == 0 {
		return true
	}
	if !isBrowserForm(item) {
		return false
	}
	for _, value := range item.Driver.Facts {
		if !privateFactPlaceholder(value) {
			return false
		}
	}
	return true
}

func newGradeFactGuard() privateFactRetention {
	return privateFactRetention{generated: true}
}

func generatedFactJSONValue(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return true
	}
	scan := privateContentScan{generated: true, tokens: 32768}
	return scan.text(string(data), 0)
}

// Inspect original bytes before decoding can discard duplicate/unknown members
// or project away streams and metadata. The existing bounded JSON-escape guard
// refuses incomplete inspection; it does not claim arbitrary-encoding coverage.
func readGradeEvidence(ctx context.Context, root *os.Root, name string, filter *strings.Replacer) ([]byte, error) {
	data, err := readPrivateReportFile(root, name)
	if err != nil {
		return nil, ErrGrade
	}
	scan := privateContentScan{generated: true, filter: filter, tokens: 1000000}
	if scan.rawEscapes(ctx, data) {
		clear(data)
		return nil, ErrGrade
	}
	return data, nil
}
