package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkRegistryDurableUpdate includes the real atomic write and sync path.
// Fixtures are validated by the production reader; setup is outside the timer.
// Explicit worker counts keep concurrency independent of the host's CPU count.
func BenchmarkRegistryDurableUpdate(b *testing.B) {
	for _, count := range []int{100, 1000, 2000} {
		for _, history := range []int{0, 5} {
			for _, workers := range []int{1, 4} {
				b.Run(fmt.Sprintf("records=%d/history=%d/workers=%d", count, history, workers), func(b *testing.B) {
					registry := benchmarkDurableRegistry(b, count, history)
					before := registry.Stats()
					writes := make([]time.Duration, b.N)
					reads := make([]time.Duration, b.N)
					var next atomic.Int64
					var group sync.WaitGroup
					failures := make(chan error, 1)
					b.ReportAllocs()
					b.ResetTimer()
					for range workers {
						group.Go(func() {
							for {
								index := int(next.Add(1) - 1)
								if index >= b.N {
									return
								}
								id := fmt.Sprintf("storage-bench-%06d", index%count)
								started := time.Now()
								err := registry.Update(id, func(rec *Record) {
									rec.ProgressSummary = fmt.Sprintf("synthetic progress %d", index)
								})
								writes[index] = time.Since(started)
								if err == nil {
									started = time.Now()
									if _, ok := registry.Get(id); !ok {
										err = fmt.Errorf("updated task %s is missing", id)
									}
									reads[index] = time.Since(started)
								}
								if err != nil {
									select {
									case failures <- err:
									default:
									}
									return
								}
							}
						})
					}
					group.Wait()
					b.StopTimer()
					select {
					case err := <-failures:
						b.Fatal(err)
					default:
					}
					after := registry.Stats()
					b.ReportMetric(float64(before.SnapshotBytes), "snapshot_start_B")
					b.ReportMetric(float64(after.SnapshotBytes), "snapshot_end_B")
					b.ReportMetric(float64(before.EventCount), "events_start")
					b.ReportMetric(float64(after.EventCount), "events_end")
					if before.OverSnapshotBudget {
						b.ReportMetric(1, "over_budget_start")
					}
					for label, samples := range map[string][]time.Duration{"write": writes, "read": reads} {
						slices.Sort(samples)
						for _, percentile := range []int{50, 95, 99} {
							index := (len(samples)*percentile+99)/100 - 1
							b.ReportMetric(float64(samples[index]), fmt.Sprintf("%s_p%d_ns", label, percentile))
						}
					}
				})
			}
		}
	}
}

func benchmarkDurableRegistry(b *testing.B, count, history int) *Registry {
	b.Helper()
	now := time.Now().UnixMilli()
	snapshot := Snapshot{Tasks: make([]Record, 0, count)}
	for index := range count {
		record := Record{
			TaskID:       fmt.Sprintf("storage-bench-%06d", index),
			GenerationID: fmt.Sprintf("storage-generation-%06d", index),
			LastEventSeq: int64(max(1, history)), Runtime: RuntimeSubagent,
			RequesterSessionKey: fmt.Sprintf("synthetic-session-%06d", index),
			Task:                strings.Repeat("x", 512), Status: StatusRunning,
			DeliveryStatus: DeliveryPending, NotifyPolicy: NotifyDoneOnly,
			CreatedAt: now, StartedAt: now, LastEventAt: now,
		}
		snapshot.Tasks = append(snapshot.Tasks, record)
		for sequence := 1; sequence <= history; sequence++ {
			event := TaskEvent{
				SchemaVersion: TaskEventSchemaVersion, TaskID: record.TaskID, GenerationID: record.GenerationID,
				EventID: fmt.Sprintf("%s:%s:%06d:%s", record.TaskID, record.GenerationID, sequence, EventTaskProgress),
				Runtime: record.Runtime, Type: EventTaskProgress, Status: record.Status,
				DeliveryStatus: record.DeliveryStatus, Seq: int64(sequence), EmittedAt: now,
				Source: "task_registry", Producer: "storage-bench", Payload: map[string]string{"progress": "synthetic"},
			}
			event.Fingerprint = taskEventFingerprint(event)
			snapshot.Events = append(snapshot.Events, event)
		}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "tasks.json")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		b.Fatal(err)
	}
	if err = ValidateSnapshot(path); err != nil {
		b.Fatal(err)
	}
	registry := NewRegistry(path)
	if err = registry.LastLoadError(); err != nil {
		b.Fatal(err)
	}
	if stats := registry.Stats(); stats.TaskCount != count || stats.ProtectedTaskCount != count {
		b.Fatalf("fixture lost protected tasks: %#v", stats)
	}
	return registry
}
