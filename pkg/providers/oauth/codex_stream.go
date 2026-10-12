package oauthprovider

import (
	"bytes"
	"context"
	"net/http"
	"slices"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

func newCodexResponseStream(
	ctx context.Context, service *responses.ResponseService, params responses.ResponseNewParams,
	opts ...option.RequestOption,
) *ssestream.Stream[responses.ResponseStreamEventUnion] {
	var raw *http.Response
	// Use the SDK's service options, authentication and request lifecycle, but
	// filter no-data SSE heartbeats before its typed JSON stream decoder.
	opts = append(slices.Clone(opts), option.WithJSONSet("stream", true), option.WithResponseBodyInto(&raw))
	_, err := service.New(ctx, params, opts...)
	decoder := ssestream.NewDecoder(raw)
	if decoder != nil {
		decoder = codexDataEventDecoder{decoder}
	}
	return ssestream.NewStream[responses.ResponseStreamEventUnion](decoder, err)
}

type codexDataEventDecoder struct {
	ssestream.Decoder
}

func (decoder codexDataEventDecoder) Next() bool {
	for decoder.Decoder.Next() {
		event := decoder.Event()
		// Named events and every nonempty payload still go through the SDK's
		// parser. Malformed responses must not be mistaken for heartbeats.
		if event.Type != "" || len(bytes.TrimSpace(event.Data)) != 0 {
			return true
		}
	}
	return false
}
