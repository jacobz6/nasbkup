package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/models"
)

func setupFileTestDB(t *testing.T) *Database {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available (CGO required): %v", err)
	}
	return database
}

func TestFileRepository_Upsert(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	repo := database.FileRepo

	now := time.Now().UTC().Truncate(time.Second)
	id, err := repo.Upsert("/data/test.txt", 1024, now, "abc123", 10001)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// GetByPath.
	rec, err := repo.GetByPath("/data/test.txt")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Path != "/data/test.txt" {
		t.Errorf("expected path=/data/test.txt, got %q", rec.Path)
	}
	if rec.Size != 1024 {
		t.Errorf("expected size=1024, got %d", rec.Size)
	}
	if rec.Hash != "abc123" {
		t.Errorf("expected hash=abc123, got %q", rec.Hash)
	}
	if rec.LastBackupStatus != "" {
		t.Errorf("expected empty last_backup_status, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupError != "" {
		t.Errorf("expected empty last_backup_error, got %q", rec.LastBackupError)
	}
	if rec.LastBackupAt != nil {
		t.Error("expected nil last_backup_at")
	}
	if rec.LastBackupID != 0 {
		t.Errorf("expected last_backup_id=0, got %d", rec.LastBackupID)
	}
}

func TestFileRepository_MarkBackupSuccess(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	// Create a backup record.
	backupID, _ := backupRepo.Create()

	// Upsert a file.
	now := time.Now().UTC().Truncate(time.Second)
	fileID, err := fileRepo.Upsert("/data/success.txt", 512, now, "hash_success", 20001)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Mark backup success.
	if err := fileRepo.MarkBackupSuccess(fileID, backupID); err != nil {
		t.Fatalf("MarkBackupSuccess: %v", err)
	}

	// Verify.
	rec, _ := fileRepo.GetByPath("/data/success.txt")
	if rec.LastBackupStatus != models.FileBackupStatusSuccess {
		t.Errorf("expected last_backup_status=success, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupError != "" {
		t.Errorf("expected empty last_backup_error, got %q", rec.LastBackupError)
	}
	if rec.LastBackupAt == nil {
		t.Error("expected last_backup_at to be set")
	}
	if rec.LastBackupID != backupID {
		t.Errorf("expected last_backup_id=%d, got %d", backupID, rec.LastBackupID)
	}
}

func TestFileRepository_MarkBackupFailed(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	// Create a backup record.
	backupID, _ := backupRepo.Create()

	// Upsert a file.
	now := time.Now().UTC().Truncate(time.Second)
	fileID, err := fileRepo.Upsert("/data/failed.txt", 2048, now, "hash_failed", 30001)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Mark backup failed.
	errMsg := "encrypt file: write ciphertext for chunk 15357: write /tmp/nas-backup-xxx/396_encrypted.enc: no space left on device"
	if err := fileRepo.MarkBackupFailed(fileID, backupID, errMsg); err != nil {
		t.Fatalf("MarkBackupFailed: %v", err)
	}

	// Verify.
	rec, _ := fileRepo.GetByPath("/data/failed.txt")
	if rec.LastBackupStatus != models.FileBackupStatusFailed {
		t.Errorf("expected last_backup_status=failed, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupError != errMsg {
		t.Errorf("expected last_backup_error=%q, got %q", errMsg, rec.LastBackupError)
	}
	if rec.LastBackupAt == nil {
		t.Error("expected last_backup_at to be set")
	}
	if rec.LastBackupID != backupID {
		t.Errorf("expected last_backup_id=%d, got %d", backupID, rec.LastBackupID)
	}
}

func TestFileRepository_MarkBackup_SuccessThenFailed(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	backupID1, _ := backupRepo.Create()
	backupID2, _ := backupRepo.Create()

	now := time.Now().UTC().Truncate(time.Second)
	fileID, _ := fileRepo.Upsert("/data/flaky.txt", 100, now, "hash_flaky", 40001)

	// First backup succeeds.
	if err := fileRepo.MarkBackupSuccess(fileID, backupID1); err != nil {
		t.Fatalf("MarkBackupSuccess: %v", err)
	}
	rec, _ := fileRepo.GetByPath("/data/flaky.txt")
	if rec.LastBackupStatus != models.FileBackupStatusSuccess {
		t.Errorf("expected success after first backup, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupID != backupID1 {
		t.Errorf("expected last_backup_id=%d, got %d", backupID1, rec.LastBackupID)
	}

	// Second backup fails.
	if err := fileRepo.MarkBackupFailed(fileID, backupID2, "network timeout"); err != nil {
		t.Fatalf("MarkBackupFailed: %v", err)
	}
	rec, _ = fileRepo.GetByPath("/data/flaky.txt")
	if rec.LastBackupStatus != models.FileBackupStatusFailed {
		t.Errorf("expected failed after second backup, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupError != "network timeout" {
		t.Errorf("expected error=network timeout, got %q", rec.LastBackupError)
	}
	if rec.LastBackupID != backupID2 {
		t.Errorf("expected last_backup_id=%d, got %d", backupID2, rec.LastBackupID)
	}
}

func TestFileRepository_MarkBackupFailed_ErrorTruncation(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	backupID, _ := backupRepo.Create()
	now := time.Now().UTC().Truncate(time.Second)
	fileID, _ := fileRepo.Upsert("/data/longerr.txt", 100, now, "hash_longerr", 50001)

	// Error message longer than 1024 chars.
	longMsg := ""
	for i := 0; i < 2000; i++ {
		longMsg += "x"
	}
	if err := fileRepo.MarkBackupFailed(fileID, backupID, longMsg); err != nil {
		t.Fatalf("MarkBackupFailed: %v", err)
	}

	rec, _ := fileRepo.GetByPath("/data/longerr.txt")
	if len(rec.LastBackupError) > 1024 {
		t.Errorf("expected error length <= 1024, got %d", len(rec.LastBackupError))
	}
}

func TestFileRepository_MarkBackup_InvalidFileID(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo

	// MarkBackupSuccess with non-existent file ID should not panic.
	if err := fileRepo.MarkBackupSuccess(99999, 1); err != nil {
		// This is fine - it should either succeed silently or fail with an error.
		t.Logf("MarkBackupSuccess with non-existent ID returned: %v", err)
	}

	// MarkBackupFailed with non-existent file ID should not panic.
	if err := fileRepo.MarkBackupFailed(99999, 1, "test error"); err != nil {
		t.Logf("MarkBackupFailed with non-existent ID returned: %v", err)
	}
}

func TestFileRepository_MarkBackupSuccessBatch(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	backupID, _ := backupRepo.Create()
	now := time.Now().UTC().Truncate(time.Second)

	// Upsert multiple files.
	ids := make([]int64, 3)
	for i, name := range []string{"/data/a.txt", "/data/b.txt", "/data/c.txt"} {
		id, _ := fileRepo.Upsert(name, int64(i+1)*100, now, "hash_batch", 60000+uint64(i))
		ids[i] = id
	}

	// Batch mark success.
	if err := fileRepo.MarkBackupSuccessBatch(ids, backupID); err != nil {
		t.Fatalf("MarkBackupSuccessBatch: %v", err)
	}

	// Verify all marked.
	for _, name := range []string{"/data/a.txt", "/data/b.txt", "/data/c.txt"} {
		rec, _ := fileRepo.GetByPath(name)
		if rec.LastBackupStatus != models.FileBackupStatusSuccess {
			t.Errorf("expected success for %q, got %q", name, rec.LastBackupStatus)
		}
		if rec.LastBackupID != backupID {
			t.Errorf("expected backup_id=%d for %q, got %d", backupID, name, rec.LastBackupID)
		}
	}
}

func TestFileRepository_MarkBackupSuccessBatch_Empty(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo

	// Empty batch should be a no-op.
	if err := fileRepo.MarkBackupSuccessBatch(nil, 1); err != nil {
		t.Fatalf("MarkBackupSuccessBatch(nil): %v", err)
	}
	if err := fileRepo.MarkBackupSuccessBatch([]int64{}, 1); err != nil {
		t.Fatalf("MarkBackupSuccessBatch(empty): %v", err)
	}
}

func TestFileRepository_GetByHash_WithBackupStatus(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo
	backupRepo := database.BackupRepo

	backupID, _ := backupRepo.Create()
	now := time.Now().UTC().Truncate(time.Second)

	// Upsert a file and mark it as backed up.
	fileID, _ := fileRepo.Upsert("/data/deduped.txt", 200, now, "dedup_hash", 70001)
	_ = fileRepo.MarkBackupSuccess(fileID, backupID)

	// GetByHash should return the file with status.
	files, err := fileRepo.GetByHash("dedup_hash")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least 1 file")
	}
	found := false
	for _, f := range files {
		if f.Path == "/data/deduped.txt" {
			found = true
			if f.LastBackupStatus != models.FileBackupStatusSuccess {
				t.Errorf("expected last_backup_status=success, got %q", f.LastBackupStatus)
			}
		}
	}
	if !found {
		t.Error("expected /data/deduped.txt in hash results")
	}
}

func TestFileRepository_scanFileRecord_EmptyBackupStatus(t *testing.T) {
	database := setupFileTestDB(t)
	defer database.Close()
	fileRepo := database.FileRepo

	now := time.Now().UTC().Truncate(time.Second)
	fileID, _ := fileRepo.Upsert("/data/never_backed_up.txt", 300, now, "hash_never", 80001)

	rec, _ := fileRepo.GetByPath("/data/never_backed_up.txt")
	if rec.ID != fileID {
		t.Errorf("expected id=%d, got %d", fileID, rec.ID)
	}
	if rec.LastBackupStatus != "" {
		t.Errorf("expected empty last_backup_status, got %q", rec.LastBackupStatus)
	}
	if rec.LastBackupError != "" {
		t.Errorf("expected empty last_backup_error, got %q", rec.LastBackupError)
	}
	if rec.LastBackupAt != nil {
		t.Error("expected nil last_backup_at")
	}
	if rec.LastBackupID != 0 {
		t.Errorf("expected last_backup_id=0, got %d", rec.LastBackupID)
	}
}