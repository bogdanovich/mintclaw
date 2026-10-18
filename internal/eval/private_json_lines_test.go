package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateJSONLinesObserveEveryRawRecord(t *testing.T) {
	for _, name := range []string{"sessions/history.jsonl", "trace.ndjson", "logs/gateway.log"} {
		for _, kind := range []string{"safe", "escaped", "duplicate", "credential", "late-after-opaque"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				home := t.TempDir()
				fact := "PRIVATE_RECORD_CANARY"
				data := "{\"safe\":\"public\\u002dvalue\"}\r\n\n{\"safe\":true}\n"
				switch kind {
				case "escaped":
					data += `{"value":"\u0050RIVATE_RECORD_CANARY"}`
				case "duplicate":
					data += `{"value":"\u0050RIVATE_RECORD_CANARY","value":"public"}`
				case "credential":
					data += `{"value":"private\u002drecord-credential"}`
				case "late-after-opaque":
					data += "not JSON\\opaque\n" + `{"value":"\u0050RIVATE_RECORD_CANARY"}`
				}
				file := filepath.Join(home, name)
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": fact},
					strings.NewReplacer("private-record-credential", "[redacted]"), nil)
				if !result.valid() {
					t.Fatal("invalid observation", result)
				}
				if kind == "safe" {
					if result.Code != "observed" || result.Files != 1 || result.OpaqueFiles != 0 {
						t.Fatal("complete safe JSON records were left opaque", result)
					}
				} else if result.Code != "leak_observed" || !result.LeakObserved || result.verdict() != Fail {
					t.Fatal("raw record leak was hidden by parsing or earlier uncertainty", result)
				}
			})
		}
	}
}

func TestPrivateJSONLinesRefuseUnknownAndExhaustedEvidence(t *testing.T) {
	for _, item := range []struct {
		name, data string
		tokens     int
	}{
		{"truncated", `{"name":"public\u002dvalue"`, 1000},
		{"trailing prose", `{"name":"public\u002dvalue"} trailing`, 1000},
		{"two values per line", `{"name":"public\u002dvalue"} {"safe":true}`, 1000},
		{"opaque nested string", `{"name":"plain\\opaque"}`, 1000},
		{"later incomplete record", "{\"safe\":true}\n{\"name\":", 1000},
		{"oversize line", strings.Repeat(" ", MaxInputBytes+1), 1000},
		{"record limit", strings.Repeat("{}\n", maxPrivateJSONRecords+1), 100000},
		{"empty record limit", strings.Repeat("\n", maxPrivateJSONRecords+1), 100000},
		{"shared token budget", "{\"safe\":true}\n{\"safe\":true}", 6},
	} {
		t.Run(item.name, func(t *testing.T) {
			scan := privateContentScan{facts: map[string]string{"name": "private-canary"}, tokens: item.tokens}
			if !scan.jsonLines([]byte(item.data)) || !scan.uncertain {
				t.Fatal("incomplete record stream became safety proof")
			}
		})
	}
}

func TestPrivateJSONLinesAdmitExactLineBound(t *testing.T) {
	for _, ending := range []string{"", "\n", "\r\n"} {
		scan := privateContentScan{facts: map[string]string{"name": "private-canary"}, tokens: 100000}
		data := strings.Repeat("{}\n", maxPrivateJSONRecords-1) + "{}" + ending
		if scan.jsonLines([]byte(data)) || scan.uncertain {
			t.Fatal("trailing line terminator added a phantom record")
		}
	}
}

func TestPrivateJSONLinesRejectNonJSONWhitespace(t *testing.T) {
	for _, whitespace := range []string{"\v", "\f", "\u00a0", "\u0085"} {
		for _, data := range []string{whitespace + "{}", "{}" + whitespace, "{}\n" + whitespace + "\n{}"} {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "history.jsonl"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			result := observePrivateSurfaces(t.Context(), home, map[string]string{"name": "private-canary"}, nil, nil)
			if !result.valid() || result.Code != "unavailable" || result.OpaqueFiles != 1 || result.LeakObserved {
				t.Fatal("non-JSON whitespace normalized an invalid record", result)
			}
		}
	}
	for _, data := range []string{" \t\r{}\t \r\n\t \r\n{} ", "\t \r\n"} {
		scan := privateContentScan{facts: map[string]string{"name": "private-canary"}, tokens: 100}
		if scan.jsonLines([]byte(data)) || scan.uncertain {
			t.Fatal("ordinary JSON whitespace or CRLF was refused")
		}
	}
}

func TestPrivateJSONLinesKeepOtherFormatsAndRoleContextsStrict(t *testing.T) {
	data := "{\"name\":\"public\\u002dvalue\"}\n{\"safe\":true}\n"
	facts := map[string]string{"name": "private-canary"}
	if !containsPrivate(data, facts) {
		t.Fatal("model context acquired raw-file JSON-stream admission")
	}
	for _, name := range []string{"records.json", "records.log", "other/gateway.log"} {
		home := t.TempDir()
		file := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		result := observePrivateSurfaces(t.Context(), home, facts, nil, nil)
		if !result.valid() || result.Code != "unavailable" || result.OpaqueFiles != 1 || result.LeakObserved {
			t.Fatal("unadmitted raw format became a complete observation", result)
		}
	}
}
