package dedup

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/scanner"
)

func setupTestDB(t *testing.T) (*db.HashRepository, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available: %v", err)
	}
	return database.HashRepo, func() { database.Close() }
}

func TestDeduplicateNewFiles(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	dedup := NewDeduplicator(hashRepo)

	changes := []scanner.FileChange{
		{Path: "/data/file1.txt", ChangeType: scanner.Added, Size: 100, ModTime: time.Now(), NewHash: "hash1"},
		{Path: "/data/file2.txt", ChangeType: scanner.Added, Size: 200, ModTime: time.Now(), NewHash: "hash2"},
	}

	result, err := dedup.Deduplicate(changes)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.ToUpload) != 2 {
		t.Errorf("expected 2 files to upload, got %d", len(result.ToUpload))
	}
	if len(result.Skipped) != 0 {
		t.Errorf("expected 0 skipped files, got %d", len(result.Skipped))
	}
	if result.TotalSaved != 0 {
		t.Errorf("expected 0 bytes saved, got %d", result.TotalSaved)
	}
}

func TestDeduplicateAllExisting(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	hashRepo.Upsert("hash_ex1", 500, "key1")
	hashRepo.Upsert("hash_ex2", 300, "key2")

	dedup := NewDeduplicator(hashRepo)

	changes := []scanner.FileChange{
		{Path: "/data/dup1.txt", ChangeType: scanner.Added, Size: 500, NewHash: "hash_ex1"},
		{Path: "/data/dup2.txt", ChangeType: scanner.Modified, Size: 300, NewHash: "hash_ex2"},
	}

	result, err := dedup.Deduplicate(changes)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.ToUpload) != 0 {
		t.Errorf("expected 0 files to upload, got %d", len(result.ToUpload))
	}
	if len(result.Skipped) != 2 {
		t.Errorf("expected 2 skipped files, got %d", len(result.Skipped))
	}
	if result.TotalSaved != 800 {
		t.Errorf("expected 800 bytes saved, got %d", result.TotalSaved)
	}
}

func TestDeduplicateEmptyChanges(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	dedup := NewDeduplicator(hashRepo)

	result, err := dedup.Deduplicate([]scanner.FileChange{})
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.ToUpload) != 0 || len(result.Skipped) != 0 {
		t.Error("expected empty result")
	}
}

func TestDeduplicateSkipsNonAddedModified(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	dedup := NewDeduplicator(hashRepo)

	changes := []scanner.FileChange{
		{Path: "/data/deleted.txt", ChangeType: scanner.Deleted, Size: 100, NewHash: "hash_d"},
		{Path: "/data/new.txt", ChangeType: scanner.Added, Size: 100, NewHash: "hash_n"},
	}

	result, err := dedup.Deduplicate(changes)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.ToUpload) != 1 {
		t.Errorf("expected 1 file to upload, got %d", len(result.ToUpload))
	}
}

func TestDeduplicateEmptyHash(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	dedup := NewDeduplicator(hashRepo)

	changes := []scanner.FileChange{
		{Path: "/data/nohash.txt", ChangeType: scanner.Added, Size: 100, NewHash: ""},
	}

	result, err := dedup.Deduplicate(changes)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.ToUpload) != 1 {
		t.Errorf("expected 1 file to upload for empty hash, got %d", len(result.ToUpload))
	}
	if !result.ToUpload[0].IsNew {
		t.Error("file with empty hash should be marked as new")
	}
}

func TestDeduplicateStorageKeyInResult(t *testing.T) {
	hashRepo, cleanup := setupTestDB(t)
	defer cleanup()

	hashRepo.Upsert("hash_key", 500, "storage/key.enc")

	dedup := NewDeduplicator(hashRepo)

	changes := []scanner.FileChange{
		{Path: "/data/dup.txt", ChangeType: scanner.Added, Size: 500, NewHash: "hash_key"},
	}

	result, err := dedup.Deduplicate(changes)
	if err != nil {
		t.Fatalf("Deduplicate failed: %v", err)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("expected 1 skipped file, got %d", len(result.Skipped))
	}
	if result.Skipped[0].ExistingStorageKey != "storage/key.enc" {
		t.Errorf("expected storage key storage/key.enc, got %q", result.Skipped[0].ExistingStorageKey)
	}
}

func TestDedupResultEntry(t *testing.T) {
	entry := DedupFileEntry{
		IsNew:      true,
		StorageKey: "test/key.enc",
	}
	if !entry.IsNew {
		t.Error("expected IsNew to be true")
	}
	if entry.StorageKey != "test/key.enc" {
		t.Errorf("expected StorageKey test/key.enc, got %q", entry.StorageKey)
	}
}

func TestDedupSkippedEntry(t *testing.T) {
	skipped := DedupSkippedEntry{
		Path:               "/data/file.txt",
		Hash:               "abc123",
		ExistingStorageKey: "storage/key.enc",
		Reason:             "content already stored",
	}
	if skipped.Path != "/data/file.txt" {
		t.Errorf("expected Path /data/file.txt, got %q", skipped.Path)
	}
	if skipped.Reason != "content already stored" {
		t.Errorf("expected Reason, got %q", skipped.Reason)
	}
}
