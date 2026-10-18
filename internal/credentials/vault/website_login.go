package vault

import (
	"context"
	"slices"

	onepassword "github.com/1password/onepassword-sdk-go"
)

// UseWebsiteLogin rechecks the full item's website metadata at the point of
// retrieval. username and secret are private borrowed buffers. secret is the
// password for a credentials/username/password step, or a freshly computed code
// for a totp step. No TOTP seed or arbitrary item field is given to a consumer.
func (provider Provider) UseWebsiteLogin(ctx context.Context, binding Binding, website, step string,
	consumer func(username, secret []byte) error,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = ErrUnavailable
		}
	}()
	if consumer == nil || !slices.Contains([]string{"credentials", "username", "password", "totp"}, step) {
		return ErrUnavailable
	}
	item, err := provider.getItem(ctx, binding)
	if err != nil || !itemMatchesWebsite(item, website) {
		return ErrUnavailable
	}
	if step == "totp" {
		return consumeItemTOTP(item, consumer)
	}
	login, err := loginFromItem(binding, item)
	if err != nil {
		return ErrUnavailable
	}
	defer login.Destroy()
	return login.Consume(consumer)
}

func itemMatchesWebsite(item onepassword.Item, website string) bool {
	domain, valid := websiteDomain(website)
	if !valid || item.Category != onepassword.ItemCategoryLogin || len(item.Websites) > 64 {
		return false
	}
	for _, saved := range item.Websites {
		if matched, ok := websiteDomain(saved.URL); ok && matched == domain {
			return true
		}
	}
	return false
}

func consumeItemTOTP(item onepassword.Item, consumer func([]byte, []byte) error) error {
	if len(item.Fields) > maxItemFields {
		return ErrTOTPUnavailable
	}
	var code []byte
	defer func() { clear(code) }()
	seen := make(map[string]bool, len(item.Fields))
	for _, field := range item.Fields {
		if field.ID == "" || seen[field.ID] {
			return ErrTOTPUnavailable
		}
		seen[field.ID] = true
		if field.FieldType != onepassword.ItemFieldTypeTOTP {
			continue
		}
		if code != nil {
			return ErrTOTPUnavailable // An ambiguous second factor requires a handoff.
		}
		if field.Details == nil {
			return ErrTOTPUnavailable
		}
		details := field.Details.OTP()
		if details == nil || details.Code == nil || details.ErrorMessage != nil || !validTOTPCode(*details.Code) {
			return ErrTOTPUnavailable
		}
		code = []byte(*details.Code)
	}
	if len(code) == 0 {
		return ErrTOTPUnavailable
	}
	if consumer(nil, code) != nil {
		return ErrConsumerFailed
	}
	return nil
}

func validTOTPCode(code string) bool {
	if len(code) < 6 || len(code) > 10 {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
