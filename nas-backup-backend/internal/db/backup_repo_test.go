package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/models"
)

func setupBackupTestDB(t *testing.T) *Database {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available (CGO required): %v", err)
	}
	return database
}

func TestBackupRepository_Create(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	id, err := repo.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	rec, err := repo.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Status != models.BackupStatusPending {
		t.Errorf("expected status=pending, got %q", rec.Status)
	}
	if rec.FailedFiles != 0 {
		t.Errorf("expected failed_files=0, got %d", rec.FailedFiles)
	}
}

func TestBackupRepository_UpdateStatus_CompletedWithErrors(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	id, err := repo.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update status to running.
	if err := repo.UpdateStatus(id, models.BackupStatusRunning, ""); err != nil {
		t.Fatalf("UpdateStatus(running): %v", err)
	}
	rec, _ := repo.GetByID(id)
	if rec.Status != models.BackupStatusRunning {
		t.Errorf("expected running, got %q", rec.Status)
	}
	if rec.StartedAt == nil {
		t.Error("expected started_at to be set")
	}

	// Update status to completed_with_errors.
	errMsg := "2 file(s) failed: /data/big.txt: insufficient temp space"
	if err := repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, errMsg); err != nil {
		t.Fatalf("UpdateStatus(completed_with_errors): %v", err)
	}
	rec, _ = repo.GetByID(id)
	if rec.Status != models.BackupStatusCompletedWithErrors {
		t.Errorf("expected completed_with_errors, got %q", rec.Status)
	}
	if rec.ErrorMessage != errMsg {
		t.Errorf("expected error_message=%q, got %q", errMsg, rec.ErrorMessage)
	}
	if rec.CompletedAt == nil {
		t.Error("expected completed_at to be set for terminal status")
	}
}

func TestBackupRepository_UpdateStats_FailedFiles(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	id, err := repo.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// UpdateStats signature: (id, totalFiles, totalSize, uploadedSize, skippedDedup, failedFiles, compressSaved)
	if err := repo.UpdateStats(id, 100, 1024000, 512000, 20, 3, 500000); err != nil {
		t.Fatalf("UpdateStats: %v", err)
	}

	rec, _ := repo.GetByID(id)
	if rec.TotalFiles != 100 {
		t.Errorf("expected total_files=100, got %d", rec.TotalFiles)
	}
	if rec.TotalSize != 1024000 {
		t.Errorf("expected total_size=1024000, got %d", rec.TotalSize)
	}
	if rec.UploadedSize != 512000 {
		t.Errorf("expected uploaded_size=512000, got %d", rec.UploadedSize)
	}
	if rec.SkippedByDedup != 20 {
		t.Errorf("expected skipped_dedup=20, got %d", rec.SkippedByDedup)
	}
	if rec.FailedFiles != 3 {
		t.Errorf("expected failed_files=3, got %d", rec.FailedFiles)
	}
	if rec.CompressSaved != 500000 {
		t.Errorf("expected compress_saved=500000, got %d", rec.CompressSaved)
	}
}

func TestBackupRepository_GetLatestCompleted_IncludesCompletedWithErrors(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// Create a backup and mark it completed_with_errors.
	id1, _ := repo.Create()
	_ = repo.UpdateStatus(id1, models.BackupStatusRunning, "")
	_ = repo.UpdateStatus(id1, models.BackupStatusCompletedWithErrors, "1 file(s) failed")

	// GetLatestCompleted should return it.
	rec, err := repo.GetLatestCompleted()
	if err != nil {
		t.Fatalf("GetLatestCompleted: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.ID != id1 {
		t.Errorf("expected id=%d, got %d", id1, rec.ID)
	}
	if rec.Status != models.BackupStatusCompletedWithErrors {
		t.Errorf("expected completed_with_errors, got %q", rec.Status)
	}
}

func TestBackupRepository_IsRunning_ExcludesCompletedWithErrors(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// completed_with_errors is a terminal state, NOT running.
	id, _ := repo.Create()
	_ = repo.UpdateStatus(id, models.BackupStatusRunning, "")
	_ = repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, "")

	running, err := repo.IsRunning()
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Error("expected completed_with_errors to NOT be considered running")
	}
}

func TestBackupRepository_ListFailedBackupsWithFiles_ExcludesCompletedWithErrors(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// Create a completed_with_errors backup.
	id, _ := repo.Create()
	_ = repo.UpdateStatus(id, models.BackupStatusRunning, "")
	_ = repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, "")

	// ListFailedBackupsWithFiles queries WHERE status = 'failed', so a
	// completed_with_errors backup should NOT be returned regardless of
	// whether it has backup_files rows.
	failed, err := repo.ListFailedBackupsWithFiles()
	if err != nil {
		t.Fatalf("ListFailedBackupsWithFiles: %v", err)
	}
	for _, rec := range failed {
		if rec.ID == id {
			t.Errorf("expected completed_with_errors backup to be excluded from failed list")
		}
	}
}

func TestBackupRepository_ListCompletedBackupsWithoutFiles_IncludesCompletedWithErrors(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// Create a backup with completed_with_errors and NO backup_files.
	id, _ := repo.Create()
	_ = repo.UpdateStatus(id, models.BackupStatusRunning, "")
	_ = repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, "")

	// ListCompletedBackupsWithoutFiles should include this.
	completed, err := repo.ListCompletedBackupsWithoutFiles()
	if err != nil {
		t.Fatalf("ListCompletedBackupsWithoutFiles: %v", err)
	}
	found := false
	for _, rec := range completed {
		if rec.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected completed_with_errors backup without files to be listed")
	}
}

func TestBackupRepository_UpdateStats_ZeroFailedFiles(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	id, _ := repo.Create()

	// UpdateStats with failed_files=0 (normal case).
	if err := repo.UpdateStats(id, 50, 500000, 250000, 10, 0, 100000); err != nil {
		t.Fatalf("UpdateStats: %v", err)
	}

	rec, _ := repo.GetByID(id)
	if rec.FailedFiles != 0 {
		t.Errorf("expected failed_files=0, got %d", rec.FailedFiles)
	}
}

func TestBackupRepository_CRUD_FullCycle(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// Create
	id, _ := repo.Create()

	// Running
	_ = repo.UpdateStatus(id, models.BackupStatusRunning, "")

	// Stats
	_ = repo.UpdateStats(id, 10, 1000, 800, 2, 1, 200)

	// Completed with errors
	_ = repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, "1 file(s) failed")

	rec, _ := repo.GetByID(id)
	if rec.TotalFiles != 10 {
		t.Errorf("expected total_files=10, got %d", rec.TotalFiles)
	}
	if rec.FailedFiles != 1 {
		t.Errorf("expected failed_files=1, got %d", rec.FailedFiles)
	}
	if rec.Status != models.BackupStatusCompletedWithErrors {
		t.Errorf("expected completed_with_errors, got %q", rec.Status)
	}
	if rec.ErrorMessage != "1 file(s) failed" {
		t.Errorf("expected error_message=%q, got %q", "1 file(s) failed", rec.ErrorMessage)
	}
	if rec.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestBackupRepository_List(t *testing.T) {
	database := setupBackupTestDB(t)
	defer database.Close()
	repo := database.BackupRepo

	// Create several backups with different statuses.
	for i := 0; i < 5; i++ {
		id, _ := repo.Create()
		_ = repo.UpdateStatus(id, models.BackupStatusRunning, "")
		if i < 3 {
			_ = repo.UpdateStatus(id, models.BackupStatusCompleted, "")
		} else if i == 3 {
			_ = repo.UpdateStatus(id, models.BackupStatusCompletedWithErrors, "err")
		} else {
			_ = repo.UpdateStatus(id, models.BackupStatusFailed, "err")
		}
		time.Sleep(10 * time.Millisecond)
	}

	records, total, err := repo.List(10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if len(records) != 5 {
		t.Errorf("expected 5 records, got %d", len(records))
	}

	// Pagination.
	records, total, err = repo.List(2, 0)
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records on page 1, got %d", len(records))
	}
	_ = total
}
