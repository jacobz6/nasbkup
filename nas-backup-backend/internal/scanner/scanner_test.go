package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
)

func setupTestDB(t *testing.T) (*db.FileRepository, *db.ConfigRepository, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.NewDatabase(dbPath)
	if err != nil {
		t.Skipf("SQLite not available: %v", err)
	}
	return database.FileRepo, database.ConfigRepo, func() { database.Close() }
}

func enableDir(t *testing.T, configRepo *db.ConfigRepository, path string) {
	t.Helper()
	configRepo.AddBackupDirectory(models.BackupDirectory{
		Path:      path,
		Recursive: true,
		Enabled:   true,
	})
}

func addExclusion(t *testing.T, configRepo *db.ConfigRepository, pattern, ruleType string) {
	t.Helper()
	configRepo.AddExclusionRule(models.ExclusionRule{
		Pattern:  pattern,
		RuleType: ruleType,
		Enabled:  true,
	})
}

func TestScanEmptyDirectory(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(result.Files))
	}
}

func TestScanSingleFile(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("hello scanner")
	os.WriteFile(testPath, testContent, 0644)
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Path != testPath {
		t.Errorf("expected path %q, got %q", testPath, f.Path)
	}
	if f.Size != int64(len(testContent)) {
		t.Errorf("expected size %d, got %d", len(testContent), f.Size)
	}
	if f.Hash == "" {
		t.Error("expected non-empty hash")
	}
}

func TestScanNestedDirectories(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "a", "b", "c"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "root.txt"), []byte("root"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a", "level1.txt"), []byte("level1"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a", "b", "level2.txt"), []byte("level2"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "a", "b", "c", "level3.txt"), []byte("level3"), 0644)
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 4 {
		t.Errorf("expected 4 files, got %d", len(result.Files))
	}
}

func TestScanWithExcludedExtensions(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("text"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file.mp4"), []byte("video"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file.jpg"), []byte("image"), 0644)
	enableDir(t, configRepo, tmpDir)
	addExclusion(t, configRepo, ".mp4", "extension")
	addExclusion(t, configRepo, ".jpg", "extension")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file (txt only), got %d", len(result.Files))
	}
}

func TestScanWithExcludedDirs(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "src"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "src", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg.js"), []byte("module"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("readme"), 0644)
	enableDir(t, configRepo, tmpDir)
	addExclusion(t, configRepo, "node_modules", "directory")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	for _, f := range result.Files {
		if filepath.Base(filepath.Dir(f.Path)) == "node_modules" {
			t.Errorf("excluded file %q in results", f.Path)
		}
	}
}

func TestScanWithExcludedPatterns(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "report_2024.pdf"), []byte("pdf"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "report_2023.pdf"), []byte("pdf old"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "data.csv"), []byte("csv"), 0644)
	enableDir(t, configRepo, tmpDir)
	addExclusion(t, configRepo, "*2024*", "pattern")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	for _, f := range result.Files {
		if filepath.Base(f.Path) == "report_2024.pdf" {
			t.Error("excluded *2024* file in results")
		}
	}
}

func TestScanMultipleDirectories(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir1, "file1.txt"), []byte("c1"), 0644)
	os.WriteFile(filepath.Join(tmpDir2, "file2.txt"), []byte("c2"), 0644)
	enableDir(t, configRepo, tmpDir1)
	enableDir(t, configRepo, tmpDir2)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(result.Files))
	}
}

func TestScanNonExistentDirectory(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	enableDir(t, configRepo, "/nonexistent/path")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan should not fail: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected error for nonexistent directory")
	}
}

func TestScanSymlink(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	realFile := filepath.Join(tmpDir, "real.txt")
	os.WriteFile(realFile, []byte("real"), 0644)
	linkFile := filepath.Join(tmpDir, "link.txt")
	if err := os.Symlink(realFile, linkFile); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file (real only), got %d", len(result.Files))
	}
}

func TestScanCircularSymlinks(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.Symlink(filepath.Join(tmpDir, "b"), filepath.Join(tmpDir, "a"))
	os.Symlink(filepath.Join(tmpDir, "a"), filepath.Join(tmpDir, "b"))
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	_, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan should handle circular symlinks: %v", err)
	}
}

func TestScanWithSizeFilters(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "small.txt"), []byte("sm"), 0644)
	largeData := make([]byte, 2048)
	os.WriteFile(filepath.Join(tmpDir, "large.bin"), largeData, 0644)
	enableDir(t, configRepo, tmpDir)
	configRepo.Set("scanner.max_file_size", "0")
	configRepo.Set("scanner.min_file_size", "100")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file (large.bin), got %d", len(result.Files))
	}
}

func TestHashConsistency(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	content := []byte("deterministic hash test")
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), content, 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), content, 0644)
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(result.Files))
	}
	if result.Files[0].Hash != result.Files[1].Hash {
		t.Errorf("same content different hashes: %q vs %q",
			result.Files[0].Hash, result.Files[1].Hash)
	}
}

func TestHashDifferentContent(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content A"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content B"), 0644)
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if result.Files[0].Hash == result.Files[1].Hash {
		t.Errorf("different content same hash: %q", result.Files[0].Hash)
	}
}

func TestScanEmptyFile(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "empty.txt"), []byte{}, 0644)
	enableDir(t, configRepo, tmpDir)

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	if result.Files[0].Size != 0 {
		t.Errorf("expected size 0, got %d", result.Files[0].Size)
	}
	if result.Files[0].Hash == "" {
		t.Error("empty file should have a valid hash")
	}
}

func TestScanErrorHandling(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	enableDir(t, configRepo, "/nonexistent")

	scanner := NewScanner(fileRepo, configRepo)
	result, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan should not fail: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors for nonexistent directory")
	}
}

func TestScannerConstructor(t *testing.T) {
	fileRepo, configRepo, cleanup := setupTestDB(t)
	defer cleanup()

	scanner := NewScanner(fileRepo, configRepo)
	if scanner == nil {
		t.Fatal("NewScanner returned nil")
	}
}

func TestScanResultStruct(t *testing.T) {
	result := ScanResult{
		Files:         []FileRecord{{Path: "/data/file.txt", Size: 100}},
		Errors:        []string{"error1"},
		TotalFiles:    1,
		TotalSize:     100,
		ExcludedCount: 5,
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(result.Files))
	}
	if result.ExcludedCount != 5 {
		t.Errorf("expected ExcludedCount 5, got %d", result.ExcludedCount)
	}
}

func TestFileRecordStruct(t *testing.T) {
	rec := FileRecord{
		Path:    "/data/file.txt",
		Size:    1024,
		ModTime: 1704067200,
		Hash:    "abc123",
	}
	if rec.Path != "/data/file.txt" {
		t.Errorf("expected Path %q, got %q", "/data/file.txt", rec.Path)
	}
	if rec.Hash != "abc123" {
		t.Errorf("expected Hash %q, got %q", "abc123", rec.Hash)
	}
}
