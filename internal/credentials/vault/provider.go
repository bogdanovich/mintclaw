// Package vault is a runtime-internal adapter for exact 1Password login reads.
// Trusted MintClaw code may use it after approval; secret values must stay
// outside model-facing results, diagnostics and durable output. It supplies
// no model tool, transport, setup command, or direct grant-issuing API.
package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	onepassword "github.com/1password/onepassword-sdk-go"
)

const (
	providerTimeout  = 10 * time.Second
	maxTokenBytes    = 32768
	maxItemFields    = 64
	maxUsernameBytes = 1024
	maxPasswordBytes = 4096
)

var (
	ErrUnavailable    = errors.New("private vault unavailable")
	ErrInvalidLogin   = errors.New("private login item is invalid")
	ErrConsumerFailed = errors.New("private credential consumer failed")
	// The SDK initializes a shared core without its own initialization lock.
	sdkInitMu sync.Mutex
)

// Binding is immutable operator configuration. The returned item's IDs must
// exactly equal these IDs, so titles or ambiguous provider references fail.
type Binding struct {
	VaultID string
	ItemID  string
}

type itemGetter interface {
	Get(context.Context, string, string) (onepassword.Item, error)
}

// Provider retains one private SDK client and permits one retrieval at a time.
// Copies share the same client and retrieval queue; its zero value denies use.
// No SDK operation other than Items.Get is exposed to service consumers.
type Provider struct {
	client *onepassword.Client
	items  itemGetter
	slot   chan struct{}
}

// New is called by the trusted runtime after private operator token setup.
// It does not read the agent environment or publish a credential result. The
// caller owns token; the SDK retains its own copy for its private lifetime.
func New(ctx context.Context, token []byte) (result Provider, resultErr error) {
	defer func() {
		if recover() != nil {
			result, resultErr = Provider{}, ErrUnavailable
		}
	}()
	if ctx == nil || ctx.Err() != nil || len(token) == 0 || len(token) > maxTokenBytes ||
		strings.ContainsAny(string(token), "\x00\r\n") {
		return Provider{}, ErrUnavailable
	}
	sdkInitMu.Lock()
	defer sdkInitMu.Unlock()
	if ctx.Err() != nil {
		return Provider{}, ErrUnavailable
	}
	client, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(string(token)),
		onepassword.WithIntegrationInfo("MintClaw Credential Service", "0.1.0"),
	)
	if err != nil || ctx.Err() != nil {
		return Provider{}, ErrUnavailable
	}
	provider := newProvider(client.Items())
	provider.client = client
	return provider, nil
}

func newProvider(items itemGetter) Provider {
	return Provider{items: items, slot: make(chan struct{}, 1)}
}

// UseLogin retrieves and consumes one login entirely inside the private service.
// The consumer must recheck current authorization and live browser readiness
// after retrieval, before injecting, and quarantine any uncertain result.
func (provider Provider) UseLogin(ctx context.Context, binding Binding,
	consumer func(username, password []byte) error,
) error {
	if ctx == nil || ctx.Err() != nil || consumer == nil {
		return ErrUnavailable
	}
	login, err := provider.GetLogin(ctx, binding)
	if err != nil {
		return ErrUnavailable
	}
	defer login.Destroy()
	return login.Consume(consumer)
}

// GetLogin retrieves only a login's built-in username and password. It accepts
// no secret URI, field name, destination, arbitrary SDK call, or model input.
// The service must check its grant before this call and again before injection.
func (provider Provider) GetLogin(ctx context.Context, binding Binding) (result Login, resultErr error) {
	defer func() {
		if recover() != nil {
			result, resultErr = Login{}, ErrUnavailable
		}
	}()
	if ctx == nil || ctx.Err() != nil || provider.items == nil || provider.slot == nil {
		return Login{}, ErrUnavailable
	}
	if !validProviderID(binding.VaultID) || !validProviderID(binding.ItemID) {
		return Login{}, ErrInvalidLogin
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	defer runtime.KeepAlive(provider.client)
	select {
	case provider.slot <- struct{}{}:
		defer func() { <-provider.slot }()
	case <-requestCtx.Done():
		return Login{}, ErrUnavailable
	}
	if requestCtx.Err() != nil {
		return Login{}, ErrUnavailable
	}
	item, err := provider.items.Get(requestCtx, binding.VaultID, binding.ItemID)
	if err != nil || requestCtx.Err() != nil {
		return Login{}, ErrUnavailable
	}
	return loginFromItem(binding, item)
}

func loginFromItem(binding Binding, item onepassword.Item) (Login, error) {
	if item.VaultID != binding.VaultID || item.ID != binding.ItemID ||
		item.Category != onepassword.ItemCategoryLogin || len(item.Fields) > maxItemFields {
		return Login{}, ErrInvalidLogin
	}
	var username, password string
	seen := make(map[string]struct{}, len(item.Fields))
	for _, field := range item.Fields {
		if field.ID == "" {
			return Login{}, ErrInvalidLogin
		}
		if _, exists := seen[field.ID]; exists {
			return Login{}, ErrInvalidLogin
		}
		seen[field.ID] = struct{}{}
		switch field.ID {
		case "username":
			if field.SectionID != nil || (field.FieldType != onepassword.ItemFieldTypeText &&
				field.FieldType != onepassword.ItemFieldTypeEmail) {
				return Login{}, ErrInvalidLogin
			}
			username = field.Value
		case "password":
			if field.SectionID != nil || field.FieldType != onepassword.ItemFieldTypeConcealed {
				return Login{}, ErrInvalidLogin
			}
			password = field.Value
		}
	}
	if !validField(username, maxUsernameBytes) || !validField(password, maxPasswordBytes) {
		return Login{}, ErrInvalidLogin
	}
	return Login{state: &loginState{username: []byte(username), password: []byte(password)}}, nil
}

func validField(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validProviderID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return false
		}
	}
	return true
}

// Format prevents generic debugging output from traversing the SDK client,
// whose private configuration contains the service-account token.
func (Provider) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private vault]")
}

func (Provider) MarshalJSON() ([]byte, error) {
	return []byte(`"[private vault]"`), nil
}
