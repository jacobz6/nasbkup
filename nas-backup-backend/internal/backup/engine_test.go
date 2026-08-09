package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nas-backup/internal/config"
)

// createTestConfig builds a minimal config for testing with the given temp dir.
func createTestConfig(tmpDir string) *config.AppConfig {
	return &config.AppConfig{
		Backup: config.BackupConfig{
			TempDir: tmpDir,
		},
	}
}

// ── formatSize ─────────────────────────────────────────────────────────────

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0B"},
		{1, "1B"},
		{500, "500B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.00MB"},
		{2 * 1024 * 1024, "2.00MB"},
		{1024 * 1024 * 1024, "1.00GB"},
		{5 * 1024 * 1024 * 1024, "5.00GB"},
		{3*1024*1024*1024 + 512*1024*1024, "3.50GB"},
	}
	for _, tc := range tests {
		got := formatSize(tc.input)
		if got != tc.want {
			t.Errorf("formatSize(%d) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// ── generateStorageKey ─────────────────────────────────────────────────────

func TestGenerateStorageKey(t *testing.T) {
	e := &Engine{}

	// Key format: data/<hash-prefix-2>/<hash>.enc
	key := e.generateStorageKey("abcdef1234567890")
	want := "data/ab/abcdef1234567890.enc"
	if key != want {
		t.Errorf("expected %q, got %q", want, key)
	}
}

func TestGenerateStorageKey_ShortHash(t *testing.T) {
	e := &Engine{}
	// With a single-char hash, the prefix should be "00".
	key := e.generateStorageKey("a")
	want := "data/00/a.enc"
	if key != want {
		t.Errorf("expected %q, got %q", want, key)
	}
}

// ── availableDiskSpace ─────────────────────────────────────────────────────

func TestAvailableDiskSpace(t *testing.T) {
	// Use the current directory as a reasonable test target.
	free, err := availableDiskSpace(".")
	if err != nil {
		t.Fatalf("availableDiskSpace(.): %v", err)
	}
	if free <= 0 {
		t.Errorf("expected positive free space, got %d", free)
	}
	t.Logf("free space in .: %d bytes (%s)", free, formatSize(free))
}

func TestAvailableDiskSpace_NonExistentDir(t *testing.T) {
	_, err := availableDiskSpace("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}

func TestAvailableDiskSpace_TempDir(t *testing.T) {
	tmpDir := t.TempDir()
	free, err := availableDiskSpace(tmpDir)
	if err != nil {
		t.Fatalf("availableDiskSpace(tempDir): %v", err)
	}
	if free <= 0 {
		t.Errorf("expected positive free space, got %d", free)
	}
}

// ── TempDir configuration ──────────────────────────────────────────────────

func TestEngine_TempDir(t *testing.T) {
	// Test that the config's TempDir() is used correctly.
	tmpDir := t.TempDir()

	// Create a minimal config with the temp dir set.
	cfg := createTestConfig(tmpDir)
	got := cfg.TempDir()
	if got != tmpDir {
		t.Errorf("expected TempDir()=%q, got %q", tmpDir, got)
	}

	// Verify the directory is usable.
	testFile := filepath.Join(got, "test_write.tmp")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("write to temp dir: %v", err)
	}
	os.Remove(testFile)
}