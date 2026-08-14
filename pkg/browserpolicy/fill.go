package browserpolicy

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSensitiveFieldTerms = 64

var builtInSensitiveFieldTerms = []string{
	"password", "passcode", "pass phrase", "secret", "credential", "login",
	"authentication", "auth code", "one-time", "one time", "otp", "2fa", "mfa",
	"two-factor", "two factor", "verification", "recovery", "pin", "card", "pan",
	"cvv", "cvc", "expiration", "expiry", "routing number", "bank account", "iban",
	"swift", "social security", "ssn", "tax id",
}

var ordinaryFieldTerms = []string{
	"search", "query", "name", "email", "e-mail", "phone", "telephone", "address",
	"city", "state", "province", "region", "postal", "zip", "country", "organization",
	"company", "title", "website", "url", "description", "comment", "message", "note",
	"subject", "age", "quantity", "count",
}

// NormalizeSensitiveFieldTerms validates and canonicalizes private
// operator-defined field identity fragments. Terms are matched against current
// private DOM and accessibility identity; they are never exposed to the model.
func NormalizeSensitiveFieldTerms(terms []string) ([]string, error) {
	if len(terms) > MaxSensitiveFieldTerms {
		return nil, errors.New("sensitive_fields exceeds the 64-entry limit")
	}
	normalized := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		value := NormalizeFieldIdentity(term)
		if value == "" || len(value) > 128 || !utf8.ValidString(value) {
			return nil, errors.New("sensitive_fields contains an invalid term")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("sensitive_fields contains a duplicate term")
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	slices.Sort(normalized)
	return normalized, nil
}

// NormalizeFieldIdentity creates the shared comparison form used at the
// broker, companion host, and final private driver boundary.
func NormalizeFieldIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// OrdinaryFillField admits only positively identified ordinary text fields.
// Generic, credential, payment, one-time-code, and operator-designated fields
// fail closed.
func OrdinaryFillField(role, name string, sensitiveTerms []string) bool {
	if role != "textbox" && role != "searchbox" {
		return false
	}
	normalizedSensitive, err := NormalizeSensitiveFieldTerms(sensitiveTerms)
	if err != nil {
		return false
	}
	identity := NormalizeFieldIdentity(name)
	if identity == "" || containsFieldTerm(identity, builtInSensitiveFieldTerms) ||
		containsFieldTerm(identity, normalizedSensitive) {
		return false
	}
	return containsFieldTerm(identity, ordinaryFieldTerms)
}

// BuiltInSensitiveFieldTerms returns a copy for the private driver classifier.
func BuiltInSensitiveFieldTerms() []string {
	return append([]string(nil), builtInSensitiveFieldTerms...)
}

// OrdinaryFieldTerms returns a copy for the private driver classifier.
func OrdinaryFieldTerms() []string {
	return append([]string(nil), ordinaryFieldTerms...)
}

func containsFieldTerm(identity string, terms []string) bool {
	for _, term := range terms {
		for offset := 0; term != "" && offset <= len(identity)-len(term); {
			index := strings.Index(identity[offset:], term)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(term)
			leftBoundary := start == 0
			if !leftBoundary {
				left, _ := utf8.DecodeLastRuneInString(identity[:start])
				leftBoundary = !unicode.IsLetter(left) && !unicode.IsNumber(left)
			}
			rightBoundary := end == len(identity)
			if !rightBoundary {
				right, _ := utf8.DecodeRuneInString(identity[end:])
				rightBoundary = !unicode.IsLetter(right) && !unicode.IsNumber(right)
			}
			if leftBoundary && rightBoundary {
				return true
			}
			offset = start + 1
		}
	}
	return false
}
