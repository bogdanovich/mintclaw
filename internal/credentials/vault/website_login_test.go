package vault

import (
	"errors"
	"testing"

	onepassword "github.com/1password/onepassword-sdk-go"
)

func websiteItem() onepassword.Item {
	item := testItem()
	item.Websites = []onepassword.Website{{URL: "https://accounts.example.com/login"}}
	return item
}

func TestWebsiteLoginRechecksItemWebsiteBeforeConsumingValues(t *testing.T) {
	for _, website := range []string{"https://www.example.com", "https://example.com.attacker.org", "https://other.org"} {
		called := false
		err := providerForItem(websiteItem()).UseWebsiteLogin(t.Context(), testBinding, website, "credentials",
			func(username, password []byte) error {
				called = true
				if string(username) != canaryUsername || string(password) != canaryPassword {
					t.Fatal("private website login values changed")
				}
				return nil
			})
		if (err == nil) != (website == "https://www.example.com") || called != (err == nil) {
			t.Fatal("website metadata mismatch reached credential use")
		}
	}
}

func TestWebsiteTOTPUsesComputedCodeOnlyAndClearsBorrowedBuffer(t *testing.T) {
	const seed = "otpauth://totp/private?secret=SYNTHETIC_PRIVATE_SEED"
	for _, mode := range []string{"success", "missing", "ambiguous", "invalid", "sdk-error", "consumer-error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			item := websiteItem()
			code := "194826"
			details := onepassword.NewItemFieldDetailsTypeVariantOTP(&onepassword.OTPFieldDetails{Code: &code})
			field := onepassword.ItemField{
				ID: "totp", FieldType: onepassword.ItemFieldTypeTOTP, Value: seed,
				Details: &details,
			}
			if mode == "invalid" {
				code = seed
			}
			if mode == "sdk-error" {
				details = onepassword.NewItemFieldDetailsTypeVariantOTP(
					&onepassword.OTPFieldDetails{ErrorMessage: &code},
				)
			}
			if mode != "missing" {
				item.Fields = append(item.Fields, field)
			}
			if mode == "ambiguous" {
				field.ID = "another-code"
				item.Fields = append(item.Fields, field)
			}
			var borrowed []byte
			calls := 0
			err := providerForItem(item).UseWebsiteLogin(t.Context(), testBinding, "https://example.com", "totp",
				func(username, value []byte) error {
					calls++
					borrowed = value
					if len(username) != 0 || string(value) != "194826" {
						t.Fatal("TOTP consumer received a seed or unrelated login values")
					}
					if mode == "consumer-error" {
						return errors.New(seed)
					}
					if mode == "panic" {
						panic(seed)
					}
					return nil
				})
			if (err == nil) != (mode == "success") {
				t.Fatal("TOTP failure was not projected to a bounded private outcome")
			}
			if mode != "success" && mode != "consumer-error" && mode != "panic" && calls != 0 {
				t.Fatal("invalid TOTP reached a consumer")
			}
			for _, value := range borrowed {
				if value != 0 {
					t.Fatal("one-time-code buffer survived private use")
				}
			}
			assertNoCanary(t, err)
		})
	}
}
