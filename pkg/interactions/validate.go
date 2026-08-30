package interactions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type answerMessageIdentity struct {
	Channel   string
	AccountID string
	ChatID    string
	TopicID   string
	SpaceID   string
	MessageID string
}

func scopedAnswerMessageIdentity(route Route, messageID string) answerMessageIdentity {
	return answerMessageIdentity{
		Channel:   route.Channel,
		AccountID: route.AccountID,
		ChatID:    route.ChatID,
		TopicID:   route.TopicID,
		SpaceID:   route.SpaceID,
		MessageID: strings.TrimSpace(messageID),
	}
}

func validateStoredRecord(rec Record) error {
	if strings.TrimSpace(rec.ID) == "" || !regexpID.MatchString(rec.ID) ||
		rec.ShortID != shortID(rec.ID) || rec.Revision <= 0 || rec.LastEventSeq <= 0 ||
		rec.CreatedAt <= 0 || rec.UpdatedAt < rec.CreatedAt || rec.ExpiresAt <= rec.CreatedAt {
		return fmt.Errorf("invalid interaction record %q", rec.ID)
	}
	if rec.Kind != KindQuestion && rec.Kind != KindApproval {
		return fmt.Errorf("invalid interaction kind %q", rec.Kind)
	}
	switch rec.Status {
	case StatusCreated,
		StatusWaiting,
		StatusClaimed,
		StatusResuming,
		StatusCanceling,
		StatusResolved,
		StatusCancelled,
		StatusFailed:
	default:
		return fmt.Errorf("invalid interaction status %q", rec.Status)
	}
	if err := rec.Route.validate(); err != nil {
		return err
	}
	if err := rec.Origin.validate(); err != nil {
		return err
	}
	if !validStoredArgumentHashForKind(rec.Kind, rec.Origin.ArgumentHash) {
		return fmt.Errorf("invalid argument hash for interaction %q", rec.ID)
	}
	if err := validateStoredInteractionMetadata(
		rec.Kind, rec.Origin.ExecutionContext, rec.ApprovalAction,
	); err != nil {
		return fmt.Errorf("invalid approval metadata for interaction %q: %w", rec.ID, err)
	}
	if err := validateQuestions(rec.Kind, rec.Questions); err != nil {
		return err
	}
	if len(rec.FinalDeliveryIDs) > MaxFinalDeliveries {
		return fmt.Errorf("too many final deliveries for interaction %q", rec.ID)
	}
	seenFinalDeliveries := make(map[string]struct{}, len(rec.FinalDeliveryIDs))
	for _, deliveryID := range rec.FinalDeliveryIDs {
		deliveryID = strings.TrimSpace(deliveryID)
		if deliveryID == "" {
			return fmt.Errorf("invalid final delivery for interaction %q", rec.ID)
		}
		if _, exists := seenFinalDeliveries[deliveryID]; exists {
			return fmt.Errorf("duplicate final delivery for interaction %q", rec.ID)
		}
		seenFinalDeliveries[deliveryID] = struct{}{}
	}
	switch rec.Status {
	case StatusCreated:
		if rec.Answer != nil || rec.Outcome != "" {
			return fmt.Errorf("invalid created interaction %q", rec.ID)
		}
	case StatusWaiting:
		if rec.Answer != nil || rec.Outcome != "" || strings.TrimSpace(rec.PromptDeliveryID) == "" {
			return fmt.Errorf("invalid waiting interaction %q", rec.ID)
		}
	case StatusClaimed, StatusResuming, StatusResolved:
		if rec.Answer == nil || rec.Answer.ReceivedAt <= 0 ||
			!validStoredOutcome(rec.Kind, rec.Outcome) {
			return fmt.Errorf("invalid answered interaction %q", rec.ID)
		}
		if err := validateStoredAnswer(rec); err != nil {
			return err
		}
		if (rec.Status == StatusResuming || rec.Status == StatusResolved) && rec.ResumeTries == 0 {
			return fmt.Errorf("invalid resuming interaction %q", rec.ID)
		}
	}
	if isTerminal(rec.Status) && (rec.ResolvedAt <= 0 || rec.CleanupAfter <= rec.ResolvedAt) {
		return fmt.Errorf("invalid terminal interaction %q", rec.ID)
	}
	if rec.ApprovalConsumedAt != 0 && (rec.Kind != KindApproval ||
		(rec.Outcome != OutcomeAllowed && rec.Outcome != OutcomeDeliveryUnknown) || rec.Status == StatusCreated ||
		rec.Status == StatusWaiting || rec.Status == StatusClaimed) {
		return fmt.Errorf("invalid consumed approval %q", rec.ID)
	}
	return nil
}

func validateStoredAnswer(rec Record) error {
	answer := rec.Answer
	if answer == nil || !validBoundedString(answer.Text, MaxAnswerLength) ||
		len(answer.Values) > MaxQuestions || len(answer.Media) > MaxAnswerMedia {
		return fmt.Errorf("invalid stored answer for interaction %q", rec.ID)
	}
	for _, ref := range answer.Media {
		if strings.TrimSpace(ref) == "" || !validBoundedString(ref, MaxAnswerMediaRefLength) {
			return fmt.Errorf("invalid stored answer media for interaction %q", rec.ID)
		}
	}
	if answer.Superseded {
		if rec.Kind != KindApproval || rec.Outcome != OutcomeDenied || len(answer.Values) != 0 ||
			(strings.TrimSpace(answer.Text) == "" && len(answer.Media) == 0) {
			return fmt.Errorf("invalid superseding answer for interaction %q", rec.ID)
		}
	} else if len(answer.Media) != 0 {
		return fmt.Errorf("unexpected answer media for interaction %q", rec.ID)
	}
	return nil
}

func validStoredOutcome(kind Kind, outcome Outcome) bool {
	if outcome == OutcomeTimedOut || outcome == OutcomeDeliveryUnknown {
		return true
	}
	if kind == KindQuestion {
		return outcome == OutcomeAnswered
	}
	return outcome == OutcomeAllowed || outcome == OutcomeDenied
}

func validArgumentHashForKind(kind Kind, value string) bool {
	value = strings.TrimSpace(value)
	if kind != KindApproval {
		return value == ""
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validStoredArgumentHashForKind(kind Kind, value string) bool {
	if kind == KindApproval && strings.TrimSpace(value) == "" {
		// Obsolete approval records are inert: recovery cannot consume them
		// without an exact argument hash and immutable execution context.
		return true
	}
	return validArgumentHashForKind(kind, value)
}
