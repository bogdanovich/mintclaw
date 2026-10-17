package vault

import (
	"context"

	onepassword "github.com/1password/onepassword-sdk-go"
)

// UseAPIKey privately consumes only the built-in concealed credential field of
// an exact API Credential item. Callers enforce a live grant and fixed API scope.
func (provider Provider) UseAPIKey(ctx context.Context, binding Binding,
	use func([]byte) error,
) (resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = ErrUnavailable
		}
	}()
	if use == nil {
		return ErrUnavailable
	}
	item, err := provider.getItem(ctx, binding)
	if err != nil || item.Category != onepassword.ItemCategoryAPICredentials || len(item.Fields) > maxItemFields {
		return ErrUnavailable
	}
	seen := make(map[string]struct{}, len(item.Fields))
	var credential string
	for _, field := range item.Fields {
		if field.ID == "" {
			return ErrUnavailable
		}
		if _, exists := seen[field.ID]; exists {
			return ErrUnavailable
		}
		seen[field.ID] = struct{}{}
		if field.ID == "credential" {
			if field.SectionID != nil || field.FieldType != onepassword.ItemFieldTypeConcealed {
				return ErrUnavailable
			}
			credential = field.Value
		}
	}
	if !validField(credential, maxPasswordBytes) || ctx.Err() != nil {
		return ErrUnavailable
	}
	value := []byte(credential)
	defer clear(value)
	if use(value) != nil {
		return ErrConsumerFailed
	}
	return nil
}
