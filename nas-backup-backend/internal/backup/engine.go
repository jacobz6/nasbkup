// Package backup implements the backup orchestration engine that coordinates
// all backup phases: Scan → Deduplicate → Compress → Encrypt → Upload → Update Index.
package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nas-backup/internal/compress"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/dedup"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/scanner"
	"github.com/nas-backup/internal/storage"
)

// Engine orchestrates the full backup pipeline.
type Engine struct {
	db         *db.Database
	scanner    *scanner.Scanner
	dedup      *dedup.Deduplicator
	compressor *compress.Compressor
	encryptor  *crypto.Encryptor
	storage    *storage.StorageManager
	config     *config.AppConfig
	logger     *slog.Logger

	mu              sync.Mutex
	runningBackupID int64
	cancelFuncs     map[int64]context.CancelFunc
}

// NewEngine creates a new backup Engine with all required dependencies.
func NewEngine(database *db.Database, sc *scanner.Scanner, dd *dedup.Deduplicator,
	comp *compress.Compressor, enc *crypto.Encryptor, stor *storage.StorageManager,
	cfg *config.AppConfig) *Engine {
	return &Engine{
		db:          database,
		scanner:     sc,
		dedup:       dd,
		compressor:  comp,
		encryptor:   enc,
		storage:     stor,
		config:      cfg,
		logger:      slog.Default(),
		cancelFuncs: make(map[int64]context.CancelFunc),
	}
}

// RunFullBackup executes a full backup synchronously.
func (e *Engine) RunFullBackup(ctx context.Context) error {
	e.mu.Lock()
	if e.runningBackupID > 0 {
		e.mu.Unlock()
		return fmt.Errorf("a backup is already running")
	}
	running, err := e.db.BackupRepo.IsRunning()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("check running backup: %w", err)
	}
	if running {
		e.mu.Unlock()
		return fmt.Errorf("a backup is already running")
	}

	backupID, err := e.db.BackupRepo.Create(models.BackupTypeFull, nil)
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	return e.executeBackup(ctx, backupID, models.BackupTypeFull, nil)
}

// RunIncrementalBackup executes an incremental backup synchronously.
func (e *Engine) RunIncrementalBackup(ctx context.Context) error {
	e.mu.Lock()
	if e.runningBackupID > 0 {
		e.mu.Unlock()
		return fmt.Errorf("a backup is already running")
	}
	running, err := e.db.BackupRepo.IsRunning()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("check running backup: %w", err)
	}
	if running {
		e.mu.Unlock()
		return fmt.Errorf("a backup is already running")
	}

	latestFull, err := e.db.BackupRepo.GetLatestFull()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("get latest full backup: %w", err)
	}
	if latestFull == nil {
		e.mu.Unlock()
		return fmt.Errorf("no full backup found; run a full backup first")
	}
	baseBackupID := latestFull.ID

	backupID, err := e.db.BackupRepo.Create(models.BackupTypeIncremental, &baseBackupID)
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	return e.executeBackup(ctx, backupID, models.BackupTypeIncremental, &baseBackupID)
}

// StartBackup creates a backup record and starts the backup asynchronously.
// Returns the backup ID immediately so callers can track progress via the API.
func (e *Engine) StartBackup(backupType models.BackupType) (int64, error) {
	e.mu.Lock()
	if e.runningBackupID > 0 {
		e.mu.Unlock()
		return 0, fmt.Errorf("a backup is already running")
	}

	running, err := e.db.BackupRepo.IsRunning()
	if err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("check running backup: %w", err)
	}
	if running {
		e.mu.Unlock()
		return 0, fmt.Errorf("a backup is already running")
	}

	var baseBackupID *int64
	if backupType == models.BackupTypeIncremental {
		latestFull, err := e.db.BackupRepo.GetLatestFull()
		if err != nil {
			e.mu.Unlock()
			return 0, fmt.Errorf("get latest full backup: %w", err)
		}
		if latestFull == nil {
			e.mu.Unlock()
			return 0, fmt.Errorf("no full backup found; run a full backup first")
		}
		id := latestFull.ID
		baseBackupID = &id
	}

	backupID, err := e.db.BackupRepo.Create(backupType, baseBackupID)
	if err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	go func() {
		ctx := context.Background()
		if err := e.executeBackup(ctx, backupID, backupType, baseBackupID); err != nil {
			e.logger.Error("async backup failed", "backup_id", backupID, "error", err)
		}
	}()

	return backupID, nil
}

// Cancel cancels a running backup by its ID.
func (e *Engine) Cancel(backupID int64) error {
	e.mu.Lock()
	cancel, ok := e.cancelFuncs[backupID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("no running backup with ID %d", backupID)
	}
	cancel()
	return nil
}

// RunningBackupID returns the ID of the currently running backup, if any.
func (e *Engine) RunningBackupID() (int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runningBackupID, e.runningBackupID > 0
}

// RunGarbageCollection cleans up orphan data in OSS and the database.
func (e *Engine) RunGarbageCollection(ctx context.Context) error {
	e.logger.Info("starting garbage collection")
	start := time.Now()

	graceDays := e.config.Backup.Retention.OrphanGraceDays
	orphans, err := e.db.HashRepo.GetOrphansOlderThan(graceDays)
	if err != nil {
		return fmt.Errorf("get orphan hash records: %w", err)
	}

	if len(orphans) == 0 {
		e.logger.Info("no orphan records to clean up")
		return nil
	}

	e.logger.Info("found orphan records", "count", len(orphans))

	var (
		deletedHashes []string
		deleteErrors  []string
	)

	for _, orphan := range orphans {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := e.storage.Delete(orphan.StorageKey); err != nil {
			e.logger.Error("failed to delete OSS object",
				"storage_key", orphan.StorageKey, "error", err)
			deleteErrors = append(deleteErrors, fmt.Sprintf("%s: %v", orphan.StorageKey, err))
			continue
		}
		e.logger.Info("deleted OSS object", "storage_key", orphan.StorageKey)
		deletedHashes = append(deletedHashes, orphan.Hash)
	}

	if len(deletedHashes) > 0 {
		if err := e.db.HashRepo.DeleteBatch(deletedHashes); err != nil {
			return fmt.Errorf("delete orphan hash records from DB: %w", err)
		}
		e.logger.Info("deleted orphan hash records from DB", "count", len(deletedHashes))
	}

	if len(deleteErrors) > 0 {
		return fmt.Errorf("garbage collection completed with %d errors: %s",
			len(deleteErrors), strings.Join(deleteErrors, "; "))
	}

	e.logger.Info("garbage collection completed",
		"deleted", len(deletedHashes), "duration", time.Since(start))
	return nil
}

// logEvent writes a log entry to the backup_logs table so it shows up in the
// Logs page of the UI. Errors writing to the DB are logged via slog but never
// propagated, so a logging failure cannot break the backup pipeline.
func (e *Engine) logEvent(backupID int64, level models.LogLevel, message, detail string) {
	id := backupID
	if err := e.db.LogRepo.Insert(&id, level, message, detail); err != nil {
		e.logger.Error("write backup log to db",
			"backup_id", backupID, "level", string(level), "error", err)
	}
}

// executeBackup runs the core backup pipeline for a given backup record.
func (e *Engine) executeBackup(ctx context.Context, backupID int64, backupType models.BackupType, baseBackupID *int64) (retErr error) {
	// Set up cancellation tracking.
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancelFuncs[backupID] = cancel
	e.runningBackupID = backupID
	e.mu.Unlock()

	// Cleanup cancellation tracking on exit (runs last due to LIFO).
	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, backupID)
		e.runningBackupID = 0
		e.mu.Unlock()
	}()

	// Update backup status on exit (runs before cleanup).
	defer func() {
		if retErr != nil {
			if ctx.Err() != nil {
				_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusCancelled, "cancelled")
				e.logEvent(backupID, models.LogLevelWarn, "backup cancelled", retErr.Error())
				retErr = ctx.Err()
			} else {
				_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusFailed, retErr.Error())
				e.logEvent(backupID, models.LogLevelError, "backup failed", retErr.Error())
			}
		} else {
			_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusCompleted, "")
			e.logEvent(backupID, models.LogLevelInfo, "backup completed", "")
		}
	}()

	// ── Phase 1: Update status to running ──────────────────────────────
	if err := e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusRunning, ""); err != nil {
		return fmt.Errorf("update backup status to running: %w", err)
	}
	e.logger.Info("backup started", "backup_id", backupID, "type", backupType)
	e.logEvent(backupID, models.LogLevelInfo, "backup started",
		fmt.Sprintf("type=%s", backupType))

	// ── Phase 2: Scan directories ──────────────────────────────────────
	scanStart := time.Now()
	scanResult, err := e.scanner.Scan()
	if err != nil {
		return fmt.Errorf("scan directories: %w", err)
	}
	e.logger.Info("scan completed",
		"changes", len(scanResult.Changes),
		"scanned", scanResult.TotalScanned,
		"errors", len(scanResult.Errors),
		"duration", time.Since(scanStart))
	e.logEvent(backupID, models.LogLevelInfo, "scan completed",
		fmt.Sprintf("changes=%d scanned=%d errors=%d duration=%s",
			len(scanResult.Changes), scanResult.TotalScanned,
			len(scanResult.Errors), time.Since(scanStart)))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 3: Compute hashes ────────────────────────────────────────
	hashStart := time.Now()
	if err := e.scanner.ComputeHashes(scanResult, func(int) {}); err != nil {
		return fmt.Errorf("compute hashes: %w", err)
	}
	e.logger.Info("hash computation completed", "duration", time.Since(hashStart))
	e.logEvent(backupID, models.LogLevelInfo, "hash computation completed",
		fmt.Sprintf("duration=%s", time.Since(hashStart)))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 4: Separate changes by type ──────────────────────────────
	var (
		changedFiles []scanner.FileChange
		deletedPaths []string
		skippedInc   int
	)
	// Build a path→FileChange map for later lookups (e.g. dedup skipped files).
	fileChangeMap := make(map[string]scanner.FileChange)
	for _, change := range scanResult.Changes {
		fileChangeMap[change.Path] = change
		switch change.ChangeType {
		case scanner.Deleted:
			deletedPaths = append(deletedPaths, change.Path)
		case scanner.Unchanged:
			if backupType == models.BackupTypeIncremental {
				skippedInc++
				continue
			}
			// For full backups, include unchanged files so their ref counts
			// and backup_files entries are refreshed.
			changedFiles = append(changedFiles, change)
		case scanner.Added, scanner.Modified:
			changedFiles = append(changedFiles, change)
		}
	}
	if skippedInc > 0 {
		e.logger.Info("skipped unchanged files (incremental)", "count", skippedInc)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 5: Deduplicate ───────────────────────────────────────────
	dedupStart := time.Now()
	dedupResult, err := e.dedup.Deduplicate(changedFiles)
	if err != nil {
		return fmt.Errorf("deduplicate: %w", err)
	}
	e.logger.Info("deduplication completed",
		"to_upload", len(dedupResult.ToUpload),
		"skipped_dedup", len(dedupResult.Skipped),
		"dedup_saved_bytes", dedupResult.TotalSaved,
		"duration", time.Since(dedupStart))
	e.logEvent(backupID, models.LogLevelInfo, "deduplication completed",
		fmt.Sprintf("to_upload=%d skipped_dedup=%d dedup_saved_bytes=%d duration=%s",
			len(dedupResult.ToUpload), len(dedupResult.Skipped),
			dedupResult.TotalSaved, time.Since(dedupStart)))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 6: Process files to upload (compress → encrypt → upload) ─
	processStart := time.Now()

	tmpDir, err := os.MkdirTemp("", "nas-backup-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var (
		totalOriginalSize int64
		totalUploadedSize int64
		compressSaved     int64
		backupFiles       []*models.BackupFileRecord
	)

	for i := range dedupResult.ToUpload {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := &dedupResult.ToUpload[i]

		fileID, origSize, storedSize, compressType, storageKey, encIV, err := e.processAndUploadFile(
			entry, backupType, tmpDir)
		if err != nil {
			return fmt.Errorf("process file %q: %w", entry.Path, err)
		}

		totalOriginalSize += origSize
		totalUploadedSize += storedSize

		if compressType == "zstd" && origSize > 0 {
			// The compressed size is storedSize minus encryption overhead.
			// We approximate compression savings as original - (stored - 60),
			// where 60 bytes ≈ salt(32) + nonce(12) + tag(16) overhead.
			// A more precise calculation would require tracking compressedSize
			// separately, but this gives a reasonable estimate.
			compressSaved += origSize - storedSize
			if compressSaved < 0 {
				compressSaved = 0
			}
		}

		backupFiles = append(backupFiles, &models.BackupFileRecord{
			BackupID:     backupID,
			FileID:       fileID,
			StorageKey:   storageKey,
			EncryptedIV:  encIV,
			AuthTag:      "", // Auth tag is embedded in AES-GCM ciphertext.
			CompressType: compressType,
			OriginalSize: origSize,
			StoredSize:   storedSize,
		})

		e.logger.Info("file uploaded",
			"path", entry.Path,
			"storage_key", storageKey,
			"original_size", origSize,
			"stored_size", storedSize,
			"compress_type", compressType)
	}

	e.logger.Info("file processing completed",
		"files_uploaded", len(dedupResult.ToUpload),
		"total_uploaded_size", totalUploadedSize,
		"compress_saved", compressSaved,
		"duration", time.Since(processStart))
	e.logEvent(backupID, models.LogLevelInfo, "file processing completed",
		fmt.Sprintf("files_uploaded=%d total_uploaded_size=%d compress_saved=%d duration=%s",
			len(dedupResult.ToUpload), totalUploadedSize, compressSaved, time.Since(processStart)))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 7: Handle dedup-only files (no upload needed) ────────────
	for _, skipped := range dedupResult.Skipped {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Look up file metadata from the scan result.
		var fileID int64
		fc, hasFC := fileChangeMap[skipped.Path]
		if hasFC {
			fileID, err = e.db.FileRepo.Upsert(fc.Path, fc.Size, fc.ModTime, fc.NewHash, fc.Inode)
			if err != nil {
				e.logger.Error("upsert file record for dedup'd file",
					"path", skipped.Path, "error", err)
				continue
			}
		} else {
			// Fallback: check if the file already exists in the DB.
			existingRec, dbErr := e.db.FileRepo.GetByPath(skipped.Path)
			if dbErr != nil {
				e.logger.Error("get file record for dedup'd file",
					"path", skipped.Path, "error", dbErr)
				continue
			}
			if existingRec == nil {
				e.logger.Warn("dedup'd file not found in scan or DB, skipping",
					"path", skipped.Path)
				continue
			}
			fileID = existingRec.ID
		}

		// Look up encryption metadata from an existing file with the same hash.
		var encIV, compressType string
		var storedSize int64
		existingFiles, err := e.db.FileRepo.GetByHash(skipped.Hash)
		if err == nil && len(existingFiles) > 0 {
			bfRec, bfErr := e.db.BackupRepo.GetFileRestoreInfo(existingFiles[0].ID)
			if bfErr == nil && bfRec != nil {
				encIV = bfRec.EncryptedIV
				compressType = bfRec.CompressType
				storedSize = bfRec.StoredSize
			}
		}

		origSize := int64(0)
		if hasFC {
			origSize = fc.Size
		}

		backupFiles = append(backupFiles, &models.BackupFileRecord{
			BackupID:     backupID,
			FileID:       fileID,
			StorageKey:   skipped.ExistingStorageKey,
			EncryptedIV:  encIV,
			AuthTag:      "",
			CompressType: compressType,
			OriginalSize: origSize,
			StoredSize:   storedSize,
		})

		totalOriginalSize += origSize
	}

	// ── Phase 8: Add all backup_files entries ──────────────────────────
	if len(backupFiles) > 0 {
		if err := e.db.BackupRepo.AddBackupFilesBatch(backupFiles); err != nil {
			return fmt.Errorf("add backup files: %w", err)
		}
	}

	// ── Phase 9: Mark deleted files and decrement ref counts ───────────
	if len(deletedPaths) > 0 {
		if err := e.db.FileRepo.MarkDeletedBatch(deletedPaths); err != nil {
			return fmt.Errorf("mark deleted files: %w", err)
		}

		for _, path := range deletedPaths {
			fileRec, err := e.db.FileRepo.GetByPath(path)
			if err != nil {
				e.logger.Error("get file record for deleted path",
					"path", path, "error", err)
				continue
			}
			if fileRec != nil && fileRec.Hash != "" {
				newRefCount, err := e.db.HashRepo.DecrementRef(fileRec.Hash)
				if err != nil {
					e.logger.Error("decrement ref count for deleted file",
						"hash", fileRec.Hash, "path", path, "error", err)
				} else {
					e.logger.Info("decremented ref count for deleted file",
						"hash", fileRec.Hash, "new_ref_count", newRefCount, "path", path)
				}
			}
		}
	}

	// ── Phase 10: Update backup stats ──────────────────────────────────
	totalFiles := len(dedupResult.ToUpload) + len(dedupResult.Skipped)
	if err := e.db.BackupRepo.UpdateStats(backupID,
		totalFiles,
		int(totalOriginalSize),
		int(totalUploadedSize),
		len(dedupResult.Skipped),
		skippedInc,
		compressSaved,
	); err != nil {
		return fmt.Errorf("update backup stats: %w", err)
	}

	e.logger.Info("backup completed successfully",
		"backup_id", backupID,
		"type", backupType,
		"total_files", totalFiles,
		"total_size", totalOriginalSize,
		"uploaded_size", totalUploadedSize,
		"skipped_dedup", len(dedupResult.Skipped),
		"skipped_inc", skippedInc,
		"compress_saved", compressSaved,
		"deleted", len(deletedPaths))
	// Note: the "backup completed" entry is also written by the status-update
	// defer on successful return, so we don't write a duplicate here.

	return nil
}

// processAndUploadFile handles compress → encrypt → upload → verify for a single file.
// Returns the file ID, original size, stored size, compress type, storage key, and
// encryption IV.
func (e *Engine) processAndUploadFile(
	entry *dedup.DedupFileEntry,
	backupType models.BackupType,
	tmpDir string,
) (fileID int64, originalSize int64, storedSize int64, compressType string, storageKey string, encIV string, err error) {

	// Upsert file record first to get the file ID.
	fileID, err = e.db.FileRepo.Upsert(entry.Path, entry.Size, entry.ModTime, entry.NewHash, entry.Inode)
	if err != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("upsert file record: %w", err)
	}

	originalSize = entry.Size
	compressType = "none"
	workingPath := entry.Path

	compressedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_compressed", fileID))
	encryptedPath := filepath.Join(tmpDir, fmt.Sprintf("%d_encrypted.enc", fileID))

	// Clean up temp files when done.
	defer func() {
		os.Remove(compressedPath)
		os.Remove(encryptedPath)
	}()

	// Compress if applicable.
	if e.compressor.ShouldCompress(entry.Path) {
		_, compressedSize, compErr := e.compressor.Compress(entry.Path, compressedPath)
		if compErr != nil {
			return 0, 0, 0, "", "", "", fmt.Errorf("compress file: %w", compErr)
		}
		workingPath = compressedPath
		compressType = "zstd"
		_ = compressedSize // tracked via storedSize after encryption
	}

	// Encrypt.
	encIV, err = e.encryptor.EncryptFile(workingPath, encryptedPath)
	if err != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("encrypt file: %w", err)
	}

	// Get stored size from encrypted file.
	encInfo, statErr := os.Stat(encryptedPath)
	if statErr != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("stat encrypted file: %w", statErr)
	}
	storedSize = encInfo.Size()

	// Generate storage key.
	storageKey = e.generateStorageKey(backupType, entry.NewHash)

	// Upload.
	if err := e.storage.Upload(encryptedPath, storageKey); err != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("upload file: %w", err)
	}

	// Verify upload.
	exists, verifyErr := e.storage.Exists(storageKey)
	if verifyErr != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("verify upload: %w", verifyErr)
	}
	if !exists {
		return 0, 0, 0, "", "", "", fmt.Errorf("upload verification failed: object %q not found in storage", storageKey)
	}

	// Upsert hash index record.
	if _, hashErr := e.db.HashRepo.Upsert(entry.NewHash, entry.Size, storageKey); hashErr != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("upsert hash record: %w", hashErr)
	}

	return fileID, originalSize, storedSize, compressType, storageKey, encIV, nil
}

// generateStorageKey builds the OSS object key for a file.
// Format: data/{YYYYMMDD}-{type}/{hash_prefix_2}/{hash}.enc
func (e *Engine) generateStorageKey(backupType models.BackupType, hash string) string {
	dateStr := time.Now().Format("20060102")
	typeStr := string(backupType)

	hashPrefix := "00"
	if len(hash) >= 2 {
		hashPrefix = hash[:2]
	}

	return fmt.Sprintf("data/%s-%s/%s/%s.enc", dateStr, typeStr, hashPrefix, hash)
}
