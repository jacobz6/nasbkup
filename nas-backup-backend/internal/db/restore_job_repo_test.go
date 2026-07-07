package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/models"
)

func setupRestoreJobTestDB(t *testing.T) *Database {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available (CGO required): %v", err)
	}
	return database
}

func TestRestoreJobRepository_CRUD(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()

	repo := database.RestoreJobRepo

	// Create a job.
	rec := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusPending,
		Paths:            []string{"/data/test.txt", "/data/docs/"},
		Pattern:          "",
		OutputDir:        "/tmp/restore",
		Expedited:        false,
		ConflictStrategy: "skip",
		TotalFiles:       2,
		TotalSize:        1024,
	}
	id, err := repo.Create(rec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Get by ID.
	got, err := repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil record")
	}
	if got.Status != models.RestoreJobStatusPending {
		t.Errorf("expected pending, got %q", got.Status)
	}
	if len(got.Paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(got.Paths))
	}
	if got.TotalFiles != 2 {
		t.Errorf("expected total_files=2, got %d", got.TotalFiles)
	}
	if got.TotalSize != 1024 {
		t.Errorf("expected total_size=1024, got %d", got.TotalSize)
	}
	if got.ConflictStrategy != "skip" {
		t.Errorf("expected conflict_strategy=skip, got %q", got.ConflictStrategy)
	}

	// Update status to running.
	if err := repo.UpdateStatus(id, models.RestoreJobStatusRunning, ""); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	got, err = repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after running: %v", err)
	}
	if got.Status != models.RestoreJobStatusRunning {
		t.Errorf("expected running, got %q", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("expected started_at to be set")
	}

	// Update progress.
	if err := repo.UpdateProgress(id, 1, 512, []string{"/data/failed.txt"}); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	got, err = repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after progress: %v", err)
	}
	if got.RestoredFiles != 1 {
		t.Errorf("expected restored_files=1, got %d", got.RestoredFiles)
	}
	if got.RestoredSize != 512 {
		t.Errorf("expected restored_size=512, got %d", got.RestoredSize)
	}
	if len(got.FailedFiles) != 1 {
		t.Errorf("expected 1 failed file, got %d", len(got.FailedFiles))
	}

	// Update completed.
	if err := repo.UpdateCompleted(id, 2, 1024, 5000, []string{"/data/failed.txt"}); err != nil {
		t.Fatalf("UpdateCompleted: %v", err)
	}
	got, err = repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after completed: %v", err)
	}
	if got.ElapsedMs != 5000 {
		t.Errorf("expected elapsed_ms=5000, got %d", got.ElapsedMs)
	}

	// Update status to completed.
	if err := repo.UpdateStatus(id, models.RestoreJobStatusCompleted, ""); err != nil {
		t.Fatalf("UpdateStatus(completed): %v", err)
	}
	got, err = repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID final: %v", err)
	}
	if got.Status != models.RestoreJobStatusCompleted {
		t.Errorf("expected completed, got %q", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestRestoreJobRepository_List(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	// Create several jobs.
	for i := 0; i < 5; i++ {
		rec := &models.RestoreJobRecord{
			Status:           models.RestoreJobStatusCompleted,
			Paths:            []string{"/data/file.txt"},
			OutputDir:        "/tmp/restore",
			ConflictStrategy: "skip",
			TotalFiles:       1,
			TotalSize:        int64(i+1) * 100,
		}
		if i == 2 {
			rec.Status = models.RestoreJobStatusFailed
		}
		if _, err := repo.Create(rec); err != nil {
			t.Fatalf("Create job %d: %v", i, err)
		}
		// Small sleep to ensure different created_at timestamps.
		time.Sleep(10 * time.Millisecond)
	}

	// List all.
	jobs, total, err := repo.List(10, 0, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if len(jobs) != 5 {
		t.Errorf("expected 5 jobs, got %d", len(jobs))
	}

	// List with status filter.
	jobs, total, err = repo.List(10, 0, "failed")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 failed job, got %d", total)
	}

	// Pagination.
	jobs, total, err = repo.List(2, 0, "")
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs on page 1, got %d", len(jobs))
	}
	jobs, _, err = repo.List(2, 2, "")
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs on page 2, got %d", len(jobs))
	}
}

func TestRestoreJobRepository_IsRunning(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	running, err := repo.IsRunning()
	if err != nil {
		t.Fatalf("IsRunning initial: %v", err)
	}
	if running {
		t.Error("expected no running jobs initially")
	}

	// Create and start a job.
	rec := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusPending,
		Paths:            []string{"/data/file.txt"},
		OutputDir:        "/tmp/restore",
		ConflictStrategy: "skip",
	}
	id, _ := repo.Create(rec)
	_ = repo.UpdateStatus(id, models.RestoreJobStatusRunning, "")

	running, err = repo.IsRunning()
	if err != nil {
		t.Fatalf("IsRunning after start: %v", err)
	}
	if !running {
		t.Error("expected running job")
	}
}

func TestRestoreJobRepository_CleanupStaleRunning(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	// Create a stale running job.
	rec := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusRunning,
		Paths:            []string{"/data/file.txt"},
		OutputDir:        "/tmp/restore",
		ConflictStrategy: "skip",
	}
	id, _ := repo.Create(rec)
	// Manually set started_at in the past by direct update using Now() format.
	pastTime := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	database.DB().Exec(`UPDATE restore_jobs SET started_at = ? WHERE id = ?`, pastTime, id)

	// Create a stale pending job.
	rec2 := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusPending,
		Paths:            []string{"/data/file2.txt"},
		OutputDir:        "/tmp/restore",
		ConflictStrategy: "skip",
	}
	_, _ = repo.Create(rec2)

	n, err := repo.CleanupStaleRunning()
	if err != nil {
		t.Fatalf("CleanupStaleRunning: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 cleaned jobs, got %d", n)
	}

	// Verify both are now failed.
	got, err := repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after cleanup: %v", err)
	}
	if got == nil {
		t.Fatalf("expected record for id=%d after cleanup, got nil", id)
	}
	if got.Status != models.RestoreJobStatusFailed {
		t.Errorf("expected status=failed, got %q", got.Status)
	}
}

func TestRestoreJobRepository_GetRunning(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	got, err := repo.GetRunning()
	if err != nil {
		t.Fatalf("GetRunning initial: %v", err)
	}
	if got != nil {
		t.Error("expected nil when no running job")
	}

	rec := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusPending,
		Paths:            []string{"/data/file.txt"},
		OutputDir:        "/tmp/restore",
		ConflictStrategy: "skip",
	}
	id, _ := repo.Create(rec)
	_ = repo.UpdateStatus(id, models.RestoreJobStatusRunning, "")

	got, err = repo.GetRunning()
	if err != nil {
		t.Fatalf("GetRunning after start: %v", err)
	}
	if got == nil {
		t.Fatal("expected running job")
	}
	if got.ID != id {
		t.Errorf("expected id=%d, got %d", id, got.ID)
	}
}
