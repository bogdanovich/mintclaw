package interactions

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (r *Registry) Create(req CreateRequest) (Record, error) {
	if r == nil {
		return Record{}, ErrStoreUnavailable
	}
	now := r.nowMillis()
	rec, err := r.buildRecord(req, now)
	if err != nil {
		return Record{}, err
	}

	r.mu.Lock()
	if availableErr := r.availableLocked(); availableErr != nil {
		r.mu.Unlock()
		return Record{}, availableErr
	}
	releaseStore, err := r.lockAndReloadLocked()
	if err != nil {
		r.mu.Unlock()
		return Record{}, err
	}
	if len(r.records) >= r.options.MaxRecords {
		releaseStore()
		r.mu.Unlock()
		return Record{}, fmt.Errorf("%w: max records %d", ErrCapacityExceeded, r.options.MaxRecords)
	}
	eventsBefore := append([]Event(nil), r.events...)
	commitSequenceBefore := r.commitSequence
	var supersededID string
	var supersededBefore Record
	for id, existing := range r.records {
		if !isTerminal(existing.Status) && existing.Route.SessionKey == rec.Route.SessionKey {
			if canChainInteraction(existing, rec) {
				supersededID = id
				supersededBefore = existing
				from := existing.Status
				existing.Status = StatusResolved
				existing.Revision++
				existing.UpdatedAt = now
				existing.ResolvedAt = now
				existing.CleanupAfter = now + r.options.TerminalRetention.Milliseconds()
				r.appendEventFromLocked(
					&existing, EventResolved, from, "continued_with_next_interaction", nil,
				)
				r.records[id] = existing
				continue
			}
			releaseStore()
			r.mu.Unlock()
			return Record{}, fmt.Errorf("%w: %s", ErrSessionHasActive, existing.ShortID)
		}
		if !isTerminal(existing.Status) && existing.ShortID == rec.ShortID {
			releaseStore()
			r.mu.Unlock()
			return Record{}, fmt.Errorf("%w: duplicate short id", ErrConflict)
		}
	}
	if _, exists := r.records[rec.ID]; exists {
		releaseStore()
		r.mu.Unlock()
		return Record{}, fmt.Errorf("%w: duplicate id", ErrConflict)
	}
	r.appendEventLocked(&rec, EventCreated, "", nil)
	r.records[rec.ID] = rec
	events := append([]Event(nil), r.events[len(eventsBefore):]...)
	r.trimEventsLocked()
	if err := r.saveLocked(); err != nil {
		delete(r.records, rec.ID)
		if supersededID != "" {
			r.records[supersededID] = supersededBefore
		}
		r.events = eventsBefore
		r.commitSequence = commitSequenceBefore
		releaseStore()
		r.mu.Unlock()
		return Record{}, err
	}
	drainNotifications := r.queueNotificationsLocked(events)
	releaseStore()
	r.mu.Unlock()
	if drainNotifications {
		r.drainNotifications()
	}
	return cloneRecord(rec), nil
}

func canChainInteraction(existing, next Record) bool {
	return existing.Status == StatusResuming &&
		existing.Origin.TaskID == next.Origin.TaskID &&
		existing.Origin.ContinuationSessionKey != "" &&
		existing.Origin.ContinuationSessionKey == next.Origin.ContinuationSessionKey &&
		existing.Route == next.Route
}

func (r *Registry) MarkWaiting(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, _ int64) (EventType, string, *bool, error) {
			if !validTransition(rec.Status, StatusWaiting) {
				return "", "", nil, fmt.Errorf(
					"%w: %s -> %s",
					ErrInvalidTransition,
					rec.Status,
					StatusWaiting,
				)
			}
			if rec.DeliveryTries == 0 || rec.DeliveryError != "" || !rec.PromptDelivered {
				return "", "", nil, fmt.Errorf(
					"%w: prompt delivery has not succeeded",
					ErrInvalidTransition,
				)
			}
			rec.Status = StatusWaiting
			return EventWaiting, "", nil, nil
		},
	)
}

func (r *Registry) RecordDeliveryAttempt(
	id string,
	expectedRevision int64,
	success bool,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusCreated || rec.DeliveryTries >= MaxDeliveryAttempts {
				return "", "", nil, fmt.Errorf(
					"%w: delivery from %s",
					ErrInvalidTransition,
					rec.Status,
				)
			}
			rec.DeliveryTries++
			rec.LastDeliveryAt = now
			if success {
				rec.PromptDelivered = true
				rec.PromptDeliveryState = DeliveryStateDelivered
				rec.DeliveryError = ""
			} else {
				rec.DeliveryError = bounded(detail, MaxSummaryLength)
			}
			return EventDeliveryAttempt, "", &success, nil
		},
	)
}

func (r *Registry) BeginPromptDelivery(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusCreated ||
				(rec.PromptDeliveryState != "" && rec.PromptDeliveryState != DeliveryStateNotSent) ||
				rec.PromptDelivered || rec.DeliveryTries >= MaxDeliveryAttempts {
				return "", "", nil, fmt.Errorf(
					"%w: begin prompt delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.PromptDeliveryState,
				)
			}
			rec.DeliveryTries++
			rec.LastDeliveryAt = now
			rec.DeliveryError = ""
			rec.PromptDeliveryState = DeliveryStateSending
			return EventDeliveryAttempt, "delivery_started", nil, nil
		},
	)
}

func (r *Registry) CompletePromptDelivery(
	id string,
	expectedRevision int64,
	success bool,
	ambiguous bool,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusCreated || rec.PromptDeliveryState != DeliveryStateSending {
				return "", "", nil, fmt.Errorf(
					"%w: complete prompt delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.PromptDeliveryState,
				)
			}
			rec.LastDeliveryAt = now
			if success {
				rec.PromptDelivered = true
				rec.PromptDeliveryState = DeliveryStateDelivered
				rec.DeliveryError = ""
			} else if ambiguous {
				rec.PromptDeliveryState = DeliveryStateAmbiguous
				rec.DeliveryError = bounded(detail, MaxSummaryLength)
			} else {
				rec.PromptDeliveryState = DeliveryStateNotSent
				rec.DeliveryError = bounded(detail, MaxSummaryLength)
			}
			return EventDeliveryAttempt, "delivery_completed", &success, nil
		},
	)
}

func (r *Registry) RecordFinalDeliveryAttempt(
	id string,
	expectedRevision int64,
	success bool,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusResuming || rec.FinalDeliveryTries >= MaxDeliveryAttempts {
				return "", "", nil, fmt.Errorf(
					"%w: final delivery from %s", ErrInvalidTransition, rec.Status,
				)
			}
			rec.FinalDeliveryTries++
			rec.LastFinalDeliveryAt = now
			if success {
				rec.FinalDelivered = true
				rec.FinalDeliveryState = DeliveryStateDelivered
				rec.FinalDeliveryError = ""
			} else {
				rec.FinalDeliveryError = bounded(detail, MaxSummaryLength)
			}
			return EventFinalDelivery, "", &success, nil
		},
	)
}

func (r *Registry) BeginFinalDelivery(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusResuming ||
				(rec.FinalDeliveryState != "" && rec.FinalDeliveryState != DeliveryStateNotSent) ||
				rec.FinalDelivered || rec.FinalDeliveryTries >= MaxDeliveryAttempts {
				return "", "", nil, fmt.Errorf(
					"%w: begin final delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.FinalDeliveryState,
				)
			}
			rec.LastFinalDeliveryAt = now
			rec.FinalDeliveryError = ""
			rec.FinalDeliveryState = DeliveryStateNotSent
			return EventFinalDelivery, "delivery_prepared", nil, nil
		},
	)
}

// StartFinalDelivery crosses the durable no-replay boundary immediately before
// an external delivery attempt. The preceding definitely-not-sent state remains
// recoverable after a crash because no channel or parent delivery has started.
func (r *Registry) StartFinalDelivery(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusResuming ||
				rec.FinalDeliveryState != DeliveryStateNotSent ||
				rec.FinalDelivered || rec.FinalDeliveryTries >= MaxDeliveryAttempts {
				return "", "", nil, fmt.Errorf(
					"%w: start final delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.FinalDeliveryState,
				)
			}
			rec.FinalDeliveryTries++
			rec.LastFinalDeliveryAt = now
			rec.FinalDeliveryError = ""
			rec.FinalDeliveryState = DeliveryStateSending
			return EventFinalDelivery, "delivery_started", nil, nil
		},
	)
}

func (r *Registry) CompleteFinalDelivery(
	id string,
	expectedRevision int64,
	success bool,
	ambiguous bool,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusResuming || rec.FinalDeliveryState != DeliveryStateSending {
				return "", "", nil, fmt.Errorf(
					"%w: complete final delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.FinalDeliveryState,
				)
			}
			rec.LastFinalDeliveryAt = now
			if success {
				rec.FinalDelivered = true
				rec.FinalDeliveryState = DeliveryStateDelivered
				rec.FinalDeliveryError = ""
			} else if ambiguous {
				rec.FinalDeliveryState = DeliveryStateAmbiguous
				rec.FinalDeliveryError = bounded(detail, MaxSummaryLength)
			} else {
				rec.FinalDeliveryState = DeliveryStateNotSent
				rec.FinalDeliveryError = bounded(detail, MaxSummaryLength)
			}
			return EventFinalDelivery, "delivery_completed", &success, nil
		},
	)
}

func (r *Registry) ClaimDeliveryUnknown(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusCreated ||
				(rec.PromptDeliveryState != DeliveryStateSending &&
					rec.PromptDeliveryState != DeliveryStateAmbiguous) {
				return "", "", nil, fmt.Errorf(
					"%w: claim unknown delivery from %s/%s",
					ErrInvalidTransition,
					rec.Status,
					rec.PromptDeliveryState,
				)
			}
			rec.Status = StatusClaimed
			rec.Outcome = OutcomeDeliveryUnknown
			rec.Answer = &Answer{ReceivedAt: now}
			return EventAnswerClaimed, "prompt_delivery_ambiguous", nil, nil
		},
	)
}

func (r *Registry) ClaimAnswer(
	id string,
	expectedRevision int64,
	answer Answer,
	outcome Outcome,
) (Record, error) {
	if outcome != OutcomeAnswered && outcome != OutcomeAllowed && outcome != OutcomeDenied {
		return Record{}, fmt.Errorf("%w: invalid answer outcome %q", ErrInvalidInteraction, outcome)
	}
	return r.claim(id, expectedRevision, answer, outcome)
}

func (r *Registry) ClaimOverdue(now time.Time) ([]Record, error) {
	if r == nil {
		return nil, ErrStoreUnavailable
	}
	nowMillis := now.UnixMilli()
	if now.IsZero() {
		nowMillis = r.nowMillis()
	}

	r.mu.Lock()
	if err := r.availableLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	releaseStore, err := r.lockAndReloadLocked()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	eventsBefore := append([]Event(nil), r.events...)
	commitSequenceBefore := r.commitSequence
	before := make(map[string]Record)
	claimed := make([]Record, 0)
	emitted := make([]Event, 0)
	for id, rec := range r.records {
		if (rec.Status != StatusCreated && rec.Status != StatusWaiting) ||
			rec.ExpiresAt <= 0 || rec.ExpiresAt > nowMillis {
			continue
		}
		before[id] = rec
		rec.Status = StatusClaimed
		rec.Outcome = OutcomeTimedOut
		rec.Answer = &Answer{ReceivedAt: nowMillis}
		rec.Revision++
		rec.UpdatedAt = nowMillis
		r.appendEventLocked(&rec, EventAnswerClaimed, "timeout", nil)
		r.records[id] = rec
		emitted = append(emitted, r.events[len(r.events)-1])
		claimed = append(claimed, cloneRecord(rec))
	}
	if len(claimed) == 0 {
		releaseStore()
		r.mu.Unlock()
		return nil, nil
	}
	r.trimEventsLocked()
	if err := r.saveLocked(); err != nil {
		for id, rec := range before {
			r.records[id] = rec
		}
		r.events = eventsBefore
		r.commitSequence = commitSequenceBefore
		releaseStore()
		r.mu.Unlock()
		return nil, err
	}
	drainNotifications := r.queueNotificationsLocked(emitted)
	releaseStore()
	r.mu.Unlock()
	if drainNotifications {
		r.drainNotifications()
	}
	slices.SortFunc(claimed, func(a, b Record) int { return cmp.Compare(a.ID, b.ID) })
	return claimed, nil
}

func (r *Registry) MarkResuming(id string, expectedRevision int64) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if !validTransition(rec.Status, StatusResuming) {
				return "", "", nil, fmt.Errorf(
					"%w: %s -> %s",
					ErrInvalidTransition,
					rec.Status,
					StatusResuming,
				)
			}
			rec.Status = StatusResuming
			rec.ResumeTries++
			rec.LastResumeAt = now
			rec.ResumeError = ""
			return EventResumeStarted, "", nil, nil
		},
	)
}

func (r *Registry) RecordResumeFailure(
	id string,
	expectedRevision int64,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusClaimed && rec.Status != StatusResuming {
				return "", "", nil, fmt.Errorf(
					"%w: resume failure from %s",
					ErrInvalidTransition,
					rec.Status,
				)
			}
			rec.ResumeError = bounded(detail, MaxSummaryLength)
			rec.LastResumeAt = now
			return EventRecoveryObserved, "resume_failed", nil, nil
		},
	)
}

func (r *Registry) Resolve(id string, expectedRevision int64) (Record, error) {
	return r.transition(id, expectedRevision, StatusResolved, EventResolved, "", nil)
}

// ConsumeApproval atomically spends an allow-once decision before the
// protected tool executes. A consumed approval is never executable again,
// including after a crash with an uncertain tool outcome.
func (r *Registry) ConsumeApproval(
	id string,
	expectedRevision int64,
	toolCallID string,
	toolName string,
	argumentHash string,
) (Record, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	toolName = strings.TrimSpace(toolName)
	argumentHash = strings.TrimSpace(argumentHash)
	record, err := r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Kind != KindApproval || rec.Status != StatusResuming ||
				rec.Outcome != OutcomeAllowed || rec.ApprovalConsumedAt != 0 ||
				rec.Origin.ToolCallID != toolCallID || rec.Origin.ToolName != toolName ||
				rec.Origin.ArgumentHash == "" || rec.Origin.ArgumentHash != argumentHash {
				return "", "", nil, fmt.Errorf("%w: approval does not match pending tool call", ErrInvalidTransition)
			}
			if rec.ExpiresAt > 0 && now >= rec.ExpiresAt {
				rec.Outcome = OutcomeTimedOut
				return EventApprovalExpired, "timeout_at_approval_consumption", nil, nil
			}
			rec.ApprovalConsumedAt = now
			return EventApprovalConsumed, "allow_once_consumed", nil, nil
		},
	)
	if err != nil {
		return Record{}, err
	}
	if record.Outcome == OutcomeTimedOut && record.ApprovalConsumedAt == 0 {
		return record, ErrApprovalExpired
	}
	return record, nil
}

func (r *Registry) BeginCancellation(
	id string,
	expectedRevision int64,
	code string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, _ int64) (EventType, string, *bool, error) {
			if !validTransition(rec.Status, StatusCanceling) {
				return "", "", nil, fmt.Errorf(
					"%w: %s -> %s",
					ErrInvalidTransition,
					rec.Status,
					StatusCanceling,
				)
			}
			rec.Status = StatusCanceling
			rec.FailureCode = bounded(code, 128)
			return EventCanceling, rec.FailureCode, nil, nil
		},
	)
}

func (r *Registry) CompleteCancellation(id string, expectedRevision int64) (Record, error) {
	return r.transition(
		id,
		expectedRevision,
		StatusCancelled,
		EventCancelled,
		"cancellation_completed",
		nil,
	)
}

func (r *Registry) Cancel(id string, expectedRevision int64, code string) (Record, error) {
	return r.transition(id, expectedRevision, StatusCancelled, EventCancelled, code, nil)
}

func (r *Registry) Fail(
	id string,
	expectedRevision int64,
	code string,
	detail string,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, _ int64) (EventType, string, *bool, error) {
			if !validTransition(rec.Status, StatusFailed) {
				return "", "", nil, fmt.Errorf(
					"%w: %s -> %s",
					ErrInvalidTransition,
					rec.Status,
					StatusFailed,
				)
			}
			rec.Status = StatusFailed
			rec.FailureCode = bounded(code, 128)
			rec.FailureDetail = bounded(detail, MaxSummaryLength)
			return EventFailed, rec.FailureCode, nil, nil
		},
	)
}

func (r *Registry) claim(
	id string,
	expectedRevision int64,
	answer Answer,
	outcome Outcome,
) (Record, error) {
	if !validBoundedString(answer.Text, MaxAnswerLength) || len(answer.Values) > MaxQuestions {
		return Record{}, fmt.Errorf("%w: answer exceeds bounds", ErrInvalidInteraction)
	}
	answer.Text = strings.TrimSpace(answer.Text)
	answer.Values = cloneStringMap(answer.Values)
	for key, value := range answer.Values {
		if !questionIDPattern.MatchString(key) || !validBoundedString(value, MaxAnswerLength) {
			return Record{}, fmt.Errorf("%w: invalid answer value %q", ErrInvalidInteraction, key)
		}
		answer.Values[key] = strings.TrimSpace(value)
	}
	answer.MessageID = strings.TrimSpace(answer.MessageID)
	answer.ResponseMessageID = strings.TrimSpace(answer.ResponseMessageID)
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, now int64) (EventType, string, *bool, error) {
			if rec.Status != StatusWaiting {
				return "", "", nil, fmt.Errorf("%w: status %s", ErrAnswerTooLate, rec.Status)
			}
			if rec.ExpiresAt > 0 && now >= rec.ExpiresAt {
				rec.Status = StatusClaimed
				rec.Outcome = OutcomeTimedOut
				rec.Answer = &Answer{
					MessageID:         answer.MessageID,
					ResponseMessageID: answer.ResponseMessageID,
					ReceivedAt:        now,
				}
				return EventAnswerClaimed, "timeout_at_answer_claim", nil, nil
			}
			if rec.Kind == KindQuestion && outcome != OutcomeAnswered {
				return "", "", nil, fmt.Errorf(
					"%w: question outcome %q",
					ErrInvalidInteraction,
					outcome,
				)
			}
			if rec.Kind == KindApproval && outcome != OutcomeAllowed && outcome != OutcomeDenied {
				return "", "", nil, fmt.Errorf(
					"%w: approval outcome %q",
					ErrInvalidInteraction,
					outcome,
				)
			}
			if rec.Kind == KindQuestion {
				known := make(map[string]struct{}, len(rec.Questions))
				for _, question := range rec.Questions {
					known[question.ID] = struct{}{}
				}
				for key := range answer.Values {
					if _, ok := known[key]; !ok {
						return "", "", nil, fmt.Errorf(
							"%w: unknown question id %q",
							ErrInvalidInteraction,
							key,
						)
					}
				}
			}
			if answer.MessageID != "" {
				identity := scopedAnswerMessageIdentity(rec.Route, answer.MessageID)
				for _, other := range r.records {
					if other.Answer != nil &&
						scopedAnswerMessageIdentity(other.Route, other.Answer.MessageID) == identity {
						return "", "", nil, fmt.Errorf(
							"%w: %s",
							ErrDuplicateAnswer,
							answer.MessageID,
						)
					}
				}
			}
			if answer.ReceivedAt == 0 {
				answer.ReceivedAt = now
			}
			rec.Status = StatusClaimed
			rec.Outcome = outcome
			rec.Answer = &answer
			return EventAnswerClaimed, "", nil, nil
		},
	)
}

func (r *Registry) transition(
	id string,
	expectedRevision int64,
	to Status,
	eventType EventType,
	code string,
	success *bool,
) (Record, error) {
	return r.update(
		id,
		expectedRevision,
		func(rec *Record, _ int64) (EventType, string, *bool, error) {
			if !validTransition(rec.Status, to) {
				return "", "", nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, rec.Status, to)
			}
			rec.Status = to
			return eventType, bounded(code, 128), success, nil
		},
	)
}

func (r *Registry) update(
	id string,
	expectedRevision int64,
	mutate func(*Record, int64) (EventType, string, *bool, error),
) (Record, error) {
	if r == nil {
		return Record{}, ErrStoreUnavailable
	}
	id = strings.TrimSpace(id)
	r.mu.Lock()
	if err := r.availableLocked(); err != nil {
		r.mu.Unlock()
		return Record{}, err
	}
	releaseStore, err := r.lockAndReloadLocked()
	if err != nil {
		r.mu.Unlock()
		return Record{}, err
	}
	rec, ok := r.records[id]
	if !ok {
		releaseStore()
		r.mu.Unlock()
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if rec.Revision != expectedRevision {
		releaseStore()
		r.mu.Unlock()
		return Record{}, fmt.Errorf(
			"%w: have %d, want %d",
			ErrConflict,
			rec.Revision,
			expectedRevision,
		)
	}
	before := rec
	eventsBefore := append([]Event(nil), r.events...)
	commitSequenceBefore := r.commitSequence
	now := r.nowMillis()
	eventType, code, success, err := mutate(&rec, now)
	if err != nil {
		releaseStore()
		r.mu.Unlock()
		return Record{}, err
	}
	from := before.Status
	rec.Revision++
	rec.UpdatedAt = now
	if isTerminal(rec.Status) {
		rec.ResolvedAt = now
		rec.CleanupAfter = now + r.options.TerminalRetention.Milliseconds()
	}
	r.appendEventFromLocked(&rec, eventType, from, code, success)
	r.records[id] = rec
	event := r.events[len(r.events)-1]
	r.trimEventsLocked()
	if err := r.saveLocked(); err != nil {
		r.records[id] = before
		r.events = eventsBefore
		r.commitSequence = commitSequenceBefore
		releaseStore()
		r.mu.Unlock()
		return Record{}, err
	}
	drainNotifications := r.queueNotificationsLocked([]Event{event})
	releaseStore()
	r.mu.Unlock()
	if drainNotifications {
		r.drainNotifications()
	}
	return cloneRecord(rec), nil
}

func (r *Registry) buildRecord(req CreateRequest, now int64) (Record, error) {
	if req.Kind != KindQuestion && req.Kind != KindApproval {
		return Record{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidInteraction, req.Kind)
	}
	if err := req.Route.validate(); err != nil {
		return Record{}, err
	}
	if err := req.Origin.validate(); err != nil {
		return Record{}, err
	}
	if !validArgumentHashForKind(req.Kind, req.Origin.ArgumentHash) {
		return Record{}, fmt.Errorf("%w: approval requires a canonical argument hash", ErrInvalidInteraction)
	}
	if err := validateInteractionCreateMetadata(req.Kind, req.Origin.ExecutionContext, req.ApprovalAction); err != nil {
		return Record{}, err
	}
	if err := validateQuestions(req.Kind, req.Questions); err != nil {
		return Record{}, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		var err error
		id, err = randomID()
		if err != nil {
			return Record{}, err
		}
	}
	if len(id) < 8 || len(id) > 128 || !regexpID.MatchString(id) {
		return Record{}, fmt.Errorf("%w: id must be 8 to 128 characters", ErrInvalidInteraction)
	}
	expiresAt := req.ExpiresAt.UnixMilli()
	if req.ExpiresAt.IsZero() || expiresAt <= now {
		return Record{}, fmt.Errorf("%w: expiry must be in the future", ErrInvalidInteraction)
	}
	return Record{
		ID:             id,
		ShortID:        shortID(id),
		Kind:           req.Kind,
		Status:         StatusCreated,
		Revision:       1,
		Route:          normalizeRoute(req.Route),
		Origin:         normalizeOrigin(req.Origin),
		Questions:      cloneQuestions(req.Questions),
		PromptSummary:  bounded(strings.TrimSpace(req.PromptSummary), MaxSummaryLength),
		ApprovalAction: bounded(strings.TrimSpace(req.ApprovalAction), MaxApprovalAction),
		CreatedAt:      now,
		UpdatedAt:      now,
		ExpiresAt:      expiresAt,
	}, nil
}
