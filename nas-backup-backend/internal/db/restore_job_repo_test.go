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

// TestRestoreJobRepository_ExportImportRoundTrip verifies that ExportAll +
// ImportBatch preserves all restore_jobs fields (IDs, status, JSON paths,
// timestamps, nullable fields). This is the exact roundtrip performed by
// Engine.pullAndPreserveRestoreJobs when OSS DB overwrites the local file.
func TestRestoreJobRepository_ExportImportRoundTrip(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	now := time.Now().UTC().Truncate(time.Second)
	backupID := int64(42)
	startedAt := now.Add(-1 * time.Minute)
	completedAt := now

	src := []*models.RestoreJobRecord{
		{
			ID:               101,
			Status:           models.RestoreJobStatusCompleted,
			Paths:            []string{"/data/a.txt", "/data/b.txt"},
			Pattern:          "",
			BackupID:         &backupID,
			OutputDir:        "/restore/dest",
			Expedited:        true,
			ConflictStrategy: "skip",
			TotalFiles:       2,
			RestoredFiles:    2,
			FailedFiles:      nil,
			TotalSize:        1024,
			RestoredSize:     1024,
			ElapsedMs:        5000,
			ErrorMessage:     "",
			CreatedAt:        now.Add(-2 * time.Minute),
			StartedAt:        &startedAt,
			CompletedAt:      &completedAt,
		},
		{
			ID:               102,
			Status:           models.RestoreJobStatusFailed,
			Paths:            []string{"/data/c.txt"},
			Pattern:          "/data/*.txt",
			BackupID:         nil,
			OutputDir:        "__original__",
			Expedited:        false,
			ConflictStrategy: "overwrite",
			TotalFiles:       3,
			RestoredFiles:    1,
			FailedFiles:      []string{"/data/c.txt", "/data/d.txt"},
			TotalSize:        2048,
			RestoredSize:     512,
			ElapsedMs:        0,
			ErrorMessage:     "download failed",
			CreatedAt:        now.Add(-1 * time.Minute),
			StartedAt:        nil,
			CompletedAt:      nil,
		},
	}

	// Insert via ImportBatch (which is the write-path after PullOSSDB).
	if err := repo.ImportBatch(src); err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}

	// Export and verify each field survives the roundtrip.
	got, err := repo.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(got) != len(src) {
		t.Fatalf("expected %d records, got %d", len(src), len(got))
	}

	for i, want := range src {
		r := got[i]
		if r.ID != want.ID {
			t.Errorf("[%d] ID: got %d want %d", i, r.ID, want.ID)
		}
		if r.Status != want.Status {
			t.Errorf("[%d] Status: got %q want %q", i, r.Status, want.Status)
		}
		if len(r.Paths) != len(want.Paths) {
			t.Errorf("[%d] Paths length: got %d want %d", i, len(r.Paths), len(want.Paths))
		} else {
			for j := range r.Paths {
				if r.Paths[j] != want.Paths[j] {
					t.Errorf("[%d] Paths[%d]: got %q want %q", i, j, r.Paths[j], want.Paths[j])
				}
			}
		}
		if r.Pattern != want.Pattern {
			t.Errorf("[%d] Pattern: got %q want %q", i, r.Pattern, want.Pattern)
		}
		if (r.BackupID == nil) != (want.BackupID == nil) {
			t.Errorf("[%d] BackupID nil mismatch", i)
		} else if r.BackupID != nil && *r.BackupID != *want.BackupID {
			t.Errorf("[%d] BackupID: got %d want %d", i, *r.BackupID, *want.BackupID)
		}
		if r.OutputDir != want.OutputDir {
			t.Errorf("[%d] OutputDir: got %q want %q", i, r.OutputDir, want.OutputDir)
		}
		if r.Expedited != want.Expedited {
			t.Errorf("[%d] Expedited: got %v want %v", i, r.Expedited, want.Expedited)
		}
		if r.ConflictStrategy != want.ConflictStrategy {
			t.Errorf("[%d] ConflictStrategy: got %q want %q", i, r.ConflictStrategy, want.ConflictStrategy)
		}
		if r.TotalFiles != want.TotalFiles {
			t.Errorf("[%d] TotalFiles: got %d want %d", i, r.TotalFiles, want.TotalFiles)
		}
		if r.RestoredFiles != want.RestoredFiles {
			t.Errorf("[%d] RestoredFiles: got %d want %d", i, r.RestoredFiles, want.RestoredFiles)
		}
		if len(r.FailedFiles) != len(want.FailedFiles) {
			t.Errorf("[%d] FailedFiles length: got %d want %d", i, len(r.FailedFiles), len(want.FailedFiles))
		}
		if r.TotalSize != want.TotalSize {
			t.Errorf("[%d] TotalSize: got %d want %d", i, r.TotalSize, want.TotalSize)
		}
		if r.RestoredSize != want.RestoredSize {
			t.Errorf("[%d] RestoredSize: got %d want %d", i, r.RestoredSize, want.RestoredSize)
		}
		if r.ErrorMessage != want.ErrorMessage {
			t.Errorf("[%d] ErrorMessage: got %q want %q", i, r.ErrorMessage, want.ErrorMessage)
		}
		if !r.CreatedAt.Equal(want.CreatedAt) {
			t.Errorf("[%d] CreatedAt: got %v want %v", i, r.CreatedAt, want.CreatedAt)
		}
		if (r.StartedAt == nil) != (want.StartedAt == nil) {
			t.Errorf("[%d] StartedAt nil mismatch", i)
		} else if r.StartedAt != nil && !r.StartedAt.Equal(*want.StartedAt) {
			t.Errorf("[%d] StartedAt: got %v want %v", i, r.StartedAt, want.StartedAt)
		}
		if (r.CompletedAt == nil) != (want.CompletedAt == nil) {
			t.Errorf("[%d] CompletedAt nil mismatch", i)
		} else if r.CompletedAt != nil && !r.CompletedAt.Equal(*want.CompletedAt) {
			t.Errorf("[%d] CompletedAt: got %v want %v", i, r.CompletedAt, want.CompletedAt)
		}
	}
}

func TestRestoreJobRepository_ExportImportEmpty(t *testing.T) {
	database := setupRestoreJobTestDB(t)
	defer database.Close()
	repo := database.RestoreJobRepo

	// ImportBatch with empty input must be a no-op (no error, no rows).
	if err := repo.ImportBatch(nil); err != nil {
		t.Fatalf("ImportBatch(nil): %v", err)
	}
	got, err := repo.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll after empty import: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 records after empty import, got %d", len(got))
	}
}
