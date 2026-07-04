// Package scheduler manages cron-based automatic backup scheduling.
// It determines whether to run a full or incremental backup based on the
// configured full-reset interval and the date of the last full backup.
package scheduler

import (
        "context"
        "fmt"
        "log/slog"
        "sync"
        "time"

        "github.com/robfig/cron/v3"

        "github.com/nas-backup/internal/backup"
        "github.com/nas-backup/internal/config"
        "github.com/nas-backup/internal/db"
)

// Scheduler manages periodic backup execution using a cron expression.
type Scheduler struct {
        cron   *cron.Cron
        engine *backup.Engine
        db     *db.Database
        config *config.AppConfig
        mu     sync.Mutex
        jobID  cron.EntryID
}

// NewScheduler creates a new Scheduler with the given dependencies.
func NewScheduler(engine *backup.Engine, database *db.Database, cfg *config.AppConfig) *Scheduler {
        return &Scheduler{
                engine: engine,
                db:     database,
                config: cfg,
        }
}

// Start parses the cron expression from config, registers the backup job,
// and starts the cron scheduler. Returns an error if the cron expression
// is invalid or the scheduler is already running.
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cron != nil {
		return fmt.Errorf("scheduler already running")
	}

	cronExpr := s.config.Backup.Schedule.CronExpr
	if cronExpr == "" {
		return fmt.Errorf("cron expression is empty")
	}

	return s.startLocked(cronExpr)
}

// StartWithCron starts the scheduler with a specific cron expression,
// bypassing the config file value. Used when the schedule is updated via API.
func (s *Scheduler) StartWithCron(cronExpr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cron != nil {
		return fmt.Errorf("scheduler already running")
	}
	if cronExpr == "" {
		return fmt.Errorf("cron expression is empty")
	}

	return s.startLocked(cronExpr)
}

func (s *Scheduler) startLocked(cronExpr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(cronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	s.cron = cron.New()
	jobID, err := s.cron.AddFunc(cronExpr, s.runBackup)
	if err != nil {
		s.cron = nil
		return fmt.Errorf("add cron job with expression %q: %w", cronExpr, err)
	}
	s.jobID = jobID
	s.cron.Start()
	slog.Info("scheduler started", "cron_expr", cronExpr, "next_run", s.NextRunLocked())
	return nil
}

// runBackup is the cron job callback that determines the backup type
// and executes it.
func (s *Scheduler) runBackup() {
        slog.Info("scheduled backup triggered")

        fullResetNeeded := s.isFullResetNeeded()

        var err error
        ctx := context.Background()

        if fullResetNeeded {
                slog.Info("full reset interval reached, running full backup")
                err = s.engine.RunFullBackup(ctx)
        } else {
                slog.Info("running incremental backup")
                err = s.engine.RunIncrementalBackup(ctx)
        }

        if err != nil {
                slog.Error("scheduled backup failed", "error", err)
        } else {
                slog.Info("scheduled backup completed successfully")
        }
}

// isFullResetNeeded checks whether a full backup should be performed instead
// of an incremental one, based on the configured full-reset interval.
func (s *Scheduler) isFullResetNeeded() bool {
        intervalMonths := s.config.Backup.Retention.FullResetInterval
        if intervalMonths <= 0 {
                return false
        }

        latestFull, err := s.db.BackupRepo.GetLatestFull()
        if err != nil {
                slog.Warn("failed to get latest full backup, defaulting to full backup",
                        "error", err)
                return true
        }
        if latestFull == nil {
                return true // No full backup exists yet.
        }
        if latestFull.CompletedAt == nil {
                return true // Full backup never completed successfully.
        }

        cutoff := time.Now().AddDate(0, -intervalMonths, 0)
        return latestFull.CompletedAt.Before(cutoff)
}

// Stop gracefully stops the cron scheduler, waiting for any running job
// to complete.
func (s *Scheduler) Stop() {
        s.mu.Lock()
        defer s.mu.Unlock()

        if s.cron != nil {
                ctx := s.cron.Stop()
                <-ctx.Done() // Wait for running jobs to finish.
                s.cron = nil
                slog.Info("scheduler stopped")
        }
}

// UpdateSchedule replaces the current cron expression with a new one.
// The old job is removed and a new one is registered. Returns an error
// if the new expression is invalid or the scheduler is not running.
func (s *Scheduler) UpdateSchedule(cronExpr string) error {
        s.mu.Lock()
        defer s.mu.Unlock()

        if s.cron == nil {
                return fmt.Errorf("scheduler not running")
        }

        // Validate the new expression before removing the old job.
        // Use the standard 5-field cron parser (minute hour day month weekday).
        parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
        schedule, err := parser.Parse(cronExpr)
        if err != nil {
                return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
        }
        _ = schedule

        // Remove old job and add new one.
        s.cron.Remove(s.jobID)

        jobID, err := s.cron.AddFunc(cronExpr, s.runBackup)
        if err != nil {
                return fmt.Errorf("add cron job with new expression: %w", err)
        }
        s.jobID = jobID

        slog.Info("schedule updated", "cron_expr", cronExpr, "next_run", s.NextRunLocked())
        return nil
}

// NextRun returns the time of the next scheduled backup.
// Returns the zero time if the scheduler is not running.
func (s *Scheduler) NextRun() time.Time {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.NextRunLocked()
}

// NextRunLocked returns the next run time. Caller must hold s.mu.
func (s *Scheduler) NextRunLocked() time.Time {
        if s.cron == nil {
                return time.Time{}
        }
        entries := s.cron.Entries()
        if len(entries) == 0 {
                return time.Time{}
        }
        return entries[0].Next
}

// IsEnabled returns true if the scheduler is currently running.
func (s *Scheduler) IsEnabled() bool {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.cron != nil
}
