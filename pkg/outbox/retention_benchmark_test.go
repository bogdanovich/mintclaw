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

// BenchmarkRecoverRetainedPayloads compares the same synthetic terminal
// histories as R10. Fixture writes warm caches; first_scan_ns is not OS-cold
// startup. Direct fixture encoding does not qualify a live retirement owner.
func BenchmarkRecoverRetainedPayloads(b *testing.B) {
	for _, workload := range []struct{ count, payloadBytes int }{
		{100, 1024}, {1000, 1024}, {10000, 1024}, {1000, 16 * 1024},
	} {
		for _, mode := range []string{"full", "compact"} {
			b.Run(
				fmt.Sprintf("records=%d/payload=%d/%s", workload.count, workload.payloadBytes, mode),
				func(b *testing.B) {
					root := b.TempDir()
					store, err := Open(root)
					if err != nil {
						b.Fatal(err)
					}
					var retainedBytes int64
					now := time.Now().UTC().Truncate(time.Second)
					old := now.Add(-30 * 24 * time.Hour)
					for index := range workload.count {
						intent, createErr := NewMessageIntent("/synthetic/instance", Identity{
							SourceID: fmt.Sprintf(
								"synthetic-source-%06d",
								index,
							),
							Channel: "test",
							ChatID:  "synthetic-chat",
						}, bus.OutboundMessage{Content: strings.Repeat("x", workload.payloadBytes)}, old)
						if createErr != nil {
							b.Fatal(createErr)
						}
						intent.Version = retentionRecordVersion
						intent.RetentionClass = retentionPlainMessage
						intent.Status, intent.Attempts = StatusDelivered, 1
						intent.PlatformMessageIDs = []string{"synthetic-receipt"}
						if mode == "compact" {
							intent = compactDeliveredIntent(intent, now)
						}
						if err = validateIntent(intent); err != nil {
							b.Fatal(err)
						}
						data, encodeErr := json.MarshalIndent(intent, "", "  ")
						if encodeErr != nil {
							b.Fatal(encodeErr)
						}
						if err = os.WriteFile(store.recordPath(intent.ID), data, 0o600); err != nil {
							b.Fatal(err)
						}
						retainedBytes += int64(len(data))
					}
					started := time.Now()
					report, err := store.Recover()
					if err != nil || len(report.Intents) != 0 || len(report.Issues) != 0 {
						b.Fatalf("terminal recovery=%#v,%v", report, err)
					}
					firstScan := time.Since(started)
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						report, err = store.Recover()
						if err != nil || len(report.Intents) != 0 || len(report.Issues) != 0 {
							b.Fatalf("terminal recovery=%#v,%v", report, err)
						}
					}
					b.ReportMetric(float64(retainedBytes), "retained_B")
					b.ReportMetric(float64(firstScan), "first_scan_ns")
				},
			)
		}
	}
}
