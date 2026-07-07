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
	// Mutual exclusion: check no restore is running.
	restoreRunning, _ := e.db.RestoreJobRepo.IsRunning()
	if restoreRunning {
		e.mu.Unlock()
		return fmt.Errorf("a restore is currently running; backup and restore cannot run concurrently")
	}
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
	// Mutual exclusion: check no restore is running.
	restoreRunning, _ := e.db.RestoreJobRepo.IsRunning()
	if restoreRunning {
		e.mu.Unlock()
		return fmt.Errorf("a restore is currently running; backup and restore cannot run concurrently")
	}
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

// determineBackupType automatically decides whether to run a full or incremental backup.
// It returns the concrete backup type (full or incremental) and the base backup ID for incrementals.
func (e *Engine) determineBackupType() (models.BackupType, *int64, error) {
	latestFull, err := e.db.BackupRepo.GetLatestFull()
	if err != nil {
		return models.BackupTypeFull, nil, fmt.Errorf("get latest full backup: %w", err)
	}
	if latestFull == nil || latestFull.CompletedAt == nil {
		e.logger.Info("no completed full backup found, will run full backup")
		return models.BackupTypeFull, nil, nil
	}

	intervalMonths := e.config.Backup.Retention.FullResetInterval
	if intervalMonths > 0 {
		cutoff := time.Now().AddDate(0, -intervalMonths, 0)
		if latestFull.CompletedAt.Before(cutoff) {
			e.logger.Info("full reset interval reached, will run full backup",
				"last_full", latestFull.CompletedAt, "interval_months", intervalMonths)
			return models.BackupTypeFull, nil, nil
		}
	}

	id := latestFull.ID
	e.logger.Info("will run incremental backup", "base_backup_id", id)
	return models.BackupTypeIncremental, &id, nil
}

// StartBackup creates a backup record and starts the backup asynchronously.
// Returns the backup ID immediately so callers can track progress via the API.
// Use BackupTypeAuto to let the system automatically determine full vs incremental.
func (e *Engine) StartBackup(backupType models.BackupType) (int64, error) {
	e.mu.Lock()
	// Mutual exclusion: check no restore is running.
	restoreRunning, _ := e.db.RestoreJobRepo.IsRunning()
	if restoreRunning {
		e.mu.Unlock()
		return 0, fmt.Errorf("a restore is currently running; backup and restore cannot run concurrently")
	}
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

	var (
		actualType   models.BackupType
		baseBackupID *int64
	)

	if backupType == models.BackupTypeAuto {
		actualType, baseBackupID, err = e.determineBackupType()
		if err != nil {
			e.mu.Unlock()
			return 0, err
		}
	} else {
		actualType = backupType
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
	}

	backupID, err := e.db.BackupRepo.Create(actualType, baseBackupID)
	if err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	go func() {
		ctx := context.Background()
		if err := e.executeBackup(ctx, backupID, actualType, baseBackupID); err != nil {
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

// NeedsReconcile performs a lightweight check to detect whether the system's
// data sources (hash_index, backup_files, backups status) have drifted out of
// sync. It does NOT list OSS objects (that would require a slow rclone call),
// so it may miss OSS-only orphans — but it catches the most common post-crash
// inconsistencies: ref_count drift, failed backups with files, and completed
// backups without files.
func (e *Engine) NeedsReconcile() bool {
	// 1. ref_count mismatches between hash_index and active files.
	mismatch, err := e.db.HashRepo.HasRefCountMismatches()
	if err != nil {
		e.logger.Warn("needs-reconcile: check ref_count mismatches failed", "error", err)
	} else if mismatch {
		return true
	}

	// 2. Failed backups that still have backup_files rows.
	failedWithFiles, err := e.db.BackupRepo.ListFailedBackupsWithFiles()
	if err != nil {
		e.logger.Warn("needs-reconcile: list failed backups with files failed", "error", err)
	} else if len(failedWithFiles) > 0 {
		return true
	}

	// 3. Completed backups that have no backup_files rows.
	completedNoFiles, err := e.db.BackupRepo.ListCompletedBackupsWithoutFiles()
	if err != nil {
		e.logger.Warn("needs-reconcile: list completed backups without files failed", "error", err)
	} else if len(completedNoFiles) > 0 {
		return true
	}

	return false
}

// RunGarbageCollection cleans up orphan data in OSS and the database.
// It refuses to run while a backup is in progress to avoid deleting objects
// that are in the process of being uploaded but not yet recorded in
// hash_index.
func (e *Engine) RunGarbageCollection(ctx context.Context) error {
	e.logger.Info("starting garbage collection")
	start := time.Now()

	// Refuse to run while a backup is in progress, same as reconcile.
	if _, running := e.RunningBackupID(); running {
		return fmt.Errorf("a backup is currently running; run GC after it finishes")
	}
	if running, err := e.db.BackupRepo.IsRunning(); err != nil {
		return fmt.Errorf("check running backup: %w", err)
	} else if running {
		return fmt.Errorf("a backup is currently running (db); run GC after it finishes")
	}

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

		if err := e.storage.Delete(ctx, orphan.StorageKey); err != nil {
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
		// 备份结束后延迟清空历史缓冲，让已连接的客户端有时间收到结束事件，
		// 同时避免下次新连接回放过期的历史事件。
		if e.progress != nil {
			go func() {
				time.Sleep(30 * time.Second)
				e.progress.ClearHistory()
			}()
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
	e.logEvent(backupID, models.LogLevelInfo, "开始扫描目录", "")
	scanResult, err := e.scanner.ScanWithProgress(func(scanned int) {
		if e.progress != nil {
			// 扫描阶段无法预知总文件数，用对数曲线估算 0-5% 的进度
			pct := 5.0 * (1.0 - 1.0/float64(scanned/100+1))
			e.progress.PublishProgress(backupID, models.PhaseScanning, scanned, 0, pct)
			e.progress.PublishFile(backupID, models.PhaseScanning, fmt.Sprintf("已扫描 %d 个文件", scanned), 0)
			if scanned%500 == 0 {
				e.logEvent(backupID, models.LogLevelInfo, "扫描进行中",
					fmt.Sprintf("已扫描 %d 个文件", scanned))
			}
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
	e.logEvent(backupID, models.LogLevelInfo, "扫描完成",
		fmt.Sprintf("变更=%d 已扫描=%d 错误=%d 耗时=%s",
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
	if filesToHash == 0 {
		e.logEvent(backupID, models.LogLevelInfo, "无需计算哈希", "没有新增或修改的文件")
	} else {
		e.logEvent(backupID, models.LogLevelInfo, "开始计算哈希",
			fmt.Sprintf("共 %d 个文件需要计算", filesToHash))
	}
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseHashing,
			fmt.Sprintf("正在计算 %d 个文件的哈希...", filesToHash))
	}
	hashedCount := 0
	if err := e.scanner.ComputeHashes(scanResult, func(done, total int, path string, size int64) {
		hashedCount = done
		if e.progress != nil {
			pct := 5.0
			if total > 0 {
				pct = 5.0 + float64(done)/float64(total)*25
			}
			e.progress.PublishProgress(backupID, models.PhaseHashing, done, total, pct)
			e.progress.PublishFile(backupID, models.PhaseHashing, path, size)
			// 逐文件哈希是高频事件，仅推送SSE实时面板，不写DB避免拖慢备份
			e.progress.PublishLog(backupID, "info", "哈希计算",
				fmt.Sprintf("[%d/%d] %s (%s)", done, total, path, formatSize(size)))
		}
	}); err != nil {
		return fmt.Errorf("compute hashes: %w", err)
	}
	if filesToHash > 0 {
		e.logEvent(backupID, models.LogLevelInfo, "哈希计算完成",
			fmt.Sprintf("已哈希 %d 个文件 耗时=%s", hashedCount, time.Since(hashStart)))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 4: Separate changes by type ──────────────────────────────
	var (
		changedFiles []scanner.FileChange
		deletedPaths []string
		skippedInc   int
	)
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
			// For full backups, rewrite Unchanged → Modified so the dedup
			// pipeline processes them. Without this, dedup.go skips
			// Unchanged entirely and a full backup degenerates into an
			// incremental one (to_upload=0 when all files are unchanged on
			// disk), never rebuilding hash_index ↔ OSS mappings for objects
			// that were lost in a previous crash.
			rewritten := change
			rewritten.ChangeType = scanner.Modified
			changedFiles = append(changedFiles, rewritten)
		case scanner.Added, scanner.Modified:
			changedFiles = append(changedFiles, change)
		}
	}
	if skippedInc > 0 {
		e.logEvent(backupID, models.LogLevelInfo, "增量备份跳过未变更文件",
			fmt.Sprintf("count=%d", skippedInc))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 5: Deduplicate ───────────────────────────────────────────
	dedupStart := time.Now()
	e.logEvent(backupID, models.LogLevelInfo, "开始去重分析",
		fmt.Sprintf("待分析文件数=%d", len(changedFiles)))
	if e.progress != nil {
		e.progress.PublishPhase(backupID, models.PhaseDeduplicating, "正在去重分析...")
	}
	dedupResult, err := e.dedup.Deduplicate(ctx, changedFiles)
	if err != nil {
		return fmt.Errorf("deduplicate: %w", err)
	}
	e.logger.Info("deduplication completed",
		"to_upload", len(dedupResult.ToUpload),
		"skipped_dedup", len(dedupResult.Skipped),
		"dedup_saved_bytes", dedupResult.TotalSaved,
		"duration", time.Since(dedupStart))
	e.logEvent(backupID, models.LogLevelInfo, "去重分析完成",
		fmt.Sprintf("待上传=%d 去重跳过=%d 节省=%s 耗时=%s",
			len(dedupResult.ToUpload), len(dedupResult.Skipped),
			formatSize(dedupResult.TotalSaved), time.Since(dedupStart)))
	if e.progress != nil {
		e.progress.PublishProgress(backupID, models.PhaseDeduplicating, len(changedFiles), len(changedFiles), 35)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 6: Process files to upload (compress → encrypt → upload) ─
	processStart := time.Now()
	totalToProcess := len(dedupResult.ToUpload) + len(dedupResult.Skipped)
	if totalToProcess == 0 {
		e.logEvent(backupID, models.LogLevelInfo, "没有文件需要上传", "")
	} else {
		e.logEvent(backupID, models.LogLevelInfo, "开始处理文件",
			fmt.Sprintf("待上传=%d 去重跳过=%d", len(dedupResult.ToUpload), len(dedupResult.Skipped)))
	}
	if e.progress != nil {
		if totalToProcess > 0 {
			e.progress.PublishPhase(backupID, models.PhaseUploading,
				fmt.Sprintf("正在上传 %d 个文件（%d 个去重跳过）", len(dedupResult.ToUpload), len(dedupResult.Skipped)))
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

	// pendingMeta records the encryption/compression metadata of files uploaded
	// in THIS batch, keyed by content hash. Dedup-skipped files in the same
	// batch need this metadata to build their backup_file record, because the
	// source backup_file row has not been persisted yet (AddBackupFilesBatch
	// runs after the whole loop). Without this, compressType would be "" and
	// violate the DB CHECK constraint, failing the entire backup.
	type pendingMeta struct {
		encIV       string
		compressType string
		storedSize  int64
	}
	pendingByHash := make(map[string]pendingMeta)

	for i := range dedupResult.ToUpload {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := &dedupResult.ToUpload[i]

		e.progress.PublishLog(backupID, "info", "开始处理文件",
			fmt.Sprintf("[%d/%d] %s (%s)", i+1, len(dedupResult.ToUpload), entry.Path, formatSize(entry.Size)))

		fileID, origSize, storedSize, compressType, storageKey, encIV, err := e.processAndUploadFile(
			ctx, entry, backupType, tmpDir, backupID)
		if err != nil {
			e.logEvent(backupID, models.LogLevelError, "文件处理失败",
				fmt.Sprintf("path=%s error=%v", entry.Path, err))
			return fmt.Errorf("process file %q: %w", entry.Path, err)
		}

		totalOriginalSize += origSize
		totalUploadedSize += storedSize

		if compressType == "zstd" && origSize > 0 {
			saved := origSize - storedSize
			if saved > 0 {
				compressSaved += saved
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

		// Record metadata so dedup-skipped files in this same batch (same hash)
		// can build a valid backup_file record before AddBackupFilesBatch runs.
		if entry.NewHash != "" {
			pendingByHash[entry.NewHash] = pendingMeta{
				encIV:        encIV,
				compressType: compressType,
				storedSize:   storedSize,
			}
		}

		e.progress.PublishLog(backupID, "info", "文件处理完成",
			fmt.Sprintf("[%d/%d] %s 原始=%s 存储=%s 压缩=%s",
				i+1, len(dedupResult.ToUpload), entry.Path,
				formatSize(origSize), formatSize(storedSize), compressType))

		if e.progress != nil {
			processed := i + 1
			pct := 35.0
			if totalToProcess > 0 {
				pct = 35.0 + float64(processed)/float64(totalToProcess)*60.0
			}
			e.progress.PublishProgress(backupID, models.PhaseUploading, processed, totalToProcess, pct)
			e.progress.PublishFile(backupID, models.PhaseUploading, entry.Path, entry.Size)
		}
	}

	// Process dedup-skipped files
	for i, skipped := range dedupResult.Skipped {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.progress.PublishLog(backupID, "info", "去重跳过文件",
			fmt.Sprintf("[%d/%d] %s (已存在)", i+1, len(dedupResult.Skipped), skipped.Path))

		if e.progress != nil {
			processed := len(dedupResult.ToUpload) + i + 1
			pct := 35.0
			if totalToProcess > 0 {
				pct = 35.0 + float64(processed)/float64(totalToProcess)*60.0
			}
			e.progress.PublishProgress(backupID, models.PhaseUploading, processed, totalToProcess, pct)
			e.progress.PublishFile(backupID, models.PhaseUploading, skipped.Path, 0)
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
		// 1) Same-batch lookup: the source file was uploaded earlier in this
		//    same executeBackup run, so its backup_file row is not in the DB
		//    yet — read from the in-memory pendingByHash map.
		if pm, ok := pendingByHash[skipped.Hash]; ok {
			encIV = pm.encIV
			compressType = pm.compressType
			storedSize = pm.storedSize
		} else {
			// 2) Cross-batch lookup: the hash was uploaded in a previous
			//    backup, so the backup_file row exists in the DB.
			existingFiles, gErr := e.db.FileRepo.GetByHash(skipped.Hash)
			if gErr == nil && len(existingFiles) > 0 {
				bfRec, bfErr := e.db.BackupRepo.GetFileRestoreInfo(existingFiles[0].ID)
				if bfErr == nil && bfRec != nil {
					encIV = bfRec.EncryptedIV
					compressType = bfRec.CompressType
					storedSize = bfRec.StoredSize
				}
			}
		}
		// 3) Fallback: if neither source provided metadata, default to "none".
		//    This satisfies the DB CHECK constraint (compress_type IN
		//    ('zstd','none')) instead of leaving an empty string that would
		//    fail the whole batch insert.
		if compressType == "" {
			compressType = "none"
			e.logger.Warn("dedup-skipped file has no compress metadata, defaulting to none",
				"path", skipped.Path, "hash", skipped.Hash)
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

	e.logEvent(backupID, models.LogLevelInfo, "文件处理全部完成",
		fmt.Sprintf("已上传=%d 总上传量=%s 压缩节省=%s 耗时=%s",
			len(dedupResult.ToUpload), formatSize(totalUploadedSize),
			formatSize(compressSaved), time.Since(processStart)))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// ── Phase 7: Handle dedup-only files already done above ────────────

	// ── Phase 8: Add all backup_files entries ──────────────────────────
	e.logEvent(backupID, models.LogLevelInfo, "更新备份索引",
		fmt.Sprintf("共 %d 条记录", len(backupFiles)))
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
		e.logEvent(backupID, models.LogLevelInfo, "标记已删除文件",
			fmt.Sprintf("count=%d", len(deletedPaths)))
		if err := e.db.FileRepo.MarkDeletedBatch(deletedPaths); err != nil {
			return fmt.Errorf("mark deleted files: %w", err)
		}

		for _, path := range deletedPaths {
			if e.progress != nil {
				e.progress.PublishLog(backupID, "info", "标记删除", path)
			}
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
					if e.progress != nil {
						hashShort := fileRec.Hash
						if len(hashShort) > 8 {
							hashShort = hashShort[:8]
						}
						e.progress.PublishLog(backupID, "info", "引用计数递减",
							fmt.Sprintf("%s hash=%s ref=%d", path, hashShort, newRefCount))
					}
				}
			}
		}
	}

	// ── Phase 10: Update backup stats ──────────────────────────────────
	totalFiles := len(dedupResult.ToUpload) + len(dedupResult.Skipped)
	e.logEvent(backupID, models.LogLevelInfo, "更新备份统计",
		fmt.Sprintf("total_files=%d total_size=%s uploaded=%s",
			totalFiles, formatSize(totalOriginalSize), formatSize(totalUploadedSize)))
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
	ctx context.Context,
	entry *dedup.DedupFileEntry,
	backupType models.BackupType,
	tmpDir string,
	backupID int64,
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
		if e.progress != nil {
			e.progress.PublishLog(backupID, "info", "压缩文件", entry.Path)
		}
		_, compressedSize, compErr := e.compressor.Compress(entry.Path, compressedPath)
		if compErr != nil {
			return 0, 0, 0, "", "", "", fmt.Errorf("compress file: %w", compErr)
		}
		workingPath = compressedPath
		compressType = "zstd"
		if e.progress != nil {
			e.progress.PublishLog(backupID, "info", "压缩完成",
				fmt.Sprintf("%s %s→%s", entry.Path, formatSize(entry.Size), formatSize(compressedSize)))
		}
		_ = compressedSize
	}

	// Encrypt.
	if e.progress != nil {
		e.progress.PublishLog(backupID, "info", "加密文件", entry.Path)
	}
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

	// Generate storage key. Reuse existing key when re-uploading a missing object.
	if entry.StorageKey != "" {
		storageKey = entry.StorageKey
	} else {
		storageKey = e.generateStorageKey(backupType, entry.NewHash)
	}

	// Upload.
	if e.progress != nil {
		e.progress.PublishLog(backupID, "info", "上传文件",
			fmt.Sprintf("%s → %s", entry.Path, storageKey))
	}
	if err := e.storage.Upload(ctx, encryptedPath, storageKey); err != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("upload file: %w", err)
	}

	// Verify upload.
	if e.progress != nil {
		e.progress.PublishLog(backupID, "info", "验证上传", storageKey)
	}
	exists, verifyErr := e.storage.Exists(ctx, storageKey)
	if verifyErr != nil {
		return 0, 0, 0, "", "", "", fmt.Errorf("verify upload: %w", verifyErr)
	}
	if !exists {
		return 0, 0, 0, "", "", "", fmt.Errorf("upload verification failed: object %q not found in storage", storageKey)
	}

	// Upsert hash index record. For re-uploads (missing OSS object being restored),
	// the hash_index row already exists with correct ref_count, so skip the upsert
	// to avoid double-counting.
	if entry.IsNew {
		if e.progress != nil {
			e.progress.PublishLog(backupID, "info", "更新哈希索引", entry.Path)
		}
		if _, hashErr := e.db.HashRepo.Upsert(entry.NewHash, entry.Size, storageKey); hashErr != nil {
			return 0, 0, 0, "", "", "", fmt.Errorf("upsert hash record: %w", hashErr)
		}
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

// formatSize 将字节数格式化为人类可读的字符串。
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2fGB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2fMB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
