// MintClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package httpapi

import (
	"context"
	"time"

	providercapabilities "github.com/bogdanovich/mintclaw/pkg/providers/capabilities"
	"github.com/bogdanovich/mintclaw/pkg/providers/openai_compat"
)

type HTTPProvider struct {
	delegate *openai_compat.Provider
}

func NewHTTPProvider(apiKey, apiBase, proxy string) *HTTPProvider {
	return &HTTPProvider{
		delegate: openai_compat.NewProvider(apiKey, apiBase, proxy),
	}
}

func NewHTTPProviderWithMaxTokensField(apiKey, apiBase, proxy, maxTokensField string) *HTTPProvider {
	return NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(apiKey, apiBase, proxy, maxTokensField, "", 0, nil, nil)
}

func NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
	apiKey, apiBase, proxy, maxTokensField, userAgent string,
	requestTimeoutSeconds int,
	extraBody map[string]any,
	customHeaders map[string]string,
) *HTTPProvider {
	return &HTTPProvider{
		delegate: openai_compat.NewProvider(
			apiKey,
			apiBase,
			proxy,
			openai_compat.WithMaxTokensField(maxTokensField),
			openai_compat.WithRequestTimeout(time.Duration(requestTimeoutSeconds)*time.Second),
			openai_compat.WithExtraBody(extraBody),
			openai_compat.WithCustomHeaders(customHeaders),
			openai_compat.WithUserAgent(userAgent),
		),
	}
}

func (p *HTTPProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	return p.delegate.Chat(ctx, messages, tools, model, options)
}

func (p *HTTPProvider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	return p.delegate.ChatStreamEvents(ctx, messages, tools, model, options, onChunk)
}

func (p *HTTPProvider) GetDefaultModel() string {
	return ""
}

func (p *HTTPProvider) Capabilities() providercapabilities.ProviderCapabilities {
	if p == nil || p.delegate == nil {
		return providercapabilities.ProviderCapabilities{}
	}
	return p.delegate.Capabilities()
}

func (p *HTTPProvider) SetProviderName(providerName string) {
	if p == nil || p.delegate == nil {
		return
	}
	p.delegate.SetProviderName(providerName)
}
