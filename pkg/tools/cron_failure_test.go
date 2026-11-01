package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/cron"
	taskregistry "github.com/bogdanovich/mintclaw/pkg/tasks"
)

func TestCronCommandOutcomeReachesSchedulerAndDurableTask(t *testing.T) {
	for _, scenario := range []string{"workspace_runner", "outside_workspace", "exit_failure", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Tools.Exec.Enabled = scenario != "disabled"
			tool := newTestCronToolWithConfig(t, cfg)
			registry := taskregistry.NewRegistry(taskregistry.WorkspaceStorePath(t.TempDir()))
			tool.SetTaskRegistry(registry)
			workspace := tool.workspace
			path := filepath.Join(workspace, "sync-test")
			if scenario == "outside_workspace" {
				path = filepath.Join(t.TempDir(), "sync-test")
			}
			marker := filepath.Join(workspace, "executed")
			if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := path
			if scenario == "exit_failure" {
				command = "exit 7"
			}
			service := tool.cronService
			service.SetOnJob(func(job *cron.CronJob) (string, error) {
				return tool.ExecuteJob(t.Context(), job)
			})
			if err := service.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := service.StopAndDrain(context.Background()); err != nil {
					t.Error(err)
				}
			})
			every := time.Hour.Milliseconds()
			job, err := service.AddJob(
				"sync",
				cron.CronSchedule{Kind: cron.ScheduleEvery, EveryMS: &every},
				cron.CronPayload{
					Kind: cron.PayloadCommand, Message: "sync test", Command: command, Channel: "cli", To: "direct",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			past := time.Now().Add(-time.Second).UnixMilli()
			job.State.NextRunAtMS = &past
			if err := service.UpdateJob(job); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(5 * time.Second)
			for {
				updated, _ := service.GetJob(job.ID)
				if updated.State.LastRunAtMS != nil {
					job = updated
					break
				}
				select {
				case <-deadline:
					t.Fatal("scheduled command did not finish")
				case <-time.After(10 * time.Millisecond):
				}
			}
			record := singleCronTaskRecord(t, registry)
			if scenario == "workspace_runner" {
				if job.State.LastStatus != "ok" || job.State.LastError != "" ||
					record.Status != taskregistry.StatusSucceeded {
					t.Fatalf("successful runner: state=%#v task=%#v", job.State, record)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("workspace runner was not executed:", err)
				}
			} else {
				if job.State.LastStatus != "error" || job.State.LastError == "" ||
					record.Status != taskregistry.StatusFailed {
					t.Fatalf("failure hidden: state=%#v task=%#v", job.State, record)
				}
				if scenario == "outside_workspace" &&
					!strings.Contains(job.State.LastError, "path outside working dir") {
					t.Fatalf("unexpected guard result: %s", job.State.LastError)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("blocked/failed runner unexpectedly executed: %v", err)
				}
			}
		})
	}
}
