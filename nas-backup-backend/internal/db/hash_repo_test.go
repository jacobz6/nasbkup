package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/models"
)

func setupHashTestDB(t *testing.T) *Database {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available (CGO required): %v", err)
	}
	return database
}

// TestHashRepository_HasRefCountMismatches_CountsBackupFiles guards against the
// "needs_reconcile" false-positive regression. ref_count is maintained per
// backup session (one increment per backup_files reference), so a single file
// backed up across multiple sessions must NOT be flagged as a mismatch even
// though it appears once in the active files table.
func TestHashRepository_HasRefCountMismatches_CountsBackupFiles(t *testing.T) {
	database := setupHashTestDB(t)
	defer database.Close()

	now := time.Now().UTC().Truncate(time.Second)
	fileID, err := database.FileRepo.Upsert("/data/a.txt", 100, now, "hash_multi", 91001)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	storageKey := "data/00/hash_multi.enc"
	bf := models.BackupFileRecord{
		FileID: fileID, StorageKey: storageKey,
		CompressType: "none", OriginalSize: 100, StoredSize: 100,
	}

	// One file, same content referenced by TWO backup sessions.
	for i := 0; i < 2; i++ {
		bid, err := database.BackupRepo.Create()
		if err != nil {
			t.Fatalf("Create backup #%d: %v", i, err)
		}
		bf.BackupID = bid
		if err := database.BackupRepo.AddBackupFile(&bf); err != nil {
			t.Fatalf("AddBackupFile #%d: %v", i, err)
		}
	}

	// ref_count mirrors the backup_files references (2 here): first reference
	// via Upsert, second via IncrementRef (dedup-skip path).
	if _, err := database.HashRepo.Upsert("hash_multi", 100, storageKey); err != nil {
		t.Fatalf("Upsert hash: %v", err)
	}
	if err := database.HashRepo.IncrementRef("hash_multi"); err != nil {
		t.Fatalf("IncrementRef: %v", err)
	}

	// Active files = 1, backup_files = 2, ref_count = 2. Only the backup_files
	// count matches ref_count; the old active-file comparison would wrongly
	// report a mismatch.
	mismatch, err := database.HashRepo.HasRefCountMismatches()
	if err != nil {
		t.Fatalf("HasRefCountMismatches: %v", err)
	}
	if mismatch {
		t.Fatal("expected NO ref_count mismatch: ref_count=2 equals backup_files count=2")
	}

	// Sanity: if ref_count truly drifts from backup_files, it IS reported.
	if err := database.HashRepo.SetRefCount("hash_multi", 5); err != nil {
		t.Fatalf("SetRefCount: %v", err)
	}
	mismatch, err = database.HashRepo.HasRefCountMismatches()
	if err != nil {
		t.Fatalf("HasRefCountMismatches (drift): %v", err)
	}
	if !mismatch {
		t.Fatal("expected mismatch after setting ref_count=5 vs backup_files=2")
	}
}