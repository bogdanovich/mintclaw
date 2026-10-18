package companion

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestTerminalWorkerReadinessAcceptsAckAndOutputInEitherOrder(t *testing.T) {
	for _, outputFirst := range []bool{true, false} {
		name := "ack-first"
		if outputFirst {
			name = "output-first"
		}
		t.Run(name, func(t *testing.T) {
			events := make(chan TerminalBrokerEvent, 2)
			output := TerminalBrokerEvent{
				Type: TerminalEventOutput, DataBase64: base64.StdEncoding.EncodeToString([]byte("ready-marker")),
			}
			ack := TerminalBrokerEvent{Type: TerminalEventAck, AcceptedSequence: 1}
			if outputFirst {
				events <- output
				events <- ack
			} else {
				events <- ack
				events <- output
			}
			close(events)
			if err := observeTerminalAckAndOutput(t.Context(), events, 1, "ready-marker"); err != nil {
				t.Fatal("readiness observation discarded an event delivered before its counterpart")
			}
		})
	}
}

func TestTerminalWorkerReadinessRetainsFragmentedOutputAcrossAck(t *testing.T) {
	events := make(chan TerminalBrokerEvent, 3)
	events <- TerminalBrokerEvent{
		Type: TerminalEventOutput, DataBase64: base64.StdEncoding.EncodeToString([]byte("ready-")),
	}
	events <- TerminalBrokerEvent{Type: TerminalEventAck, AcceptedSequence: 1}
	events <- TerminalBrokerEvent{
		Type: TerminalEventOutput, DataBase64: base64.StdEncoding.EncodeToString([]byte("marker")),
	}
	close(events)
	if err := observeTerminalAckAndOutput(t.Context(), events, 1, "ready-marker"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalWorkerReadinessRequiresBothValidEvents(t *testing.T) {
	ack := TerminalBrokerEvent{Type: TerminalEventAck, AcceptedSequence: 1}
	output := TerminalBrokerEvent{
		Type: TerminalEventOutput, DataBase64: base64.StdEncoding.EncodeToString([]byte("ready-marker")),
	}
	for _, test := range []struct {
		name   string
		events []TerminalBrokerEvent
	}{
		{name: "closed"},
		{name: "ack-only", events: []TerminalBrokerEvent{ack}},
		{name: "output-only", events: []TerminalBrokerEvent{output}},
		{
			name: "wrong-sequence",
			events: []TerminalBrokerEvent{
				{Type: TerminalEventAck, AcceptedSequence: 2}, output,
			},
		},
		{
			name: "invalid-output",
			events: []TerminalBrokerEvent{
				ack, {Type: TerminalEventOutput, DataBase64: "not base64"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan TerminalBrokerEvent, len(test.events))
			for _, event := range test.events {
				events <- event
			}
			close(events)
			if err := observeTerminalAckAndOutput(t.Context(), events, 1, "ready-marker"); err == nil {
				t.Fatal("incomplete or invalid readiness was accepted")
			}
		})
	}
}

// Both direct workers and the Unix broker may publish readiness output before
// the input acknowledgement. One observer retains both parts until complete.
func observeTerminalReadiness(
	ctx context.Context,
	receive func(context.Context) (TerminalBrokerEvent, error),
	sequence uint64,
	marker string,
) error {
	acked := false
	var text strings.Builder
	for {
		event, err := receive(ctx)
		if err != nil {
			return err
		}
		if event.Type == TerminalEventAck && event.AcceptedSequence == sequence {
			acked = true
		}
		if event.Type == TerminalEventOutput {
			data, decodeErr := base64.StdEncoding.DecodeString(event.DataBase64)
			if decodeErr != nil {
				return decodeErr
			}
			text.Write(data)
		}
		if acked && strings.Contains(text.String(), marker) {
			return nil
		}
	}
}

func observeTerminalAckAndOutput(
	ctx context.Context, events <-chan TerminalBrokerEvent, sequence uint64, marker string,
) error {
	return observeTerminalReadiness(ctx, func(ctx context.Context) (TerminalBrokerEvent, error) {
		select {
		case event, ok := <-events:
			if !ok {
				return TerminalBrokerEvent{}, errors.New(
					"terminal events closed before acknowledgement and readiness output",
				)
			}
			return event, nil
		case <-ctx.Done():
			return TerminalBrokerEvent{}, ctx.Err()
		}
	}, sequence, marker)
}
