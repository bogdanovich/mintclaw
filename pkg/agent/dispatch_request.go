package agent

import (
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

// DispatchRequest is the normalized runtime input passed into the agent loop
// after routing and session allocation have completed.
type DispatchRequest struct {
	RouteSessionKey string
	BaseSessionKey  string
	SessionKey      string
	InboundContext  *bus.InboundContext
	RouteResult     *routing.ResolvedRoute
	SessionScope    *session.SessionScope
	UserMessage     string
	Media           []string
}

func cloneDispatchRequest(request DispatchRequest) DispatchRequest {
	request.InboundContext = cloneInboundContext(request.InboundContext)
	request.RouteResult = cloneResolvedRoute(request.RouteResult)
	request.SessionScope = session.CloneScope(request.SessionScope)
	request.Media = append([]string(nil), request.Media...)
	return request
}

func (r DispatchRequest) Channel() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.Channel
}

func (r DispatchRequest) ChatID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.ChatID
}

func (r DispatchRequest) MessageID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.MessageID
}

func (r DispatchRequest) ReplyToMessageID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.ReplyToMessageID
}

func (r DispatchRequest) ChatType() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.ChatType
}

func (r DispatchRequest) SenderID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.SenderID
}

func normalizeTurnSpecInPlace(opts *turnSpec) {
	if opts == nil {
		return
	}
	*opts = normalizeTurnSpec(*opts)
}

func normalizeTurnSpec(opts turnSpec) turnSpec {
	opts.ModelBinding.RouteSessionKey = strings.TrimSpace(opts.ModelBinding.RouteSessionKey)
	opts.Dispatch.RouteSessionKey = strings.TrimSpace(opts.Dispatch.RouteSessionKey)
	opts.Dispatch.SessionKey = strings.TrimSpace(opts.Dispatch.SessionKey)
	if opts.Dispatch.RouteSessionKey == "" {
		opts.Dispatch.RouteSessionKey = opts.ModelBinding.RouteSessionKey
	}
	if opts.Dispatch.BaseSessionKey == "" {
		opts.Dispatch.BaseSessionKey = opts.Dispatch.SessionKey
	}

	return opts
}

func inferChatTypeFromSessionScope(scope *session.SessionScope) string {
	if scope == nil || len(scope.Values) == 0 {
		return ""
	}
	chatValue := strings.TrimSpace(scope.Values["chat"])
	if chatValue == "" {
		return ""
	}
	chatType, _, ok := strings.Cut(chatValue, ":")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(chatType))
}
