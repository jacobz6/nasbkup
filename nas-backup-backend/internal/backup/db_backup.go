// Package backup provides database backup/restore service for disaster recovery.
// This module encrypts the local SQLite database and uploads it to OSS so that
// in a total-loss scenario only the master.key and rclone.conf are needed to
// fully bootstrap a new NAS from cloud backups.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/storage"
)

const (
	// dbBackupPrefix is the OSS key prefix for database backups.
	dbBackupPrefix = "meta/db"

	// dbBackupKeepVersions is the number of historical database backup versions to retain.
	dbBackupKeepVersions = 3
)

// DBBackupService handles encrypted database snapshots to cloud storage.
type DBBackupService struct {
	encryptor *crypto.Encryptor
	storage   *storage.StorageManager
	dbPath    string       // path to the local SQLite database file
	dbConn    *sql.DB      // raw DB connection for WAL checkpoint
	config    *config.AppConfig
	logger    *slog.Logger
}

// NewDBBackupService creates a new DBBackupService.
// dbConn is the live *sql.DB connection used to checkpoint the WAL before
// copying the database file. It may be nil (e.g. in restore-cli bootstrap),
// in which case WAL checkpoint is skipped.
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

// BackupDatabase creates an encrypted copy of the local database and uploads it
// to OSS. It also prunes old versions beyond dbBackupKeepVersions.
// The remote key format is: meta/db/nas-backup-YYYYMMDD-HHMMSS.db.enc
// A companion IV file is stored as: meta/db/nas-backup-YYYYMMDD-HHMMSS.db.iv
func (s *DBBackupService) BackupDatabase(ctx context.Context) error {
	if s.encryptor == nil || s.storage == nil {
		return fmt.Errorf("database backup requires both encryptor and storage manager")
	}

	// Verify the database file exists.
	if _, err := os.Stat(s.dbPath); err != nil {
		return fmt.Errorf("database file not found: %s: %w", s.dbPath, err)
	}

	// Step 0: Checkpoint the SQLite WAL so all committed transactions are
	// written to the main database file before we copy it. Without this,
	// recent writes in the -wal file would be missing from the backup.
	if s.dbConn != nil {
		if _, err := s.dbConn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			s.logger.Warn("failed to checkpoint WAL, backup may be incomplete", "error", err)
		}
	}

	timestamp := time.Now().UTC().Format("20060102-150405")
	baseName := fmt.Sprintf("nas-backup-%s.db", timestamp)
	remoteEncKey := fmt.Sprintf("%s/%s.enc", dbBackupPrefix, baseName)
	remoteIVKey := fmt.Sprintf("%s/%s.iv", dbBackupPrefix, baseName)

	// Step 1: Copy database to a temp file using streaming to avoid loading
	// the entire file into memory.
	tmpDir := os.TempDir()
	localCopy := filepath.Join(tmpDir, fmt.Sprintf("%s.copy", baseName))
	if err := copyFile(s.dbPath, localCopy); err != nil {
		return fmt.Errorf("copy database for encryption: %w", err)
	}
	defer os.Remove(localCopy)

	// Step 2: Encrypt the database copy.
	localEnc := filepath.Join(tmpDir, baseName+".enc")
	iv, err := s.encryptor.EncryptFile(localCopy, localEnc)
	if err != nil {
		os.Remove(localEnc)
		return fmt.Errorf("encrypt database: %w", err)
	}
	defer os.Remove(localEnc)

	// Step 3: Upload encrypted database to OSS.
	s.logger.Info("uploading database backup", "key", remoteEncKey)
	if err := s.storage.Upload(ctx, localEnc, remoteEncKey); err != nil {
		return fmt.Errorf("upload encrypted database: %w", err)
	}

	// Step 4: Upload IV file. This is CRITICAL — without the IV, the
	// encrypted database cannot be decrypted. If this fails, we must
	// delete the orphaned .enc file from OSS to avoid leaving an
	// unrecoverable backup that looks valid.
	ivFile := filepath.Join(tmpDir, baseName+".iv")
	if err := os.WriteFile(ivFile, []byte(iv), 0600); err != nil {
		os.Remove(ivFile)
		// Best-effort cleanup of the orphaned .enc file in OSS.
		_ = s.storage.Delete(ctx, remoteEncKey)
		return fmt.Errorf("write IV file: %w", err)
	}
	defer os.Remove(ivFile)

	s.logger.Info("uploading database backup IV", "key", remoteIVKey)
	if err := s.storage.Upload(ctx, ivFile, remoteIVKey); err != nil {
		// IV upload failed — the .enc file in OSS is useless without it.
		// Delete the orphaned .enc to avoid a false sense of security.
		s.logger.Error("failed to upload IV file, deleting orphaned encrypted database", "error", err)
		if delErr := s.storage.Delete(ctx, remoteEncKey); delErr != nil {
			s.logger.Error("failed to cleanup orphaned encrypted database", "cleanup_error", delErr)
		}
		return fmt.Errorf("upload IV file (encrypted database cleaned up): %w", err)
	}

	// Step 5: Prune old versions.
	if err := s.pruneOldVersions(ctx); err != nil {
		// Non-fatal: the current backup is safe.
		s.logger.Warn("failed to prune old database backups", "error", err)
	}

	s.logger.Info("database backup completed", "key", remoteEncKey)
	return nil
}

// pruneOldVersions lists all database backup versions in OSS and deletes
// the oldest ones beyond dbBackupKeepVersions.
func (s *DBBackupService) pruneOldVersions(ctx context.Context) error {
	keys, err := s.storage.List(ctx, dbBackupPrefix)
	if err != nil {
		return fmt.Errorf("list database backups: %w", err)
	}

	// Extract unique base names (strip .enc/.iv suffix) and collect all keys.
	versions := make(map[string][]string) // baseName → [keys]
	for _, key := range keys {
		// key format: "meta/db/nas-backup-20060102-150405.db.enc" or ".iv"
		base := strings.TrimSuffix(key, ".enc")
		base = strings.TrimSuffix(base, ".iv")
		if base != key {
			versions[base] = append(versions[base], key)
		}
	}

	// Sort base names descending (newest first) and delete extras.
	sorted := make([]string, 0, len(versions))
	for base := range versions {
		sorted = append(sorted, base)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sorted)))

	if len(sorted) <= dbBackupKeepVersions {
		return nil
	}

	// Delete versions beyond the keep count.
	var toDelete []string
	for _, base := range sorted[dbBackupKeepVersions:] {
		toDelete = append(toDelete, versions[base]...)
	}

	if len(toDelete) == 0 {
		return nil
	}

	s.logger.Info("pruning old database backups", "count", len(toDelete))
	return s.storage.DeleteBatch(ctx, toDelete)
}

// ListVersions returns all available database backup versions from OSS.
// Each entry is the base name (without .enc/.iv suffix).
func (s *DBBackupService) ListVersions(ctx context.Context) ([]string, error) {
	keys, err := s.storage.List(ctx, dbBackupPrefix)
	if err != nil {
		return nil, fmt.Errorf("list database backups: %w", err)
	}

	seen := make(map[string]bool)
	var versions []string
	for _, key := range keys {
		base := strings.TrimSuffix(key, ".enc")
		base = strings.TrimSuffix(base, ".iv")
		if base != key && !seen[base] {
			seen[base] = true
			versions = append(versions, base)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions, nil
}

// Bootstrap downloads the specified database backup version from OSS,
// decrypts it, and replaces the local database file. This is used for
// disaster recovery when setting up a new NAS.
func (s *DBBackupService) Bootstrap(ctx context.Context, version string, targetDBPath string) error {
	if version == "" {
		// Find latest version.
		versions, err := s.ListVersions(ctx)
		if err != nil {
			return fmt.Errorf("list versions: %w", err)
		}
		if len(versions) == 0 {
			return fmt.Errorf("no database backups found in OSS")
		}
		version = versions[0]
	}

	encKey := fmt.Sprintf("%s.enc", version)
	ivKey := fmt.Sprintf("%s.iv", version)

	// Ensure the target directory exists.
	targetDir := filepath.Dir(targetDBPath)
	if targetDir != "" && targetDir != "." {
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("create target directory %s: %w", targetDir, err)
		}
	}

	// Download encrypted database.
	tmpDir := os.TempDir()
	localEnc := filepath.Join(tmpDir, filepath.Base(encKey))
	if err := s.storage.Download(ctx, encKey, localEnc); err != nil {
		return fmt.Errorf("download encrypted database: %w", err)
	}
	defer os.Remove(localEnc)

	// Download IV file.
	localIV := filepath.Join(tmpDir, filepath.Base(ivKey))
	if err := s.storage.Download(ctx, ivKey, localIV); err != nil {
		return fmt.Errorf("download IV file: %w", err)
	}
	defer os.Remove(localIV)

	ivData, err := os.ReadFile(localIV)
	if err != nil {
		return fmt.Errorf("read IV file: %w", err)
	}
	iv := strings.TrimSpace(string(ivData))

	// Decrypt to target path.
	if err := s.encryptor.DecryptFile(localEnc, targetDBPath, iv); err != nil {
		return fmt.Errorf("decrypt database: %w", err)
	}

	s.logger.Info("database bootstrapped from OSS", "version", version, "target", targetDBPath)
	return nil
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
