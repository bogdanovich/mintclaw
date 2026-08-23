package cron

import (
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/cron"
)

func loadCronService(storePath string) (*cron.CronService, error) {
	cs := cron.NewCronService(storePath, nil)
	if err := cs.Load(); err != nil {
		return nil, fmt.Errorf("load cron store: %w", err)
	}
	return cs, nil
}

func cronListCmd(storePath string) error {
	cs, err := loadCronService(storePath)
	if err != nil {
		return err
	}
	jobs := cs.ListJobs(true) // Show all jobs, including disabled

	if len(jobs) == 0 {
		fmt.Println("No scheduled jobs.")
		return nil
	}

	fmt.Println("\nScheduled Jobs:")
	fmt.Println("----------------")
	for _, job := range jobs {
		var schedule string
		if job.Schedule.Kind == "every" && job.Schedule.EveryMS != nil {
			schedule = fmt.Sprintf("every %ds", *job.Schedule.EveryMS/1000)
		} else if job.Schedule.Kind == "cron" {
			schedule = job.Schedule.Expr
		} else {
			schedule = "one-time"
		}

		nextRun := "scheduled"
		if job.State.NextRunAtMS != nil {
			nextTime := time.UnixMilli(*job.State.NextRunAtMS)
			nextRun = nextTime.Format("2006-01-02 15:04")
		}

		status := "enabled"
		if !job.Enabled {
			status = "disabled"
		}

		fmt.Printf("  %s (%s)\n", job.Name, job.ID)
		fmt.Printf("    Schedule: %s\n", schedule)
		fmt.Printf("    Status: %s\n", status)
		fmt.Printf("    Next run: %s\n", nextRun)
	}
	return nil
}

func cronRemoveCmd(storePath, jobID string) error {
	cs, err := loadCronService(storePath)
	if err != nil {
		return err
	}
	if !cs.RemoveJob(jobID) {
		return fmt.Errorf("job %s not found", jobID)
	}
	fmt.Printf("✓ Removed job %s\n", jobID)
	return nil
}

func cronSetJobEnabled(storePath, jobID string, enabled bool) error {
	cs, err := loadCronService(storePath)
	if err != nil {
		return err
	}
	job, err := cs.EnableJob(jobID, enabled)
	if err != nil {
		return err
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	fmt.Printf("✓ Job '%s' %s\n", job.Name, state)
	return nil
}
