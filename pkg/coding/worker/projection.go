package worker

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

// QuestionSource is an optional controller capability. It exposes only one
// already-authorized, bounded question and never accepts an answer itself.
type QuestionSource interface {
	CodingWorkerQuestion(context.Context) (*QuestionState, error)
}

type projectedState struct {
	snapshot Snapshot
	question *QuestionState
}

type projectedEvent struct {
	name    EventName
	payload any
}

func projectControllerState(
	ctx context.Context,
	identity ControlIdentity,
	controller frontend.Controller,
	source frontend.ThreadSnapshot,
) (projectedState, error) {
	var question *QuestionState
	if questions, ok := controller.(QuestionSource); ok {
		current, err := questions.CodingWorkerQuestion(ctx)
		if err != nil {
			return projectedState{}, fmt.Errorf("read coding worker question: %w", err)
		}
		if current != nil {
			cloned := *current
			cloned.Options = slices.Clone(current.Options)
			if err := cloned.Validate(); err != nil {
				return projectedState{}, fmt.Errorf("project coding worker question: %w", err)
			}
			question = &cloned
		}
	}
	var pending *QuestionState
	if question != nil && question.Status == QuestionWaiting {
		pending = question
	}
	snapshot := SnapshotFromFrontend(source, pending)
	if err := validateSnapshot(identity, snapshot); err != nil {
		return projectedState{}, fmt.Errorf("project coding worker snapshot: %w", err)
	}
	return projectedState{snapshot: snapshot, question: question}, nil
}

func eventsBetween(identity ControlIdentity, previous, next projectedState) []projectedEvent {
	events := make([]projectedEvent, 0, len(next.snapshot.Items)+4)
	previousRevisions := make(map[string]uint64, len(previous.snapshot.Items))
	for _, item := range previous.snapshot.Items {
		previousRevisions[item.ID] = item.Revision
	}
	for _, item := range next.snapshot.Items {
		if revision, exists := previousRevisions[item.ID]; exists && revision >= item.Revision {
			continue
		}
		events = append(events, projectedEvent{
			name: EventItemUpdated,
			payload: ItemUpdatedPayload{
				ControlIdentity: identity,
				Item:            item,
			},
		})
	}
	if previous.snapshot.Activity != next.snapshot.Activity || previous.snapshot.Status != next.snapshot.Status {
		events = append(events, projectedEvent{
			name: EventStatusChanged,
			payload: StatusChangedPayload{
				ControlIdentity: identity,
				Activity:        next.snapshot.Activity,
				Status:          next.snapshot.Status,
			},
		})
	}
	question := changedQuestion(previous.question, next.question)
	if question != nil {
		events = append(events, projectedEvent{
			name: EventQuestionState,
			payload: QuestionStatePayload{
				ControlIdentity: identity,
				Question:        *question,
			},
		})
	}
	if previous.snapshot.ContextUsage != next.snapshot.ContextUsage {
		events = append(events, projectedEvent{
			name: EventContextUsage,
			payload: ContextUsagePayload{
				ControlIdentity: identity,
				Usage:           next.snapshot.ContextUsage,
			},
		})
	}
	if !reflect.DeepEqual(previous.snapshot.LastTurn, next.snapshot.LastTurn) && next.snapshot.LastTurn != nil {
		status := strings.TrimSpace(next.snapshot.Status)
		if status == "" {
			status = "turn settled"
		}
		events = append(events, projectedEvent{
			name: EventTurnTerminal,
			payload: TurnTerminalPayload{
				ControlIdentity: identity,
				TurnID:          next.snapshot.LastTurn.TurnID,
				Outcome:         next.snapshot.LastTurn.Outcome,
				Status:          status,
			},
		})
	}
	return events
}

func changedQuestion(previous, next *QuestionState) *QuestionState {
	if reflect.DeepEqual(previous, next) {
		return nil
	}
	if next != nil {
		cloned := *next
		cloned.Options = slices.Clone(next.Options)
		return &cloned
	}
	if previous == nil {
		return nil
	}
	canceled := *previous
	canceled.Options = slices.Clone(previous.Options)
	canceled.Status = QuestionCanceled
	return &canceled
}

func eventRecord(event projectedEvent) (Record, error) {
	payload, err := MarshalPayload(event.payload)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		SchemaVersion: ProtocolV1,
		Type:          RecordEvent,
		Event:         event.name,
		Payload:       payload,
	}
	if _, err := Encode(record); err != nil {
		return Record{}, err
	}
	return record, nil
}
