package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
)

func TestConnectServerToolDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name           string
		toolCount      int
		advertiseTools bool
		failPage       int32
		cancelPage     int32
		wantPages      int32
		wantFailure    bool
	}{
		{name: "first page failure", toolCount: 2, advertiseTools: true, failPage: 1, wantPages: 1, wantFailure: true},
		{name: "later page failure", toolCount: 3, advertiseTools: true, failPage: 2, wantPages: 2, wantFailure: true},
		{name: "cancellation", toolCount: 2, advertiseTools: true, cancelPage: 1, wantPages: 1, wantFailure: true},
		{name: "complete pagination", toolCount: 3, advertiseTools: true, wantPages: 3},
		{name: "empty catalog", advertiseTools: true, wantPages: 1},
		{name: "no tool capability"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			capabilities := &sdkmcp.ServerCapabilities{}
			if tt.advertiseTools {
				capabilities.Tools = &sdkmcp.ToolCapabilities{}
			}
			server := sdkmcp.NewServer(&sdkmcp.Implementation{
				Name: "discovery-test", Version: "1",
			}, &sdkmcp.ServerOptions{PageSize: 1, Capabilities: capabilities})
			for i := range tt.toolCount {
				sdkmcp.AddTool(server, &sdkmcp.Tool{
					Name: fmt.Sprintf("echo_%d", i), Description: "Discovery fixture",
				}, func(context.Context, *sdkmcp.CallToolRequest, map[string]any) (*sdkmcp.CallToolResult, any, error) {
					return &sdkmcp.CallToolResult{}, nil, nil
				})
			}
			var pages, deletes atomic.Int32
			server.AddReceivingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
				return func(requestCtx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
					if method == "tools/list" {
						page := pages.Add(1)
						if page == tt.failPage {
							return nil, errors.New("synthetic catalog failure")
						}
						if page == tt.cancelPage {
							cancel()
							return nil, context.Canceled
						}
					}
					return next(requestCtx, method, req)
				}
			})
			handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(host.Close)

			eventBus := runtimeevents.NewBus()
			t.Cleanup(func() { _ = eventBus.Close() })
			_, events, err := eventBus.Channel().OfKind(
				runtimeevents.KindMCPServerConnected,
				runtimeevents.KindMCPServerFailed,
				runtimeevents.KindMCPToolDiscovered,
			).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "discovery", Buffer: 8})
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(WithRuntimeEvents(eventBus))
			t.Cleanup(func() {
				if closeErr := manager.Close(); closeErr != nil {
					t.Errorf("Close() error = %v", closeErr)
				}
			})
			err = manager.ConnectServer(ctx, "discovery", config.MCPServerConfig{
				Enabled: true, Type: "http", URL: host.URL,
			})
			if tt.wantFailure {
				if err == nil {
					t.Fatal("ConnectServer() succeeded after failed tool discovery")
				}
				if tt.cancelPage != 0 {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("ConnectServer() error = %v, want cancellation cause", err)
					}
				} else if !strings.Contains(err.Error(), "synthetic catalog failure") {
					t.Fatalf("ConnectServer() error = %v, want discovery failure", err)
				}
				if len(manager.GetServers()) != 0 || len(manager.GetAllTools()) != 0 {
					t.Fatal("failed discovery published a server or partial tool catalog")
				}
				if event := receiveMCPRuntimeEvent(t, events); event.Kind != runtimeevents.KindMCPServerFailed {
					t.Fatalf("failed discovery published %q, want server failure", event.Kind)
				}
			} else {
				if err != nil {
					t.Fatalf("ConnectServer() error = %v", err)
				}
				connection, ok := manager.GetServer("discovery")
				if !ok || len(connection.Tools) != tt.toolCount {
					t.Fatalf("successful discovery = %#v, want %d tools", connection, tt.toolCount)
				}
				for range tt.toolCount {
					if event := receiveMCPRuntimeEvent(t, events); event.Kind != runtimeevents.KindMCPToolDiscovered {
						t.Fatalf("tool discovery event = %q", event.Kind)
					}
				}
				if event := receiveMCPRuntimeEvent(t, events); event.Kind != runtimeevents.KindMCPServerConnected {
					t.Fatalf("successful discovery published %q, want connected", event.Kind)
				}
			}
			if got := pages.Load(); got != tt.wantPages {
				t.Fatalf("tools/list requests = %d, want %d", got, tt.wantPages)
			}
			if err := manager.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if got := deletes.Load(); got != 1 {
				t.Fatalf("session DELETE requests = %d, want one cleanup", got)
			}
		})
	}
}
