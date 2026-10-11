package vault

import (
	"context"
	"errors"
	"testing"

	onepassword "github.com/1password/onepassword-sdk-go"
)

func TestUseLoginClearsBuffersAndProjectsConsumerFailures(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var username, password []byte
			err := providerForItem(testItem()).UseLogin(t.Context(), testBinding, func(user, pass []byte) error {
				username, password = user, pass
				if string(user) != canaryUsername || string(pass) != canaryPassword {
					t.Fatal("unexpected private fields")
				}
				if mode == "error" {
					return errors.New(canaryPassword)
				}
				if mode == "panic" {
					panic(canaryPassword)
				}
				return nil
			})
			if (err == nil) != (mode == "success") {
				t.Fatal("unexpected consumption outcome")
			}
			assertNoCanary(t, err)
			for _, data := range [][]byte{username, password} {
				for _, value := range data {
					if value != 0 {
						t.Fatal("owned credential buffers survived consumption")
					}
				}
			}
		})
	}
}

func TestUseLoginRejectsMissingConsumerBeforeRetrieval(t *testing.T) {
	calls := 0
	provider := newProvider(fakeItems(func(context.Context, string, string) (onepassword.Item, error) {
		calls++
		return testItem(), nil
	}))
	if provider.UseLogin(t.Context(), testBinding, nil) == nil || calls != 0 {
		t.Fatal("missing consumer retrieved a login")
	}
}
