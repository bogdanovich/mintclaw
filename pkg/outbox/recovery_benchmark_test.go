package outbox

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
)

// BenchmarkRecoverTerminalHistory measures retained records that produce no
// recovery work. The first scan is process-local, not an OS-cold-cache claim.
func BenchmarkRecoverTerminalHistory(b *testing.B) {
	for _, workload := range []struct{ count, payloadBytes int }{
		{100, 1024}, {1000, 1024}, {10000, 1024}, {1000, 16 * 1024},
	} {
		b.Run(fmt.Sprintf("records=%d/payload=%d", workload.count, workload.payloadBytes), func(b *testing.B) {
			root := b.TempDir()
			store, err := Open(root)
			if err != nil {
				b.Fatal(err)
			}
			var retainedBytes int64
			now := time.Now().Add(-30 * 24 * time.Hour)
			for index := range workload.count {
				intent, createErr := NewMessageIntent(root, Identity{
					SourceID: fmt.Sprintf("synthetic-source-%06d", index), Channel: "test", ChatID: "synthetic-chat",
				}, bus.OutboundMessage{Content: strings.Repeat("x", workload.payloadBytes)}, now)
				if createErr != nil {
					b.Fatal(createErr)
				}
				intent.Status, intent.Attempts = StatusDelivered, 1
				intent.PlatformMessageIDs = []string{"synthetic-receipt"}
				data, marshalErr := json.MarshalIndent(intent, "", "  ")
				if marshalErr != nil {
					b.Fatal(marshalErr)
				}
				if err = os.WriteFile(store.recordPath(intent.ID), data, 0o600); err != nil {
					b.Fatal(err)
				}
				retainedBytes += int64(len(data))
			}
			started := time.Now()
			if _, err = store.Recover(); err != nil {
				b.Fatal(err)
			}
			firstScan := time.Since(started)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err = store.Recover(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(retainedBytes), "retained_B")
			b.ReportMetric(float64(firstScan), "first_scan_ns")
		})
	}
}
