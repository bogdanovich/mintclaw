package events

import (
	"strings"
	"testing"
)

func TestFormatMessageAdd(t *testing.T) {
	ev := &DeviceEvent{
		Action:       ActionAdd,
		Kind:         KindUSB,
		DeviceID:     "1-2",
		Vendor:       "Acme",
		Product:      "Widget",
		Serial:       "SN-123",
		Capabilities: "read write",
	}
	msg := ev.FormatMessage()
	for _, want := range []string{"Connected", "Type: usb", "Device: Acme Widget", "Capabilities: read write", "Serial: SN-123"} {
		if !strings.Contains(msg, want) {
			t.Errorf("FormatMessage() missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Disconnected") {
		t.Errorf("FormatMessage() add event contains Disconnected:\n%s", msg)
	}
}

func TestFormatMessageRemove(t *testing.T) {
	ev := &DeviceEvent{Action: ActionRemove, Kind: KindBluetooth, Vendor: "Acme", Product: "Widget"}
	msg := ev.FormatMessage()
	for _, want := range []string{"Disconnected", "Type: bluetooth", "Device: Acme Widget"} {
		if !strings.Contains(msg, want) {
			t.Errorf("FormatMessage() missing %q in:\n%s", want, msg)
		}
	}
}

func TestFormatMessageChangeReportsConnected(t *testing.T) {
	ev := &DeviceEvent{Action: ActionChange, Kind: KindPCI, Vendor: "Acme", Product: "Widget"}
	if msg := ev.FormatMessage(); !strings.Contains(msg, "Connected") {
		t.Errorf("FormatMessage() change event should read Connected, got:\n%s", msg)
	}
}

func TestFormatMessageOmitsEmptyOptionalFields(t *testing.T) {
	ev := &DeviceEvent{Action: ActionAdd, Kind: KindGeneric, Vendor: "", Product: ""}
	msg := ev.FormatMessage()
	for _, unwanted := range []string{"Capabilities:", "Serial:"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("FormatMessage() contains %q for empty optional fields:\n%s", unwanted, msg)
		}
	}
}
