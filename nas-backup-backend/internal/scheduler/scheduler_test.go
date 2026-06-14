package scheduler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
)

func setupTestDB(t *testing.T) (*db.Database, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available: %v", err)
	}
	return database, func() { database.Close() }
}

func TestNewScheduler(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 2 * * *",
				Timezone: "Asia/Shanghai",
			},
			Retention: config.RetentionConfig{
				FullResetInterval: 12,
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	if s == nil {
		t.Fatal("NewScheduler returned nil")
	}
}

func TestStartInvalidCron(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "not valid",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	err := s.Start()
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestStartEmptyCron(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	err := s.Start()
	if err == nil {
		t.Fatal("expected error for empty cron")
	}
}

func TestStartStopValidCron(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 * * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !s.IsEnabled() {
		t.Error("scheduler should be enabled")
	}
	s.Stop()
	if s.IsEnabled() {
		t.Error("scheduler should not be enabled after Stop")
	}
}

func TestStopBeforeStart(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled: false,
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	s.Stop() // should not panic
}

func TestStartTwice(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 * * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	if err := s.Start(); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	defer s.Stop()

	if err := s.Start(); err == nil {
		t.Error("Second Start should fail when already running")
	}
}

func TestNextRun(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 * * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	s.Start()
	defer s.Stop()

	next := s.NextRun()
	if next.IsZero() {
		t.Error("NextRun returned zero time")
	}
	if next.Before(time.Now()) {
		t.Error("NextRun returned past time")
	}
}

func TestNextRunNotRunning(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled: false,
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	next := s.NextRun()
	if !next.IsZero() {
		t.Errorf("expected zero time when not running, got %v", next)
	}
}

func TestUpdateSchedule(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 2 * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	s.Start()
	defer s.Stop()

	if err := s.UpdateSchedule("0 3 * * *"); err != nil {
		t.Fatalf("UpdateSchedule failed: %v", err)
	}
}

func TestUpdateScheduleInvalid(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 2 * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	s.Start()
	defer s.Stop()

	if err := s.UpdateSchedule("not valid"); err == nil {
		t.Fatal("expected error for invalid cron in UpdateSchedule")
	}
}

func TestUpdateScheduleNotRunning(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 2 * * *",
			},
		},
	}

	s := NewScheduler(nil, database, cfg)
	if err := s.UpdateSchedule("0 3 * * *"); err == nil {
		t.Fatal("expected error when updating non-running scheduler")
	}
}

func TestCronDescriptors(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	for _, desc := range []string{"@hourly", "@daily", "@weekly", "@monthly"} {
		t.Run(desc, func(t *testing.T) {
			cfg := &config.AppConfig{
				Backup: config.BackupConfig{
					Schedule: config.ScheduleConfig{
						Enabled:  true,
						CronExpr: desc,
					},
				},
			}
			s := NewScheduler(nil, database, cfg)
			if err := s.Start(); err != nil {
				t.Fatalf("Start with %q failed: %v", desc, err)
			}
			defer s.Stop()

			next := s.NextRun()
			if next.IsZero() {
				t.Errorf("NextRun zero for %q", desc)
			}
		})
	}
}

func TestBackupHistoryCreateAndGet(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	id, err := database.BackupRepo.Create(models.BackupTypeIncremental, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}

	// Set status to running and then completed with timestamps
	if err := database.BackupRepo.UpdateStatus(id, models.BackupStatusRunning, ""); err != nil {
		t.Fatalf("UpdateStatus to running failed: %v", err)
	}
	if err := database.BackupRepo.UpdateStatus(id, models.BackupStatusCompleted, ""); err != nil {
		t.Fatalf("UpdateStatus to completed failed: %v", err)
	}

	rec, err := database.BackupRepo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if rec.ID != id {
		t.Errorf("expected ID %d, got %d", id, rec.ID)
	}
	if rec.Type != models.BackupTypeIncremental {
		t.Errorf("expected Type Incremental, got %q", rec.Type)
	}
}

func TestBackupHistoryList(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		_, err := database.BackupRepo.Create(models.BackupTypeFull, nil)
		if err != nil {
			t.Fatalf("Create record %d failed: %v", i, err)
		}
	}

	all, err := database.BackupRepo.List(100, 0)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("expected 5 records, got %d", len(all))
	}
}

func TestGetLatestFullBackup(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// 无记录时应返回 nil
	latest, err := database.BackupRepo.GetLatestFull()
	if err != nil {
		t.Logf("GetLatestFull returned error (expected): %v", err)
	}
	if latest != nil {
		t.Error("expected no latest full backup")
	}

	// 插入全量备份并完成它
	id, err := database.BackupRepo.Create(models.BackupTypeFull, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if err := database.BackupRepo.UpdateStatus(id, models.BackupStatusRunning, ""); err != nil {
		t.Fatalf("UpdateStatus to running failed: %v", err)
	}
	if err := database.BackupRepo.UpdateStatus(id, models.BackupStatusCompleted, ""); err != nil {
		t.Fatalf("UpdateStatus to completed failed: %v", err)
	}

	latest, err = database.BackupRepo.GetLatestFull()
	if err != nil {
		t.Fatalf("GetLatestFull failed: %v", err)
	}
	if latest == nil {
		t.Fatal("expected latest full backup")
	}
	if latest.Type != models.BackupTypeFull {
		t.Errorf("expected Type Full, got %q", latest.Type)
	}
}

func TestBackupRepoUpdate(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	id, _ := database.BackupRepo.Create(models.BackupTypeFull, nil)

	// 更新状态
	err := database.BackupRepo.UpdateStatus(id, models.BackupStatusRunning, "")
	if err != nil {
		t.Fatalf("UpdateStatus to running failed: %v", err)
	}

	rec, _ := database.BackupRepo.GetByID(id)
	if rec.Status != models.BackupStatusRunning {
		t.Errorf("expected Status Running, got %q", rec.Status)
	}

	// 更新统计信息
	err = database.BackupRepo.UpdateStats(id, 100, 1024, 800, 20, 10, 512)
	if err != nil {
		t.Fatalf("UpdateStats failed: %v", err)
	}

	rec, _ = database.BackupRepo.GetByID(id)
	if rec.TotalFiles != 100 {
		t.Errorf("expected TotalFiles 100, got %d", rec.TotalFiles)
	}
	if rec.TotalSize != 1024 {
		t.Errorf("expected TotalSize 1024, got %d", rec.TotalSize)
	}

	// 更新状态为完成
	err = database.BackupRepo.UpdateStatus(id, models.BackupStatusCompleted, "")
	if err != nil {
		t.Fatalf("UpdateStatus to completed failed: %v", err)
	}

	rec, _ = database.BackupRepo.GetByID(id)
	if rec.Status != models.BackupStatusCompleted {
		t.Errorf("expected Status Completed, got %q", rec.Status)
	}
}

func TestConfigToModelsScheduleConfig(t *testing.T) {
	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Schedule: config.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 */6 * * *",
				Timezone: "Asia/Shanghai",
			},
		},
	}

	mc := cfg.ToModelsScheduleConfig()
	if !mc.Enabled {
		t.Error("expected Enabled true")
	}
	if mc.CronExpr != "0 */6 * * *" {
		t.Errorf("expected CronExpr %q, got %q", "0 */6 * * *", mc.CronExpr)
	}
}

func TestConfigToModelsRetentionConfig(t *testing.T) {
	cfg := &config.AppConfig{
		Backup: config.BackupConfig{
			Retention: config.RetentionConfig{
				VersionKeepCount:  5,
				OrphanGraceDays:   120,
				FullResetInterval: 9,
				KeepDeletedDays:   60,
			},
		},
	}

	mc := cfg.ToModelsRetentionConfig()
	if mc.VersionKeepCount != 5 {
		t.Errorf("expected VersionKeepCount 5, got %d", mc.VersionKeepCount)
	}
	if mc.OrphanGraceDays != 120 {
		t.Errorf("expected OrphanGraceDays 120, got %d", mc.OrphanGraceDays)
	}
}
