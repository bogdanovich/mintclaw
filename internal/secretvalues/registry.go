// Package secretvalues guards process-local outputs against accidental echoes
// of credentials registered by trusted runtime code. Source suppression and
// safe typed results remain the primary credential privacy boundary.
package secretvalues

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	Marker         = "[REDACTED]"
	maxValueBytes  = 32768
	maxValues      = 256
	maxStoredBytes = 8 * 1024 * 1024
)

var ErrUnavailable = errors.New("credential output protection unavailable")

type entry struct {
	references int
	variants   []string
	bytes      int
}

type registry struct {
	mu       sync.Mutex
	values   map[string]*entry
	bytes    int
	snapshot atomic.Pointer[strings.Replacer]
}

var runtimeValues registry

// Register must succeed before a value is used in a browser or connector.
// Keep its release function until every session/operation retaining the value
// has ended. Releasing one registration never removes another owner's guard.
// Capacity denial does not evict earlier values. No values are persisted.
func Register(value []byte) (release func(), err error) {
	return runtimeValues.register(value)
}

func (r *registry) register(value []byte) (func(), error) {
	if len(value) == 0 || len(value) > maxValueBytes || !utf8.Valid(value) {
		return nil, ErrUnavailable
	}
	key := string(value)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]*entry)
	}
	if existing := r.values[key]; existing != nil {
		existing.references++
	} else {
		variants := valueVariants(key)
		size := len(key)
		for _, variant := range variants {
			size += len(variant)
		}
		if len(r.values) >= maxValues || size > maxStoredBytes-r.bytes {
			return nil, ErrUnavailable
		}
		r.values[key] = &entry{references: 1, variants: variants, bytes: size}
		r.bytes += size
		r.rebuildLocked()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			current := r.values[key]
			current.references--
			if current.references == 0 {
				delete(r.values, key)
				r.bytes -= current.bytes
				r.rebuildLocked()
			}
			key = ""
		})
	}, nil
}

func (r *registry) rebuildLocked() {
	if len(r.values) == 0 {
		r.snapshot.Store(nil)
		return
	}
	unique := make(map[string]struct{})
	for _, value := range r.values {
		for _, variant := range value.variants {
			unique[variant] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for value := range unique {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(a, b int) bool {
		if len(ordered[a]) != len(ordered[b]) {
			return len(ordered[a]) > len(ordered[b])
		}
		return ordered[a] < ordered[b]
	})
	pairs := make([]string, 0, 2*len(ordered))
	for _, value := range ordered {
		pairs = append(pairs, value, Marker)
	}
	r.snapshot.Store(strings.NewReplacer(pairs...))
}

func valueVariants(value string) []string {
	variants := make(map[string]struct{})
	for _, normalized := range []string{value, strings.NewReplacer("\r", "", "\n", "").Replace(value)} {
		if normalized == "" {
			continue
		}
		encoded, _ := json.Marshal(normalized)
		var unescaped bytes.Buffer
		encoder := json.NewEncoder(&unescaped)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(normalized)
		// Encoder adds a newline after the closing quote. Loggers such as
		// zerolog use this JSON string encoding with HTML escaping disabled.
		plainJSON := unescaped.Bytes()
		quoted := strconv.Quote(normalized)
		for _, variant := range []string{
			normalized, string(encoded[1 : len(encoded)-1]), quoted[1 : len(quoted)-1],
			string(plainJSON[1 : len(plainJSON)-2]),
			url.QueryEscape(normalized), url.PathEscape(normalized), html.EscapeString(normalized),
			hex.EncodeToString([]byte(normalized)), strings.ToUpper(hex.EncodeToString([]byte(normalized))),
			base64.StdEncoding.EncodeToString([]byte(normalized)), base64.RawStdEncoding.EncodeToString([]byte(normalized)),
			base64.URLEncoding.EncodeToString([]byte(normalized)), base64.RawURLEncoding.EncodeToString([]byte(normalized)),
		} {
			variants[variant] = struct{}{}
		}
	}
	result := make([]string, 0, len(variants))
	for variant := range variants {
		result = append(result, variant)
	}
	return result
}

// Redact protects registered values regardless of ordinary filtering settings.
// It does not promise protection from malicious arbitrary encoding or protect
// screenshots/binary artifacts, which need source-level capture admission.
func Redact(text string) string { return runtimeValues.redact(text) }

func (r *registry) redact(text string) string {
	if snapshot := r.snapshot.Load(); snapshot != nil && text != "" {
		return snapshot.Replace(text)
	}
	return text
}

// RedactRecord handles complete serialized log records. If replacing a numeric
// or structural occurrence would break valid JSON, suppress the entire record
// rather than writing the original value or emitting malformed diagnostics.
func RedactRecord(data []byte) []byte {
	if runtimeValues.snapshot.Load() == nil {
		return data
	}
	filtered := Redact(string(data))
	if filtered == string(data) {
		return data
	}
	result := []byte(filtered)
	if json.Valid(data) && !json.Valid(result) {
		return []byte("{\"level\":\"warn\",\"component\":\"credentials\",\"message\":\"log record redacted\"}\n")
	}
	return result
}
