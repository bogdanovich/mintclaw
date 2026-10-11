package startup

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const tokenCanary = "MINTCLAW_SYNTHETIC_LOADER_TOKEN_v1"

func newTestToken() serviceToken { return serviceToken{&tokenState{value: []byte(tokenCanary)}} }

func TestTokenInitializationIsSingleUseAndClearsOwnedBytes(t *testing.T) {
	for _, outcome := range []string{"success", "error", "panic", "nil"} {
		t.Run(outcome, func(t *testing.T) {
			token := newTestToken()
			copy := token
			owned := token.state.value
			var initialize func([]byte) error
			if outcome != "nil" {
				initialize = func(value []byte) error {
					if !bytes.Equal(value, []byte(tokenCanary)) {
						t.Fatal("initializer lost its private input")
					}
					switch outcome {
					case "error":
						return errors.New(tokenCanary)
					case "panic":
						panic(tokenCanary)
					default:
						return nil
					}
				}
			}
			err := token.consume(initialize)
			if outcome == "success" && err != nil || outcome != "success" && !errors.Is(err, errUnavailable) {
				t.Fatal("private initialization returned the wrong fixed outcome")
			}
			if err != nil && strings.Contains(err.Error(), tokenCanary) {
				t.Fatal("private initialization disclosed its diagnostic input")
			}
			if !bytes.Equal(owned, make([]byte, len(owned))) {
				t.Fatal("owned token bytes survived initialization")
			}
			if copy.consume(func([]byte) error { t.Fatal("token was reused"); return nil }) == nil {
				t.Fatal("copied token retained authority")
			}
		})
	}
}

func TestConcurrentTokenCopiesInitializeOnlyOnce(t *testing.T) {
	token := newTestToken()
	var calls, successes atomic.Int32
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func(copy serviceToken) {
			defer workers.Done()
			if copy.consume(func([]byte) error { calls.Add(1); return nil }) == nil {
				successes.Add(1)
			}
		}(token)
	}
	workers.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatal("concurrent copies initialized more than once")
	}
}

func TestDestroyedZeroAndFormattedTokensDiscloseNoCredential(t *testing.T) {
	token := newTestToken()
	owned := token.state.value
	for _, value := range []any{token, &token, map[string]any{"credential": token}} {
		jsonData, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range []string{
			fmt.Sprintf("%v %+v %#v %s %q", value, value, value, value, value), string(jsonData),
		} {
			for _, forbidden := range []string{
				tokenCanary, base64.StdEncoding.EncodeToString([]byte(tokenCanary)), hex.EncodeToString([]byte(tokenCanary)),
			} {
				if strings.Contains(data, forbidden) {
					t.Fatal("generic formatting disclosed a private credential")
				}
			}
		}
	}
	text, err := token.MarshalText()
	if err != nil || bytes.Contains(text, []byte(tokenCanary)) {
		t.Fatal("text serialization disclosed a private credential")
	}
	copy := token
	token.destroy()
	if !bytes.Equal(owned, make([]byte, len(owned))) || copy.consume(func([]byte) error { return nil }) == nil ||
		(serviceToken{}).consume(func([]byte) error { return nil }) == nil {
		t.Fatal("destroyed or zero token retained authority")
	}
}
