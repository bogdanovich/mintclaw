package outbox

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

const (
	retentionRecordVersion = 4
	retentionPlainMessage  = "plain_message"
	retentionSourcePending = "native_final_source_pending"
)

var errPayloadNotRetirable = errors.New("outbox payload is not eligible for retirement")

// RetiredPayloadError preserves known terminal truth when a replay attempts to
// introduce a domain callback after the original plain payload was retired.
// Its public error text contains no original body, route, path or candidate data.
type RetiredPayloadError struct{ Receipt Intent }

func (*RetiredPayloadError) Error() string {
	return "message was already delivered; another action requires a new request"
}

// PlainMessageRetirementAllowed excludes domain-bearing and intermediary
// payloads. This shape check is insufficient by itself: admission also needs
// a qualified native final owner, no callbacks, and a durable source seal.
// Native evaluation evidence and unqualified sources remain protected.
func PlainMessageRetirementAllowed(msg bus.OutboundMessage) bool {
	metadata := msg.Metadata
	return msg.ResultOutput == nil &&
		(metadata.MessageKind == "" || metadata.MessageKind == bus.OutboundMessageKindFinalReply) &&
		(metadata.OutboundKind == "" || metadata.OutboundKind == bus.OutboundKindFinal) &&
		len(metadata.ToolCalls) == 0 && metadata.InteractionKind == "" &&
		metadata.InteractionControls == "" && metadata.InteractionLanguage == "" &&
		metadata.InteractionID == "" && metadata.InteractionShortID == "" &&
		metadata.RequestID == "" && len(metadata.Choices) == 0 && len(metadata.Actions) == 0
}

func validateRetiredIntent(intent Intent) error {
	if !intent.PayloadRetired || intent.PayloadRetiredAt.IsZero() ||
		intent.PayloadRetiredAt.Before(intent.UpdatedAt) ||
		intent.RetentionClass != retentionPlainMessage || intent.Identity.Kind != KindMessage ||
		intent.Status != StatusDelivered || intent.Attempts < 1 ||
		intent.Message != nil || intent.Media != nil || intent.RecoverySettled ||
		intent.LastError != "" || !intent.RetryAfter.IsZero() {
		return errors.New("invalid outbox retired delivered receipt")
	}
	return nil
}

// retirePlainPayload is an offline primitive. The caller must exclusively own
// a fresh coordinator and expose no admission/transport work while it runs.
// One atomic replacement preserves the canonical filename through every crash
// boundary; the body is never removed separately from its no-replay receipt.
func (s *Store) retirePlainPayload(id string, before, now time.Time) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, err := s.read(id)
	if err != nil {
		return Intent{}, err
	}
	if !intent.PayloadRetired && payloadRetirementBlocker(intent, before, now) != "" {
		return Intent{}, errPayloadNotRetirable
	}
	retired := compactDeliveredIntent(intent, now)
	if err = validateIntent(retired); err != nil {
		return Intent{}, err
	}
	data, err := json.MarshalIndent(retired, "", "  ")
	if err != nil {
		return Intent{}, err
	}
	if len(data)+1 > maxRetiredReceiptBytes {
		return Intent{}, errPayloadNotRetirable
	}
	if err = s.write(retired); err != nil {
		return Intent{}, err
	}
	return retired, nil
}

// protectPayload monotonically revokes retirement before a caller can create
// a new domain obligation. The canonical body and delivery outcome stay intact.
func (s *Store) protectPayload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, err := s.read(id)
	if err != nil {
		return err
	}
	if intent.PayloadRetired {
		return &RetiredPayloadError{Receipt: intent}
	}
	if intent.RetentionClass == "" {
		return nil
	}
	intent.RetentionClass = ""
	intent.UpdatedAt = s.now().UTC()
	return s.write(intent)
}

// SealNativeFinalMessage advances only an already qualified native admission.
// A receipt proves exact instance/source removal, while admission proves the
// final publication owner. Neither can promote a legacy or revoked payload.
// Sealing does not imply delivery, transcript settlement, or eval completion.
func (c *Coordinator) SealNativeFinalMessage(id string, receipt bus.InboundAckReceipt) (bool, error) {
	if c == nil || c.store == nil {
		return false, errors.New("outbox coordinator is unavailable")
	}
	if err := validateID(id); err != nil {
		return false, err
	}
	if receipt == (bus.InboundAckReceipt{}) {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validateOpenLocked(); err != nil {
		return false, err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	intent, err := c.store.read(id)
	if err != nil {
		return false, err
	}
	if !receipt.Acknowledges(c.root, intent.Identity.SourceID) {
		return false, nil
	}
	if intent.RetentionClass == retentionPlainMessage {
		if err = c.store.write(intent); err != nil {
			return false, err
		}
		return true, nil
	}
	if intent.RetentionClass != retentionSourcePending || intent.PayloadRetired {
		return false, nil
	}
	intent.RetentionClass = retentionPlainMessage
	intent.UpdatedAt = c.store.now().UTC()
	if err = c.store.write(intent); err != nil {
		return false, err
	}
	return true, nil
}
