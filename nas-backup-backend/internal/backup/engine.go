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
	progress   *ProgressBroker

	mu              sync.Mutex
	runningBackupID int64
	cancelFuncs     map[int64]context.CancelFunc
}

// NewEngine creates a new backup Engine with all required dependencies.
func NewEngine(database *db.Database, sc *scanner.Scanner, dd *dedup.Deduplicator,
	comp *compress.Compressor, enc *crypto.Encryptor, stor *storage.StorageManager,
	cfg *config.AppConfig, pb *ProgressBroker) *Engine {
	return &Engine{
		db:          database,
		scanner:     sc,
		dedup:       dd,
		compressor:  comp,
		encryptor:   enc,
		storage:     stor,
		config:      cfg,
		logger:      slog.Default(),
		progress:    pb,
		cancelFuncs: make(map[int64]context.CancelFunc),
	}
}

// ProgressBroker returns the progress broker for SSE subscriptions.
func (e *Engine) ProgressBroker() *ProgressBroker {
	return e.progress
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
	if e.progress != nil {
		e.progress.PublishLog(backupID, string(level), message, detail)
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
				if e.progress != nil {
					e.progress.PublishPhase(backupID, models.PhaseCancelled, "备份已取消")
				}
				retErr = ctx.Err()
			} else {
				_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusFailed, retErr.Error())
				e.logEvent(backupID, models.LogLevelError, "backup failed", retErr.Error())
				if e.progress != nil {
					e.progress.PublishPhase(backupID, models.PhaseFailed, fmt.Sprintf("备份失败: %v", retErr))
				}
			}
		} else {
			_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusCompleted, "")
			e.logEvent(backupID, models.LogLevelInfo, "backup completed", "")
			if e.progress != nil {
				e.progress.PublishPhase(backupID, models.PhaseCompleted, "备份完成")
			}
		}
	}()

	// ── Phase 1: Update status to running ──────────────────────────────
	if err := e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusRunning, ""); err != nil {
		return fmt.Errorf("update backup status to running: %w", err)
	}
	e.logger.Info("backup started", "backup_id", backupID, "type", backupType)
	e.logEvent(backupID, models.LogLevelInfo, "backup started",
		fmt.Sprintf("type=%s", backupType))
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseScanning, "正在扫描文件...")
	}

	// ── Phase 2: Scan directories ──────────────────────────────────────
	scanStart := time.Now()
	scanResult, err := e.scanner.ScanWithProgress(func(scanned int) {
		if e.progress != nil && scanned%200 == 0 {
			// 扫描阶段无法预知总文件数，用对数曲线估算 0-5% 的进度，
			// 让进度条有视觉反馈而非卡在 0%。
			pct := 5.0 * (1.0 - 1.0/float64(scanned/100+1))
			e.progress.PublishProgress(backupID, models.PhaseScanning, scanned, 0, pct)
		}
	})
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
	if e.progress != nil {
		e.progress.PublishProgress(backupID, models.PhaseScanning, scanResult.TotalScanned, scanResult.TotalScanned, 5)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Count files needing hashing
	var filesToHash int
	for _, ch := range scanResult.Changes {
		if ch.ChangeType == scanner.Added || ch.ChangeType == scanner.Modified {
			filesToHash++
		}
	}

	// ── Phase 3: Compute hashes ────────────────────────────────────────
	hashStart := time.Now()
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseHashing, fmt.Sprintf("正在计算 %d 个文件的哈希...", filesToHash))
	}
	hashedCount := 0
	if err := e.scanner.ComputeHashes(scanResult, func(done int) {
		hashedCount = done
		if e.progress != nil {
			pct := 5.0
			if filesToHash > 0 {
				// 哈希阶段占 5%-30%（25% 权重，从扫描结束的 5% 开始）
				pct = 5.0 + float64(done)/float64(filesToHash)*25
			}
			e.progress.PublishProgress(backupID, models.PhaseHashing, done, filesToHash, pct)
		}
	}); err != nil {
		return fmt.Errorf("compute hashes: %w", err)
	}
	e.logger.Info("hash computation completed", "duration", time.Since(hashStart))
	e.logEvent(backupID, models.LogLevelInfo, "hash computation completed",
		fmt.Sprintf("files_hashed=%d duration=%s", hashedCount, time.Since(hashStart)))

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
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseDeduplicating, "正在进行去重分析...")
	}
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
	if e.progress != nil {
		e.progress.PublishProgress(backupID, models.PhaseDeduplicating, len(changedFiles), len(changedFiles), 35)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 6: Process files to upload (compress → encrypt → upload) ─
	processStart := time.Now()
	if e.progress != nil {
		totalFiles := len(dedupResult.ToUpload) + len(dedupResult.Skipped)
		if totalFiles > 0 {
			e.progress.PublishPhase(backupID, models.PhaseUploading, fmt.Sprintf("正在上传 %d 个文件（%d 个去重跳过）...", len(dedupResult.ToUpload), len(dedupResult.Skipped)))
		} else {
			e.progress.PublishPhase(backupID, models.PhaseUploading, "没有文件需要上传")
		}
	}

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

	totalToProcess := len(dedupResult.ToUpload) + len(dedupResult.Skipped)
	for i := range dedupResult.ToUpload {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := &dedupResult.ToUpload[i]

		// 降频发布 file 事件：首个文件立即发布，之后每 50 个发布一次，
		// 避免大量小文件时事件洪流导致前端频繁 re-render。
		if e.progress != nil && (i == 0 || i%50 == 0) {
			e.progress.PublishFile(backupID, models.PhaseUploading, entry.Path, entry.Size)
		}

		fileID, origSize, storedSize, compressType, storageKey, encIV, err := e.processAndUploadFile(
			entry, backupType, tmpDir)
		if err != nil {
			e.logEvent(backupID, models.LogLevelError, "file upload failed",
				fmt.Sprintf("path=%s error=%v", entry.Path, err))
			return fmt.Errorf("process file %q: %w", entry.Path, err)
		}

		totalOriginalSize += origSize
		totalUploadedSize += storedSize

		if compressType == "zstd" && origSize > 0 {
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
			AuthTag:      "",
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
		e.logEvent(backupID, models.LogLevelInfo, "file uploaded",
			fmt.Sprintf("path=%s size=%d stored=%d", entry.Path, origSize, storedSize))

		if e.progress != nil {
			processed := i + 1
			pct := 35.0
			if totalToProcess > 0 {
				// 上传阶段占 35%-95%（60% 权重）
				pct = 35.0 + float64(processed)/float64(totalToProcess)*60.0
			}
			e.progress.PublishProgress(backupID, models.PhaseUploading, processed, totalToProcess, pct)
		}
	}

	// Process dedup-skipped files
	for i, skipped := range dedupResult.Skipped {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.progress != nil && i%50 == 0 {
			processed := len(dedupResult.ToUpload) + i + 1
			pct := 35.0
			if totalToProcess > 0 {
				pct = 35.0 + float64(processed)/float64(totalToProcess)*60.0
			}
			e.progress.PublishProgress(backupID, models.PhaseUploading, processed, totalToProcess, pct)
		}

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

	// ── Phase 7: Handle dedup-only files already done above ────────────

	// ── Phase 8: Add all backup_files entries ──────────────────────────
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseFinalizing, "正在更新备份索引...")
		e.progress.PublishProgress(backupID, models.PhaseFinalizing, 0, 1, 95)
	}
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
	if e.progress != nil {
		e.progress.PublishProgress(backupID, models.PhaseFinalizing, 1, 1, 100)
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

	return nil
}

// processAndUploadFile handles compress → encrypt → upload → verify for a single file.
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
		_ = compressedSize
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
func (e *Engine) generateStorageKey(backupType models.BackupType, hash string) string {
	dateStr := time.Now().Format("20060102")
	typeStr := string(backupType)

	hashPrefix := "00"
	if len(hash) >= 2 {
		hashPrefix = hash[:2]
	}

	return fmt.Sprintf("data/%s-%s/%s/%s.enc", dateStr, typeStr, hashPrefix, hash)
}
