package vault

import (
	"bytes"
	"errors"
	"testing"

	onepassword "github.com/1password/onepassword-sdk-go"
)

func TestAPIKeyConsumesOnlyExactConcealedCredentialAndClearsBuffer(t *testing.T) {
	const token = "synthetic-private-github-token-P93"
	for _, mode := range []string{"success", "consumer-error", "consumer-panic", "login-category", "wrong-item", "wrong-type", "duplicate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			item := onepassword.Item{
				ID:       testBinding.ItemID,
				VaultID:  testBinding.VaultID,
				Category: onepassword.ItemCategoryAPICredentials,
				Fields: []onepassword.ItemField{
					{ID: "credential", FieldType: onepassword.ItemFieldTypeConcealed, Value: token},
				},
			}
			switch mode {
			case "login-category":
				item.Category = onepassword.ItemCategoryLogin
			case "wrong-item":
				item.ID = "different"
			case "wrong-type":
				item.Fields[0].FieldType = onepassword.ItemFieldTypeText
			case "duplicate":
				item.Fields = append(item.Fields, item.Fields[0])
			case "missing":
				item.Fields = nil
			}
			var retained []byte
			err := providerForItem(item).UseAPIKey(t.Context(), testBinding, func(key []byte) error {
				retained = key
				if !bytes.Equal(key, []byte(token)) {
					t.Fatal("private API credential changed")
				}
				if mode == "consumer-error" {
					return errors.New(token)
				}
				if mode == "consumer-panic" {
					panic(token)
				}
				return nil
			})
			if (err == nil) != (mode == "success") ||
				(retained != nil) != (mode == "success" || mode == "consumer-error" || mode == "consumer-panic") {
				t.Fatal("invalid API item reached consumption or changed failure projection")
			}
			if retained != nil && !bytes.Equal(retained, make([]byte, len(retained))) {
				t.Fatal("API credential buffer retained after consumption")
			}
			if err != nil && err.Error() != ErrUnavailable.Error() && err.Error() != ErrConsumerFailed.Error() {
				t.Fatal("API failure exposed private details")
			}
		})
	}
}
