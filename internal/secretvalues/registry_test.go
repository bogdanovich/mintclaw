package secretvalues

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegisteredValuesAndAccidentalEncodingsPreservePublicMetadata(t *testing.T) {
	value := "canary-runtime-A9&\"/\n雪"
	release, err := Register([]byte(value))
	if err != nil {
		t.Fatal("registration unavailable")
	}
	defer release()
	encoded, _ := json.Marshal(value)
	for name, candidate := range map[string]string{
		"raw": value, "json": string(encoded[1 : len(encoded)-1]),
		"url": url.QueryEscape(value), "path": url.PathEscape(value),
		"hex":                 hex.EncodeToString([]byte(value)),
		"base64":              base64.StdEncoding.EncodeToString([]byte(value)),
		"base64url":           base64.RawURLEncoding.EncodeToString([]byte(value)),
		"input normalization": strings.ReplaceAll(value, "\n", ""),
	} {
		t.Run(name, func(t *testing.T) {
			got := Redact("alias=ebay outcome=failed detail=" + candidate)
			if strings.Contains(got, candidate) || !strings.Contains(got, "alias=ebay outcome=failed") {
				t.Fatal("secret leaked or safe metadata was lost")
			}
		})
	}
}

func TestJSONWithoutHTMLEscapingProtectsCombinedControlCharacters(t *testing.T) {
	release, err := Register([]byte("fixture-json-A&\x00B"))
	if err != nil {
		t.Fatal("registration unavailable")
	}
	defer release()
	const record = `{"alias":"ebay","detail":"fixture-json-A&\u0000B"}`
	got := RedactRecord([]byte(record))
	var decoded map[string]string
	if json.Unmarshal(got, &decoded) != nil || decoded["alias"] != "ebay" || decoded["detail"] != Marker {
		t.Fatal("non-HTML-escaped JSON leaked a registered value or lost safe metadata")
	}
}

func TestSharedAndOverlappingRegistrationLifetime(t *testing.T) {
	var r registry
	short := "fixture-prefix-93"
	long := short + "-continued"
	releaseA, err := r.register([]byte(short))
	if err != nil {
		t.Fatal("first registration unavailable")
	}
	defer releaseA()
	releaseB, _ := r.register([]byte(short))
	defer releaseB()
	releaseLong, _ := r.register([]byte(long))
	defer releaseLong()
	if r.redact(short+"/"+long) != Marker+"/"+Marker {
		t.Fatal("shorter secret shadowed a longer value")
	}
	releaseA()
	releaseA()
	if r.redact(short) != Marker {
		t.Fatal("one owner withdrew another owner's guard")
	}
	releaseB()
	if r.redact(short) != short || r.redact(long) != Marker {
		t.Fatal("independent registration lifetime changed")
	}
	releaseLong()
	if r.snapshot.Load() != nil {
		t.Fatal("closed registry retained an active snapshot")
	}
}

func TestCapacityDenialDoesNotEvictActiveSecrets(t *testing.T) {
	var r registry
	for i := range maxValues {
		value := "bounded-fixture-" + strconv.Itoa(i)
		release, err := r.register([]byte(value))
		if err != nil {
			t.Fatal("bounded registration unavailable")
		}
		defer release()
	}
	if release, err := r.register([]byte("over-capacity-fixture")); !errors.Is(err, ErrUnavailable) || release != nil {
		t.Fatal("capacity admitted an unguarded value")
	}
	if r.redact("bounded-fixture-0") != Marker {
		t.Fatal("capacity denial evicted an active value")
	}
	for _, value := range [][]byte{nil, {0xff}, make([]byte, maxValueBytes+1)} {
		if release, err := r.register(value); !errors.Is(err, ErrUnavailable) || release != nil {
			t.Fatal("invalid value was admitted")
		}
	}
}

func TestConcurrentOwnersKeepLiveValuesGuarded(t *testing.T) {
	var r registry
	var workers sync.WaitGroup
	var failed atomic.Bool
	for range 8 {
		workers.Go(func() {
			for range 100 {
				release, err := r.register([]byte("concurrent-fixture-9261"))
				if err != nil {
					failed.Store(true)
					return
				}
				if r.redact("concurrent-fixture-9261") != Marker {
					failed.Store(true)
				}
				release()
			}
		})
	}
	workers.Wait()
	if failed.Load() {
		t.Fatal("concurrent live registration lost protection")
	}
}

func TestNumericLogOccurrenceSuppressesRecordWithoutInvalidJSON(t *testing.T) {
	release, err := Register([]byte("913487"))
	if err != nil {
		t.Fatal("OTP registration unavailable")
	}
	defer release()
	got := RedactRecord([]byte(`{"level":"info","detail":913487}`))
	if !json.Valid(got) || strings.Contains(string(got), "913487") {
		t.Fatal("numeric log value leaked or corrupted JSON")
	}
}
