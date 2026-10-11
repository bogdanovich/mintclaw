package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	onepassword "github.com/1password/onepassword-sdk-go"
)

const (
	canaryUsername = "synthetic-private-user"
	canaryPassword = "synthetic-private-password"
)

var testBinding = Binding{VaultID: "vault-1", ItemID: "item-1"}

type fakeItems func(context.Context, string, string) (onepassword.Item, error)

func (get fakeItems) Get(ctx context.Context, vaultID, itemID string) (onepassword.Item, error) {
	return get(ctx, vaultID, itemID)
}

func testItem() onepassword.Item {
	return onepassword.Item{
		ID: testBinding.ItemID, VaultID: testBinding.VaultID, Category: onepassword.ItemCategoryLogin,
		Fields: []onepassword.ItemField{
			{ID: "username", FieldType: onepassword.ItemFieldTypeText, Value: canaryUsername},
			{ID: "password", FieldType: onepassword.ItemFieldTypeConcealed, Value: canaryPassword},
			{ID: "extra-field", FieldType: onepassword.ItemFieldTypeConcealed, Value: "synthetic-private-mfa-seed"},
		},
		Notes: "synthetic-private-note",
	}
}

func providerForItem(item onepassword.Item) Provider {
	return newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) { return item, nil }))
}

func TestExactLoginRetrievalAndSingleUse(t *testing.T) {
	var calls int
	provider := newProvider(fakeItems(func(ctx context.Context, vaultID, itemID string) (onepassword.Item, error) {
		calls++
		if vaultID != testBinding.VaultID || itemID != testBinding.ItemID {
			t.Error("retrieval changed the operator binding")
		}
		deadline, exists := ctx.Deadline()
		if !exists || time.Until(deadline) > providerTimeout {
			t.Error("retrieval has no bounded deadline")
		}
		return testItem(), nil
	}))
	login, err := provider.GetLogin(t.Context(), testBinding)
	if err != nil || calls != 1 {
		t.Fatalf("retrieval result = %v, %v", login, err)
	}
	copyOfLogin := login
	var borrowedUsername, borrowedPassword []byte
	err = login.Consume(func(username, password []byte) error {
		if string(username) != canaryUsername || string(password) != canaryPassword {
			t.Error("consumer received unexpected fields")
		}
		borrowedUsername, borrowedPassword = username, password
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range append(borrowedUsername, borrowedPassword...) {
		if value != 0 {
			t.Fatal("owned buffer was not cleared after consumption")
		}
	}
	if err = copyOfLogin.Consume(
		func([]byte, []byte) error { t.Error("login reused"); return nil },
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("a copied login retained usable credentials")
	}
}

func TestLoginAcceptsEmailUsernameWithoutExportingOtherFields(t *testing.T) {
	item := testItem()
	item.Fields[0].FieldType = onepassword.ItemFieldTypeEmail
	login, err := providerForItem(item).GetLogin(t.Context(), testBinding)
	if err != nil {
		t.Fatal(err)
	}
	defer login.Destroy()
	assertNoCanary(t, login)
}

func TestMalformedItemsFailWithoutReturningCredentials(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*onepassword.Item)
	}{
		{name: "other vault", mutate: func(item *onepassword.Item) { item.VaultID = "other" }},
		{name: "other item", mutate: func(item *onepassword.Item) { item.ID = "other" }},
		{
			name:   "unsupported category",
			mutate: func(item *onepassword.Item) { item.Category = onepassword.ItemCategoryPassword },
		},
		{name: "missing password", mutate: func(item *onepassword.Item) { item.Fields = item.Fields[:1] }},
		{name: "missing username", mutate: func(item *onepassword.Item) { item.Fields = item.Fields[1:] }},
		{
			name:   "duplicate password",
			mutate: func(item *onepassword.Item) { item.Fields = append(item.Fields, item.Fields[1]) },
		},
		{name: "empty field ID", mutate: func(item *onepassword.Item) { item.Fields[2].ID = "" }},
		{
			name:   "unconcealed password",
			mutate: func(item *onepassword.Item) { item.Fields[1].FieldType = onepassword.ItemFieldTypeText },
		},
		{
			name:   "concealed username",
			mutate: func(item *onepassword.Item) { item.Fields[0].FieldType = onepassword.ItemFieldTypeConcealed },
		},
		{
			name:   "custom-section password",
			mutate: func(item *onepassword.Item) { section := "section-1"; item.Fields[1].SectionID = &section },
		},
		{name: "empty password", mutate: func(item *onepassword.Item) { item.Fields[1].Value = "" }},
		{
			name:   "oversized password",
			mutate: func(item *onepassword.Item) { item.Fields[1].Value = strings.Repeat("x", maxPasswordBytes+1) },
		},
		{
			name:   "oversized username",
			mutate: func(item *onepassword.Item) { item.Fields[0].Value = strings.Repeat("x", maxUsernameBytes+1) },
		},
		{name: "invalid UTF8", mutate: func(item *onepassword.Item) { item.Fields[1].Value = string([]byte{0xff}) }},
		{name: "NUL", mutate: func(item *onepassword.Item) { item.Fields[1].Value += "\x00" }},
		{name: "multiline password", mutate: func(item *onepassword.Item) { item.Fields[1].Value += "\nother" }},
		{
			name:   "too many fields",
			mutate: func(item *onepassword.Item) { item.Fields = make([]onepassword.ItemField, maxItemFields+1) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := testItem()
			test.mutate(&item)
			login, err := providerForItem(item).GetLogin(t.Context(), testBinding)
			if !errors.Is(err, ErrInvalidLogin) || login.state != nil {
				t.Fatalf("invalid item projection = %v, %v", login, err)
			}
			assertNoCanary(t, err)
		})
	}
}

func TestPrivateFailuresAndPanicsAreProjectedSafely(t *testing.T) {
	for _, panicInstead := range []bool{false, true} {
		provider := newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
			if panicInstead {
				panic(canaryPassword)
			}
			return onepassword.Item{}, errors.New(canaryPassword)
		}))
		login, err := provider.GetLogin(t.Context(), testBinding)
		if !errors.Is(err, ErrUnavailable) || login.state != nil {
			t.Fatalf("private failure result = %v, %v", login, err)
		}
		assertNoCanary(t, err)
	}
	for _, panicInstead := range []bool{false, true} {
		login, err := providerForItem(testItem()).GetLogin(t.Context(), testBinding)
		if err != nil {
			t.Fatal(err)
		}
		var borrowed []byte
		err = login.Consume(func(_, password []byte) error {
			borrowed = password
			if panicInstead {
				panic(canaryPassword)
			}
			return errors.New(canaryPassword)
		})
		if !errors.Is(err, ErrConsumerFailed) {
			t.Fatal("consumer failure was not projected")
		}
		assertNoCanary(t, err)
		for _, value := range borrowed {
			if value != 0 {
				t.Fatal("failed consumer retained owned password buffer")
			}
		}
	}
}

func TestCancellationPreventsRetrievalAndDiscardsLateResults(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	provider := newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
		t.Error("canceled request reached the vault")
		return testItem(), nil
	}))
	if login, err := provider.GetLogin(ctx, testBinding); !errors.Is(err, ErrUnavailable) || login.state != nil {
		t.Fatal("canceled request returned a login")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	provider = newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
		cancel()
		return testItem(), nil
	}))
	if login, err := provider.GetLogin(ctx, testBinding); !errors.Is(err, ErrUnavailable) || login.state != nil {
		t.Fatal("late canceled result returned a login")
	}
}

func TestQueuedRetrievalHonorsCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
		close(entered)
		<-release
		return testItem(), nil
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		login, err := provider.GetLogin(t.Context(), testBinding)
		if err != nil {
			t.Error(err)
		}
		login.Destroy()
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := provider.GetLogin(ctx, testBinding); !errors.Is(err, ErrUnavailable) {
		t.Error("queued canceled retrieval returned success")
	}
	close(release)
	<-done
}

func TestConcurrentConsumptionIsExactlyOnce(t *testing.T) {
	login, err := providerForItem(testItem()).GetLogin(t.Context(), testBinding)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var done sync.WaitGroup
	for range 16 {
		done.Add(1)
		go func() {
			defer done.Done()
			consumeErr := login.Consume(func([]byte, []byte) error { calls.Add(1); return nil })
			if consumeErr != nil && !errors.Is(consumeErr, ErrUnavailable) {
				t.Error(consumeErr)
			}
		}()
	}
	done.Wait()
	if calls.Load() != 1 {
		t.Fatalf("consumer executions = %d, want 1", calls.Load())
	}
}

func TestDestroyAndOutputProjections(t *testing.T) {
	provider := providerForItem(testItem())
	login, err := provider.GetLogin(t.Context(), testBinding)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCanary(t, provider)
	assertNoCanary(t, &provider)
	assertNoCanary(t, login)
	assertNoCanary(t, &login)
	text, err := login.MarshalText()
	if err != nil || strings.Contains(string(text), canaryPassword) {
		t.Fatal("text projection exposed a password")
	}
	login.Destroy()
	login.Destroy()
	if err = login.Consume(
		func([]byte, []byte) error { t.Error("destroyed login consumed"); return nil },
	); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatal("destroyed login retained access")
	}
}

func TestInvalidBindingAndUnconfiguredProviderDoNotRetrieve(t *testing.T) {
	provider := newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
		t.Error("invalid binding reached provider")
		return testItem(), nil
	}))
	for _, binding := range []Binding{{}, {VaultID: "op://vault", ItemID: "item"}, {VaultID: "vault", ItemID: "../item"}} {
		if _, err := provider.GetLogin(t.Context(), binding); !errors.Is(err, ErrInvalidLogin) {
			t.Fatal("invalid binding accepted")
		}
	}
	for _, provider := range []Provider{{}} {
		if _, err := provider.GetLogin(t.Context(), testBinding); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unconfigured provider returned a login")
		}
	}
	if _, err := New(t.Context(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing token accepted")
	}
}

func assertNoCanary(t *testing.T, value any) {
	t.Helper()
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		output := fmt.Sprintf(format, value)
		if strings.Contains(output, "synthetic-private") {
			t.Fatal("format projection contained a private canary")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || strings.Contains(string(encoded), "synthetic-private") {
		t.Fatal("JSON projection failed or contained a private canary")
	}
}
