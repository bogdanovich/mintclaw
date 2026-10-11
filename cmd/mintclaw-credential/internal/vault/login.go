package vault

import (
	"fmt"
	"io"
	"sync"
)

// Login owns a single-use, private credential buffer. Copies share the same
// consumption state. Generic formatting and serialization reveal no fields.
// Trusted consumers must not retain or expose the borrowed buffers.
type Login struct {
	state *loginState
}

type loginState struct {
	mu       sync.Mutex
	username []byte
	password []byte
}

// Consume lends credentials to one synchronous private injection operation,
// then clears the owned buffers. SDK/string/browser copies cannot be guaranteed
// erased. An error or panic is projected to a fixed failure; consumers must
// treat a failed injection as possibly executed and quarantine its browser.
func (login Login) Consume(consumer func(username, password []byte) error) (useErr error) {
	if login.state == nil {
		return ErrUnavailable
	}
	login.state.mu.Lock()
	username, password := login.state.username, login.state.password
	login.state.username, login.state.password = nil, nil
	login.state.mu.Unlock()
	defer func() {
		clear(username)
		clear(password)
		if recover() != nil {
			useErr = ErrConsumerFailed
		}
	}()
	if len(username) == 0 || len(password) == 0 || consumer == nil {
		return ErrUnavailable
	}
	if consumer(username, password) != nil {
		return ErrConsumerFailed
	}
	return nil
}

// Destroy clears an unconsumed login. It cannot cancel an injection that
// already borrowed its buffers; browser cancellation is a service obligation.
func (login Login) Destroy() {
	if login.state == nil {
		return
	}
	login.state.mu.Lock()
	defer login.state.mu.Unlock()
	clear(login.state.username)
	clear(login.state.password)
	login.state.username, login.state.password = nil, nil
}

func (Login) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private login]")
}

func (Login) MarshalJSON() ([]byte, error) {
	return []byte(`"[private login]"`), nil
}

func (Login) MarshalText() ([]byte, error) {
	return []byte("[private login]"), nil
}
