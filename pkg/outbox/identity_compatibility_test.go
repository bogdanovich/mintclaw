package outbox

import "testing"

// These are persisted v3 delivery identities, not values derived from the
// current recordVersion. Changing a storage schema must not permit a new send.
func TestDeliveryIdentityV3GoldenKeys(t *testing.T) {
	cases := []struct {
		source  string
		ordinal int
		kind    Kind
		want    string
	}{
		{source: "turn-012345", ordinal: 0, kind: KindMessage, want: "out_167326932f56928e2e9180443e72a885"},
		{source: "turn-012345", ordinal: 1, kind: KindMedia, want: "out_5c1c1b9ebc4913b5e0a3f5b318b39157"},
		{source: "delivery/雪", ordinal: 42, kind: KindMessage, want: "out_e37b905b43267759894d8b85b77f8913"},
	}
	for _, tc := range cases {
		identity := Identity{
			SourceID: tc.source, Ordinal: tc.ordinal, Kind: tc.kind,
			Channel: "telegram", ChatID: "original", SessionKey: "session",
		}
		got, err := DeliveryID(identity)
		if err != nil || got != tc.want {
			t.Fatalf("persisted identity = %q, %v; want %q", got, err, tc.want)
		}
		// Route changes do not establish a new logical operation. Create must
		// continue to return the original canonical route/payload for this ID.
		identity.Channel, identity.ChatID, identity.SessionKey = "web", "other", "other-session"
		replay, err := DeliveryID(identity)
		if err != nil || replay != tc.want {
			t.Fatalf("replay identity = %q, %v; want %q", replay, err, tc.want)
		}
	}
}
