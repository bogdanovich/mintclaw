package eval

import (
	"errors"
	"os"
)

var ErrPendingCases = errors.New("eval: pending scenarios require explicit --include-pending")

// Catalog policy is operational metadata, not part of the immutable scenario
// bytes/digest, a verdict, or runtime execution authority.
type SuitePolicy struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Default bool   `json:"default"`
	Reason  string `json:"reason,omitempty"`
}

type corpusCatalog struct {
	Version int           `json:"version"`
	Suites  []SuitePolicy `json:"suites"`
}

func (item LoadedCase) Availability() SuitePolicy {
	if item.Policy == nil {
		return SuitePolicy{ID: item.Case.Suite, Status: "active", Default: true}
	}
	return *item.Policy
}

func (policy SuitePolicy) valid() bool {
	if !validID(policy.ID) {
		return false
	}
	switch policy.Status {
	case "active":
		return policy.Reason == ""
	case "pending":
		return !policy.Default && boundedText(policy.Reason, 1024)
	default:
		return false
	}
}

func loadCatalogPolicy(root *os.Root, cases []LoadedCase) error {
	info, err := root.Lstat("catalog.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil // Existing private corpora remain active/default without a catalog.
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrInvalidCorpus
	}
	data, err := readBoundedFile(root, "catalog.json", MaxManifestBytes)
	if err != nil {
		return ErrInvalidCorpus
	}
	catalog, err := DecodeManifest[corpusCatalog](data)
	if err != nil || catalog.Version != Version || len(catalog.Suites) == 0 || len(catalog.Suites) > MaxCorpusCases {
		return ErrInvalidCorpus
	}
	known := make(map[string]bool)
	for _, item := range cases {
		known[item.Case.Suite] = true
	}
	policies := make(map[string]SuitePolicy, len(catalog.Suites))
	for _, policy := range catalog.Suites {
		if !policy.valid() || !known[policy.ID] || policies[policy.ID].ID != "" {
			return ErrInvalidCorpus
		}
		policies[policy.ID] = policy
	}
	if len(policies) != len(known) {
		return ErrInvalidCorpus
	}
	for index := range cases {
		policy := policies[cases[index].Case.Suite]
		cases[index].Policy = &policy
	}
	return nil
}

// Both CLI selection and direct Run callers must deliberately opt in. This is
// checked before target admission or model work; no pending trial is fabricated
// as passed/skipped. Grade/Compare consume historical evidence, not new tasks.
func CheckRunAvailability(cases []LoadedCase, includePending bool) error {
	for _, item := range cases {
		policy := item.Availability()
		if !policy.valid() || policy.ID != item.Case.Suite {
			return ErrInvalidCorpus
		}
		if policy.Status == "pending" && !includePending {
			return ErrPendingCases
		}
	}
	return nil
}
