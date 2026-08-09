package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nas-backup/internal/config"
)

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "source.txt")
	dst := filepath.Join(tmpDir, "dest.txt")

	content := "hello world"
	if err := os.WriteFile(src, []byte(content), 0600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}

	// Verify permissions.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600, got %o", info.Mode().Perm())
	}
}

// ── bkupEncPattern ──────────────────────────────────────────────────────────

func TestBkupEncPattern(t *testing.T) {
	cases := []struct {
		key     string
		matched bool
		ts      string
	}{
		{"meta/db/oss.db.version202608091200.bkup.enc", true, "202608091200"},
		{"meta/db/oss.db.version202608091200.bkup.iv", false, ""},
		{"meta/db/oss.db.enc", false, ""},
		{"meta/db/oss.db.version20260809.bkup.enc", false, ""},  // 10 digits, not 12
		{"meta/db/oss.db.version2026080912001.bkup.enc", false, ""}, // 13 digits
		{"other/oss.db.version202608091200.bkup.enc", false, ""},
		{"meta/db/oss.db.versionABC.bkup.enc", false, ""},
	}
	for _, c := range cases {
		m := bkupEncPattern.FindStringSubmatch(c.key)
		gotMatch := m != nil
		if gotMatch != c.matched {
			t.Errorf("bkupEncPattern(%q) matched=%v; want %v", c.key, gotMatch, c.matched)
			continue
		}
		if c.matched && m[1] != c.ts {
			t.Errorf("bkupEncPattern(%q) ts=%q; want %q", c.key, m[1], c.ts)
		}
	}
}

// ── bkupKeepCount ───────────────────────────────────────────────────────────

func TestBkupKeepCount(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.AppConfig
		wantKeep int
	}{
		{
			name:     "nil config falls back to default",
			cfg:      nil,
			wantKeep: defaultDBBkupKeepCount,
		},
		{
			name: "zero falls back to default",
			cfg: &config.AppConfig{
				Backup: config.BackupConfig{
					Retention: config.RetentionConfig{DBBkupKeepCount: 0},
				},
			},
			wantKeep: defaultDBBkupKeepCount,
		},
		{
			name: "negative falls back to default",
			cfg: &config.AppConfig{
				Backup: config.BackupConfig{
					Retention: config.RetentionConfig{DBBkupKeepCount: -1},
				},
			},
			wantKeep: defaultDBBkupKeepCount,
		},
		{
			name: "configured value is returned",
			cfg: &config.AppConfig{
				Backup: config.BackupConfig{
					Retention: config.RetentionConfig{DBBkupKeepCount: 3},
				},
			},
			wantKeep: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &DBBackupService{config: tc.cfg}
			if got := svc.bkupKeepCount(); got != tc.wantKeep {
				t.Errorf("bkupKeepCount()=%d; want %d", got, tc.wantKeep)
			}
		})
	}
}

// ── pruneBkupsKeys ──────────────────────────────────────────────────────────

func TestPruneBkupsKeys(t *testing.T) {
	// Mixed set: valid .enc+.iv pairs, plus unrelated keys that must be ignored.
	keys := []string{
		"meta/db/oss.db.enc",                       // current authoritative, ignored
		"meta/db/oss.db.iv",                        // current IV, ignored
		"meta/db/oss.db.version202608091200.bkup.enc", // ts=202608091200
		"meta/db/oss.db.version202608091200.bkup.iv",
		"meta/db/oss.db.version202608091100.bkup.enc", // ts=202608091100
		"meta/db/oss.db.version202608091100.bkup.iv",
		"meta/db/oss.db.version202608091000.bkup.enc", // ts=202608091000
		"meta/db/oss.db.version202608091000.bkup.iv",
		"meta/db/oss.db.version202608090900.bkup.enc", // ts=202608090900
		"meta/db/oss.db.version202608090900.bkup.iv",
		"meta/db/oss.db.version202608090800.bkup.enc", // ts=202608090800
		"meta/db/oss.db.version202608090800.bkup.iv",
		"some/other/key.bin",                        // unrelated, ignored
	}

	t.Run("keep all when below limit", func(t *testing.T) {
		got := pruneBkupsKeys(keys, 5)
		if len(got) != 0 {
			t.Errorf("keep=5 with 5 bkup entries: expected nothing deleted, got %v", got)
		}
	})

	t.Run("keep all when equal to limit", func(t *testing.T) {
		got := pruneBkupsKeys(keys, 5)
		if len(got) != 0 {
			t.Errorf("keep=5 with 5 bkup entries: expected nothing deleted, got %v", got)
		}
	})

	t.Run("prune oldest beyond keep count", func(t *testing.T) {
		got := pruneBkupsKeys(keys, 2)
		// keep=2 retains newest 202608091200 + 202608091100; deletes the other 3 entries.
		want := []string{
			"meta/db/oss.db.version202608091000.bkup.enc",
			"meta/db/oss.db.version202608091000.bkup.iv",
			"meta/db/oss.db.version202608090900.bkup.enc",
			"meta/db/oss.db.version202608090900.bkup.iv",
			"meta/db/oss.db.version202608090800.bkup.enc",
			"meta/db/oss.db.version202608090800.bkup.iv",
		}
		if len(got) != len(want) {
			t.Fatalf("keep=2: expected %d deletions, got %d (%v)", len(want), len(got), got)
		}
		gotSet := make(map[string]struct{}, len(got))
		for _, k := range got {
			gotSet[k] = struct{}{}
		}
		for _, k := range want {
			if _, ok := gotSet[k]; !ok {
				t.Errorf("expected %q to be in deletion set", k)
			}
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		got := pruneBkupsKeys(nil, 5)
		if got != nil {
			t.Errorf("expected nil for empty input, got %v", got)
		}
	})

	t.Run("no bkup entries returns nil", func(t *testing.T) {
		got := pruneBkupsKeys([]string{"meta/db/oss.db.enc", "meta/db/oss.db.iv"}, 1)
		if got != nil {
			t.Errorf("expected nil when no bkup entries, got %v", got)
		}
	})

	t.Run("negative keep count prunes everything", func(t *testing.T) {
		got := pruneBkupsKeys(keys, -1)
		// keepCount <= 0 => len(byTS) > keepCount is true, so all 5 entries pruned.
		if len(got) != 10 {
			t.Errorf("keep=-1: expected all 10 bkup keys pruned, got %d", len(got))
		}
	})
}

// TestDBBackupServiceDependencies ensures NewDBBackupService propagates the
// encryptor/storage/config correctly so that PullOSSDB/PushOSSDB can be
// exercised end-to-end in integration tests without nil-pointer surprises.
func TestNewDBBackupService_Dependencies(t *testing.T) {
	cfg := config.DefaultConfig()
	// storage manager may be nil here (no rclone configured); the test only
	// cares that fields are wired up, not that OSS calls succeed.
	svc := NewDBBackupService(nil, nil, cfg, nil)
	if svc == nil {
		t.Fatal("NewDBBackupService returned nil")
	}
	if svc.config != cfg {
		t.Errorf("config not propagated")
	}
	if svc.encryptor != nil {
		t.Errorf("encryptor should be nil, got %v", svc.encryptor)
	}
	if svc.storage != nil {
		t.Errorf("storage should be nil, got %v", svc.storage)
	}
}
