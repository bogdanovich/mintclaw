package devices

import (
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/devices/events"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

func TestParseLastChannel(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantPlatform string
		wantUserID   string
		wantOK       bool
	}{
		{name: "canonical", in: "telegram:123456", wantPlatform: "telegram", wantUserID: "123456", wantOK: true},
		{
			name:         "matrix user id with colons",
			in:           "matrix:@alice:example.org",
			wantPlatform: "matrix",
			wantUserID:   "@alice:example.org",
			wantOK:       true,
		},
		{name: "empty", in: "", wantOK: false},
		{name: "whitespace", in: "   ", wantOK: false},
		{name: "no colon", in: "telegram123456", wantOK: false},
		{name: "leading colon", in: ":123456", wantOK: false},
		{name: "trailing colon", in: "telegram:", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform, userID := parseLastChannel(tt.in)
			if platform != tt.wantPlatform || userID != tt.wantUserID {
				t.Errorf(
					"parseLastChannel(%q) = (%q, %q), want (%q, %q)",
					tt.in,
					platform,
					userID,
					tt.wantPlatform,
					tt.wantUserID,
				)
			}
		})
	}
}

func newTestService(t *testing.T, msgBus *bus.MessageBus, lastChannel string) *Service {
	t.Helper()
	st := state.NewManager(t.TempDir())
	if lastChannel != "" {
		if err := st.SetLastChannel(lastChannel); err != nil {
			t.Fatalf("SetLastChannel: %v", err)
		}
	}
	s := &Service{state: st, bus: msgBus, enabled: true, sources: []events.EventSource{}}
	return s
}

func TestSendNotificationWithoutBusDoesNotPanic(t *testing.T) {
	s := newTestService(t, nil, "telegram:123456")
	s.sendNotification(
		&events.DeviceEvent{Action: events.ActionAdd, Kind: events.KindUSB, Vendor: "Acme", Product: "Widget"},
	)
}

func TestSendNotificationSkipsWithoutLastChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	s := newTestService(t, mb, "")
	ev := &events.DeviceEvent{Action: events.ActionAdd, Kind: events.KindUSB, Vendor: "Acme", Product: "Widget"}
	s.sendNotification(ev)
	select {
	case msg := <-mb.OutboundChan():
		t.Fatalf("unexpected outbound message: %+v", msg)
	default:
	}
}

func TestSendNotificationSkipsInternalChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	s := newTestService(t, mb, "cli:1")
	s.sendNotification(
		&events.DeviceEvent{Action: events.ActionAdd, Kind: events.KindUSB, Vendor: "Acme", Product: "Widget"},
	)
	select {
	case msg := <-mb.OutboundChan():
		t.Fatalf("unexpected outbound message to internal channel: %+v", msg)
	default:
	}
}

func TestSendNotificationPublishesToLastChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	s := newTestService(t, mb, "telegram:123456")
	ev := &events.DeviceEvent{
		Action:       events.ActionAdd,
		Kind:         events.KindUSB,
		Vendor:       "Acme",
		Product:      "Widget",
		Capabilities: "read",
	}

	done := make(chan struct{})
	var got *bus.OutboundMessage
	go func() {
		defer close(done)
		select {
		case msg := <-mb.OutboundChan():
			got = &msg
		case <-time.After(3 * time.Second):
		}
	}()

	s.sendNotification(ev)
	<-done

	if got == nil {
		t.Fatal("expected an outbound message")
	}
	if !strings.Contains(got.Content, ev.FormatMessage()) {
		t.Errorf("outbound content = %q, want device message", got.Content)
	}
	if got.Context.Channel != "telegram" || got.Context.ChatID != "123456" {
		t.Errorf("outbound context = %+v, want telegram/123456", got.Context)
	}
}
