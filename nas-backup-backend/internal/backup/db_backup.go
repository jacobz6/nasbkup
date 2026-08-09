// Package backup provides database backup/restore service for disaster recovery.
// This module encrypts the local SQLite database and uploads it to OSS so that
// in a total-loss scenario only the master.key and rclone.conf are needed to
// fully bootstrap a new NAS from cloud backups.
//
// The OSS bucket holds a single authoritative DB snapshot (oss.db.enc +
// oss.db.iv). On every push the previous snapshot is renamed to a .bkup
// (kept up to db_bkup_keep_count, default 5) so accidental pushes or
// corruption can be rolled back. There is no multi-version history: the local
// data/nas-backup.db is the working copy of the same logical database.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/storage"
)

const (
	// ossDBPrefix is the OSS key prefix under which the authoritative DB
	// snapshot and its .bkup history live.
	ossDBPrefix = "meta/db"

	// ossDBEncKey is the OSS object key for the current authoritative
	// encrypted DB snapshot.
	ossDBEncKey = "meta/db/oss.db.enc"

	// ossDBIVKey is the OSS object key for the IV that decrypts ossDBEncKey.
	ossDBIVKey = "meta/db/oss.db.iv"

	// dbBkupTimestampLayout is the timestamp format embedded in .bkup names.
	// Minute precision is enough — pushes are at most a few per day.
	dbBkupTimestampLayout = "200601021504"

	// defaultDBBkupKeepCount is used when the config value is unset or invalid.
	defaultDBBkupKeepCount = 5
)

// DBBackupService handles encrypted database snapshots to cloud storage.
type DBBackupService struct {
	encryptor *crypto.Encryptor
	storage   *storage.StorageManager
	dbPath    string       // path to the local SQLite database file
	dbConn    *sql.DB      // raw DB connection for WAL checkpoint (may be nil)
	config    *config.AppConfig
	logger    *slog.Logger
}

// NewDBBackupService creates a new DBBackupService.
// dbConn is the live *sql.DB connection used to checkpoint the WAL before
// copying the database file. It may be nil (e.g. during initial bootstrap
// before the DB has been opened), in which case WAL checkpoint is skipped.
func NewDBBackupService(enc *crypto.Encryptor, stor *storage.StorageManager, cfg *config.AppConfig, dbConn *sql.DB) *DBBackupService {
	return &DBBackupService{
		encryptor: enc,
		storage:   stor,
		dbPath:    cfg.Database.Path,
		dbConn:    dbConn,
		config:    cfg,
		logger:    slog.Default(),
	}
}

// bkupKeepCount returns the configured .bkup retention count, falling back to
// the default when the config value is non-positive.
func (s *DBBackupService) bkupKeepCount() int {
	if s.config == nil || s.config.Backup.Retention.DBBkupKeepCount <= 0 {
		return defaultDBBkupKeepCount
	}
	return s.config.Backup.Retention.DBBkupKeepCount
}

// OSSDBExists reports whether OSS currently holds an authoritative DB snapshot
// (oss.db.enc). Existence of the IV is assumed when the .enc is present.
func (s *DBBackupService) OSSDBExists(ctx context.Context) (bool, error) {
	exists, err := s.storage.Exists(ctx, ossDBEncKey)
	if err != nil {
		return false, fmt.Errorf("check oss.db.enc existence: %w", err)
	}
	return exists, nil
}

// PullOSSDB downloads the authoritative DB snapshot from OSS, decrypts it, and
// atomically overwrites the local database file.
//
// Returns (false, nil) when OSS has no oss.db.enc — this is the first-time
// deployment case; the caller should initialize an empty local DB instead.
//
// PullOSSDB performs a pure file-level swap. It does NOT close or reopen the
// *sql.DB connection — the caller is responsible for DB lifecycle management
// (close before pull, reopen after) when the DB is already open. The engine
// wraps this with restore_jobs preservation in pullOSSDBForBackup.
func (s *DBBackupService) PullOSSDB(ctx context.Context) (bool, error) {
	if s.encryptor == nil || s.storage == nil {
		return false, fmt.Errorf("pull oss.db requires both encryptor and storage manager")
	}

	exists, err := s.OSSDBExists(ctx)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}

	tmpDir := os.TempDir()
	localEnc := filepath.Join(tmpDir, "oss.db.pull.enc")
	localIV := filepath.Join(tmpDir, "oss.db.pull.iv")
	localDec := filepath.Join(tmpDir, "oss.db.pull.dec")
	defer os.Remove(localEnc)
	defer os.Remove(localIV)
	defer os.Remove(localDec)

	if err := s.storage.DownloadWithThaw(ctx, ossDBEncKey, localEnc, 30*time.Minute, 10*time.Second); err != nil {
		return false, fmt.Errorf("download oss.db.enc: %w", err)
	}
	if err := s.storage.DownloadWithThaw(ctx, ossDBIVKey, localIV, 30*time.Minute, 10*time.Second); err != nil {
		return false, fmt.Errorf("download oss.db.iv: %w", err)
	}

	ivData, err := os.ReadFile(localIV)
	if err != nil {
		return false, fmt.Errorf("read oss.db.iv: %w", err)
	}
	iv := strings.TrimSpace(string(ivData))

	if err := s.encryptor.DecryptFile(localEnc, localDec, iv); err != nil {
		return false, fmt.Errorf("decrypt oss.db: %w", err)
	}

	// Ensure the target directory exists (first-time bootstrap on a fresh host).
	if dir := filepath.Dir(s.dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("create db directory %s: %w", dir, err)
		}
	}

	// Remove any stale -wal/-shm sidecars so the swapped-in DB is opened clean.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(s.dbPath + suffix)
	}

	// Atomic-ish swap: rename decrypted file over the local DB path.
	if err := os.Rename(localDec, s.dbPath); err != nil {
		return false, fmt.Errorf("overwrite local db: %w", err)
	}

	s.logger.Info("pulled oss.db from OSS", "path", s.dbPath)
	return true, nil
}

// PushOSSDB uploads the local database as the new authoritative oss.db in OSS.
// The previous oss.db is renamed to a timestamped .bkup before the new one is
// uploaded, and superseded .bkup entries beyond the configured keep-count are
// pruned.
//
// PushOSSDB is a no-op when the local DB file is missing.
func (s *DBBackupService) PushOSSDB(ctx context.Context) error {
	if s.encryptor == nil || s.storage == nil {
		return fmt.Errorf("push oss.db requires both encryptor and storage manager")
	}
	if _, err := os.Stat(s.dbPath); err != nil {
		return fmt.Errorf("local database file not found: %s: %w", s.dbPath, err)
	}

	// Step 1: checkpoint WAL so all committed transactions are in the main file.
	if s.dbConn != nil {
		if _, err := s.dbConn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			s.logger.Warn("wal checkpoint failed, snapshot may be incomplete", "error", err)
		}
	}

	// Step 2: integrity check — refuse to upload a corrupt DB.
	if s.dbConn != nil {
		var ok string
		if err := s.dbConn.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil {
			return fmt.Errorf("integrity_check query failed: %w", err)
		}
		if ok != "ok" {
			return fmt.Errorf("integrity_check failed: %s", ok)
		}
	}

	tmpDir := os.TempDir()
	localEnc := filepath.Join(tmpDir, "oss.db.push.enc")
	localIV := filepath.Join(tmpDir, "oss.db.push.iv")
	localCopy := filepath.Join(tmpDir, "oss.db.push.copy")
	defer os.Remove(localEnc)
	defer os.Remove(localIV)
	defer os.Remove(localCopy)

	// Step 3: stream-copy the DB file (avoids loading the whole file into memory).
	if err := copyFile(s.dbPath, localCopy); err != nil {
		return fmt.Errorf("copy database for encryption: %w", err)
	}

	// Step 4: encrypt the copy.
	iv, err := s.encryptor.EncryptFile(localCopy, localEnc)
	if err != nil {
		return fmt.Errorf("encrypt database: %w", err)
	}
	if err := os.WriteFile(localIV, []byte(iv), 0o600); err != nil {
		return fmt.Errorf("write IV file: %w", err)
	}

	// Step 5: rename the previous authoritative snapshot to a .bkup (if any).
	if prevExists, pErr := s.OSSDBExists(ctx); pErr == nil && prevExists {
		if err := s.archivePreviousOSSDB(ctx); err != nil {
			// Non-fatal: the new snapshot can still be uploaded; the old one
			// will simply not be retained as a .bkup.
			s.logger.Warn("failed to archive previous oss.db", "error", err)
		}
	} else if pErr != nil {
		s.logger.Warn("failed to check previous oss.db existence", "error", pErr)
	}

	// Step 6: upload the new .enc and .iv. If the .iv upload fails we must
	// delete the orphaned .enc to avoid leaving an unrecoverable snapshot.
	s.logger.Info("uploading oss.db", "key", ossDBEncKey)
	if err := s.storage.Upload(ctx, localEnc, ossDBEncKey); err != nil {
		return fmt.Errorf("upload oss.db.enc: %w", err)
	}
	if err := s.storage.Upload(ctx, localIV, ossDBIVKey); err != nil {
		s.logger.Error("failed to upload oss.db.iv, deleting orphaned oss.db.enc", "error", err)
		_ = s.storage.Delete(ctx, ossDBEncKey)
		return fmt.Errorf("upload oss.db.iv (orphaned .enc cleaned up): %w", err)
	}

	// Step 7: prune superseded .bkup entries beyond the keep-count.
	if err := s.pruneBkups(ctx); err != nil {
		s.logger.Warn("failed to prune oss.db .bkup entries", "error", err)
	}

	s.logger.Info("pushed oss.db to OSS")
	return nil
}

// archivePreviousOSSDB downloads the current oss.db.enc/.iv and re-uploads them
// under a timestamped .bkup name, preserving the previous snapshot as a
// rollback point.
func (s *DBBackupService) archivePreviousOSSDB(ctx context.Context) error {
	ts := time.Now().UTC().Format(dbBkupTimestampLayout)
	bkupEnc := fmt.Sprintf("%s/oss.db.version%s.bkup.enc", ossDBPrefix, ts)
	bkupIV := fmt.Sprintf("%s/oss.db.version%s.bkup.iv", ossDBPrefix, ts)

	tmpDir := os.TempDir()
	prevEnc := filepath.Join(tmpDir, "oss.db.prev.enc")
	prevIV := filepath.Join(tmpDir, "oss.db.prev.iv")
	defer os.Remove(prevEnc)
	defer os.Remove(prevIV)

	if err := s.storage.DownloadWithThaw(ctx, ossDBEncKey, prevEnc, 30*time.Minute, 10*time.Second); err != nil {
		return fmt.Errorf("download previous oss.db.enc: %w", err)
	}
	if err := s.storage.DownloadWithThaw(ctx, ossDBIVKey, prevIV, 30*time.Minute, 10*time.Second); err != nil {
		return fmt.Errorf("download previous oss.db.iv: %w", err)
	}
	if err := s.storage.Upload(ctx, prevEnc, bkupEnc); err != nil {
		return fmt.Errorf("upload previous oss.db to .bkup.enc: %w", err)
	}
	if err := s.storage.Upload(ctx, prevIV, bkupIV); err != nil {
		// Clean up the orphaned .bkup.enc — a .bkup without its IV is useless.
		_ = s.storage.Delete(ctx, bkupEnc)
		return fmt.Errorf("upload previous oss.db to .bkup.iv: %w", err)
	}
	s.logger.Info("archived previous oss.db", "bkup_enc", bkupEnc)
	return nil
}

// bkupEncPattern matches the .bkup.enc object key and captures the timestamp.
var bkupEncPattern = regexp.MustCompile(`^meta/db/oss\.db\.version(\d{12})\.bkup\.enc$`)

// pruneBkups lists all .bkup.enc objects under meta/db/, sorts them by
// timestamp descending, and deletes the .enc+.iv pair for any entry beyond the
// configured keep-count.
func (s *DBBackupService) pruneBkups(ctx context.Context) error {
	keys, err := s.storage.List(ctx, ossDBPrefix)
	if err != nil {
		return fmt.Errorf("list %s: %w", ossDBPrefix, err)
	}

	toDelete := pruneBkupsKeys(keys, s.bkupKeepCount())
	if len(toDelete) == 0 {
		return nil
	}

	s.logger.Info("pruning superseded oss.db .bkup entries", "count", len(toDelete)/2)
	return s.storage.DeleteBatch(ctx, toDelete)
}

// pruneBkupsKeys determines which .bkup.enc/.iv pairs should be deleted given
// the full set of object keys under meta/db/ and a keep-count. Timestamps are
// sorted descending (newest first); the first keepCount entries are retained
// and the remainder are returned for deletion (both .enc and .iv keys).
//
// Exported as a pure function so unit tests can cover the pruning logic without
// constructing a DBBackupService or StorageManager.
func pruneBkupsKeys(keys []string, keepCount int) []string {
	type bkup struct {
		timestamp string
		encKey    string
		ivKey     string
	}
	byTS := make(map[string]*bkup)
	for _, key := range keys {
		m := bkupEncPattern.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		ts := m[1]
		b, ok := byTS[ts]
		if !ok {
			b = &bkup{timestamp: ts}
			byTS[ts] = b
		}
		b.encKey = key
		b.ivKey = fmt.Sprintf("%s/oss.db.version%s.bkup.iv", ossDBPrefix, ts)
	}

	if len(byTS) <= keepCount {
		return nil
	}

	sorted := make([]string, 0, len(byTS))
	for ts := range byTS {
		sorted = append(sorted, ts)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sorted)))

	// Guard against negative keepCount (treated as 0 => delete everything).
	if keepCount < 0 {
		keepCount = 0
	}
	var toDelete []string
	for _, ts := range sorted[keepCount:] {
		b := byTS[ts]
		toDelete = append(toDelete, b.encKey, b.ivKey)
	}
	return toDelete
}

// copyFile copies a file from src to dst using streaming to avoid loading
// the entire file into memory.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
