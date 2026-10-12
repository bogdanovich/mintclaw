package agent

import (
	"context"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

// One transport retry stays inside the same reserved, tools-disabled call. It
// does not replay intake admission, reset native limits or retry decoded output.
func callDocumentPrivateModel(
	ctx context.Context, model documentFormAuditModel, instruction string, payload []byte,
) (*providers.LLMResponse, error) {
	ctx = protocoltypes.WithSensitiveRequest(ctx)
	messages := []providers.Message{
		{Role: "system", Content: instruction}, {Role: "user", Content: string(payload)},
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		options := map[string]any{"temperature": 0, "native_search": false}
		protocoltypes.DisablePromptCache(options)
		response, err := model.provider.Chat(ctx, messages, nil, model.model, options)
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		var failure *providers.ProviderError
		if err == nil || attempt == 1 || !errors.As(err, &failure) || failure == nil {
			return response, err
		}
		switch failure.Kind {
		case "transient", "network", "timeout":
		default:
			return nil, err
		}
		// Respect a short server delay; a longer recovery belongs to the existing
		// explicit retained-input retry, not an unbounded private wait.
		if failure.RetryAfter < 0 || failure.RetryAfter > 5*time.Second {
			return nil, err
		}
		if failure.RetryAfter > 0 {
			timer := time.NewTimer(failure.RetryAfter)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
}
