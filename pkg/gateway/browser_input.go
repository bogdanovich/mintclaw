package gateway

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/browser"
)

func (source *gatewayBrowserToolSource) CreateProtectedFillBinding(
	ctx context.Context, request browser.PrepareActionRequest,
) (string, error) {
	return withGatewayBrowserBroker(ctx, source, func(ctx context.Context, broker *browser.Broker) (string, error) {
		return broker.CreateProtectedFillBinding(ctx, request)
	})
}

func (source *gatewayBrowserToolSource) InspectProtectedFillBinding(
	ctx context.Context, owner browser.Owner, token string,
) (browser.ProtectedFillTarget, error) {
	return withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.ProtectedFillTarget, error) {
			return broker.InspectProtectedFillBinding(ctx, owner, token)
		})
}

func (source *gatewayBrowserToolSource) PrepareProtectedFill(
	ctx context.Context, owner browser.Owner, token, value string,
) (browser.Preparation, error) {
	return withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.Preparation, error) {
			return broker.PrepareProtectedFill(ctx, owner, token, value)
		})
}

func (source *gatewayBrowserToolSource) ProtectedFillPreparation(
	ctx context.Context, owner browser.Owner, preparedID string,
) (browser.Preparation, error) {
	return withGatewayBrowserBroker(ctx, source,
		func(ctx context.Context, broker *browser.Broker) (browser.Preparation, error) {
			return broker.ProtectedFillPreparation(ctx, owner, preparedID)
		})
}
