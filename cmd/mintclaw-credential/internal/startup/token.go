// Package startup owns private credential-service startup helpers. No helper is
// registered as a model tool or invoked by the provision-only executable.
package startup

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

const maxTokenBytes = 32768

var errUnavailable = errors.New("credential startup is unavailable")

// serviceToken owns one initialization's bytes. Copies share consumption state;
// no accessor returns a token. The trusted initializer must not retain borrowed
// buffers. Clearing cannot erase copies held by the SDK or Go runtime.
type serviceToken struct{ state *tokenState }

type tokenState struct {
	mu    sync.Mutex
	value []byte
}

func (token serviceToken) consume(initialize func([]byte) error) (result error) {
	if token.state == nil {
		return errUnavailable
	}
	token.state.mu.Lock()
	value := token.state.value
	token.state.value = nil
	token.state.mu.Unlock()
	defer func() {
		clear(value)
		if recover() != nil {
			result = errUnavailable
		}
	}()
	if len(value) == 0 || initialize == nil || initialize(value) != nil {
		return errUnavailable
	}
	return nil
}

func (token serviceToken) destroy() {
	if token.state == nil {
		return
	}
	token.state.mu.Lock()
	defer token.state.mu.Unlock()
	clear(token.state.value)
	token.state.value = nil
}

func (serviceToken) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private service credential]")
}

func (serviceToken) MarshalJSON() ([]byte, error) {
	return []byte(`"[private service credential]"`), nil
}

func (serviceToken) MarshalText() ([]byte, error) { return []byte("[private service credential]"), nil }

func validToken(value []byte) bool {
	if len(value) == 0 || len(value) > maxTokenBytes {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
