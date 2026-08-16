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
	"sync/atomic"
	"syscall"
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
	db          *db.Database
	scanner     *scanner.Scanner
	dedup       *dedup.Deduplicator
	compressor  *compress.Compressor
	encryptor   *crypto.Encryptor
	storage     *storage.StorageManager
	config      *config.AppConfig
	logger      *slog.Logger
	progress    *ProgressBroker
	dbBackupSvc *DBBackupService // optional: syncs encrypted DB to OSS after each backup

	mu              sync.Mutex
	runningBackupID int64
	cancelFuncs     map[int64]context.CancelFunc

	ready           atomic.Bool // true after InitFromOSS completes successfully
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

// SetDBBackupService injects a DBBackupService. If set, the engine will
// automatically upload an encrypted copy of the database to OSS after
// each successful backup.
func (e *Engine) SetDBBackupService(svc *DBBackupService) {
	e.dbBackupSvc = svc
}

// rebindAfterReopen rebuilds the in-memory scanner and deduplicator against the
// current DB connection. Reopen() swaps the underlying *sql.DB and re-binds the
// repos; both Scanner and Deduplicator cache repo pointers at construction time,
// so they must be recreated after every Reopen or their cached pointers resolve
// to a closed connection, surfacing as "sql: database is closed" on the next
// backup (scan or dedup phase).
func (e *Engine) rebindAfterReopen() {
	if e.db == nil {
		return
	}
	e.scanner = scanner.NewScanner(e.db.FileRepo, e.db.ConfigRepo)
	if e.config != nil {
		e.dedup = dedup.NewDeduplicator(e.db.HashRepo, e.storage, e.config.Storage.Concurrency)
	}
}

// getDBBackupSvc returns the engine's DBBackupService, lazily constructing one
// from the existing storage/encryptor/config/database dependencies if it was
// never injected. This lets InitFromOSS work even when the engine was
// assembled by test helpers or lightweight callers that skipped the explicit
// SetDBBackupService call.
//
// Returns nil if the underlying storage or encryptor is unavailable (e.g.
// tests running without OSS configured).
func (e *Engine) getDBBackupSvc() *DBBackupService {
	if e.dbBackupSvc != nil {
		return e.dbBackupSvc
	}
	if e.storage == nil || e.encryptor == nil || e.db == nil || e.db.Conn() == nil || e.config == nil {
		return nil
	}
	e.dbBackupSvc = NewDBBackupService(e.encryptor, e.storage, e.config, e.db.Conn())
	return e.dbBackupSvc
}

// ProgressBroker returns the progress broker for SSE subscriptions.
func (e *Engine) ProgressBroker() *ProgressBroker {
	return e.progress
}

// Ready returns true after InitFromOSS has completed successfully.
func (e *Engine) Ready() bool {
	return e.ready.Load()
}

// SetReady sets the engine's ready state. This is primarily useful for testing
// where InitFromOSS is skipped (no real OSS), but the engine should still be
// considered ready for API requests.
func (e *Engine) SetReady(ready bool) {
	e.ready.Store(ready)
}

// InitFromOSS initializes the local DB from OSS at service startup.
// It probes OSS reachability with exponential backoff retry (up to 10 minutes),
// then either pulls the authoritative oss.db or falls back to the local DB.
//
// OSS reachability is a hard prerequisite: if OSS is unreachable after the
// retry budget, the service cannot start and a fatal error is returned.
func (e *Engine) InitFromOSS(ctx context.Context) error {
	defer func() {
		e.ready.Store(true)
	}()

	svc := e.getDBBackupSvc()
	if svc == nil {
		return fmt.Errorf("cannot init from OSS: storage or encryptor not configured")
	}

	// Probe OSS reachability with exponential backoff (1s, 2s, 4s... max 60s),
	// for up to 10 minutes total.
	if err := e.waitForOSS(ctx, svc); err != nil {
		return fmt.Errorf("OSS not reachable: %w", err)
	}

	// Try to pull the authoritative DB from OSS.
	pulled, err := svc.PullOSSDB(ctx)
	if err != nil {
		return fmt.Errorf("pull oss.db: %w", err)
	}
	if pulled {
		// Reopen DB connection against the pulled file.
		if err := e.db.Reopen(); err != nil {
			return fmt.Errorf("reopen db after pull: %w", err)
		}
		// Re-bind the DBBackupService to the new connection so WAL checkpoint
		// and integrity_check in PushOSSDB use the correct handle.
		e.dbBackupSvc = NewDBBackupService(e.encryptor, e.storage, e.config, e.db.Conn())
		// Recreate scanner/deduplicator against the fresh connection.
		e.rebindAfterReopen()
		e.logger.Info("initialized local DB from OSS")
		return nil
	}

	// OSS has no oss.db — first-time deployment. The local DB (already opened
	// by main.go with migrations) serves as the initial empty DB.
	e.logger.Info("OSS has no oss.db, starting with empty local DB (first-time deployment)")
	return nil
}

// waitForOSS probes OSS reachability with exponential backoff.
// Returns nil as soon as OSS responds; returns an error if the deadline is reached.
func (e *Engine) waitForOSS(ctx context.Context, svc *DBBackupService) error {
	const (
		maxWait      = 10 * time.Minute
		initialDelay = 1 * time.Second
		maxDelay     = 60 * time.Second
	)

	deadline := time.Now().Add(maxWait)
	delay := initialDelay

	for {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := svc.OSSDBExists(probeCtx)
		cancel()
		if err == nil {
			return nil // OSS is reachable
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("OSS not reachable after %v (last error: %w)", maxWait, err)
		}

		e.logger.Warn("OSS not reachable, retrying", "error", err, "delay", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// pullAndPreserveRestoreJobs pulls the latest oss.db from OSS, preserving
// local restore_jobs across the DB overwrite. Returns (true, nil) if the DB
// was pulled; (false, nil) if OSS had no oss.db (first-time deployment).
func (e *Engine) pullAndPreserveRestoreJobs(ctx context.Context) (bool, error) {
	svc := e.getDBBackupSvc()
	if svc == nil {
		return false, nil
	}

	// Ensure the local schema is up to date before querying restore_jobs.
	// A stale or older-version local DB may lack the restore_jobs table; running
	// migrations here brings it up to date instead of failing with "no such table".
	if err := e.db.EnsureSchema(); err != nil {
		e.logger.Warn("ensure schema before export restore jobs failed", "error", err)
	}

	// Export local restore_jobs before DB overwrite. A missing table (e.g. the
	// authoritative oss.db came from an older version) is not fatal: we simply
	// have no local restore history to preserve.
	localJobs, err := e.db.RestoreJobRepo.ExportAll()
	if err != nil {
		e.logger.Warn("failed to export restore jobs before pull; continuing without local restore history", "error", err)
		localJobs = nil
	}

	// Pull oss.db from OSS (overwrites local DB file).
	pulled, err := svc.PullOSSDB(ctx)
	if err != nil {
		return false, fmt.Errorf("pull oss.db: %w", err)
	}
	if !pulled {
		// OSS has no oss.db — first-time deployment, keep local DB as-is.
		return false, nil
	}

	// Reopen DB connection against the new file.
	if err := e.db.Reopen(); err != nil {
		return false, fmt.Errorf("reopen db after pull: %w", err)
	}

	// Re-bind the DBBackupService to the new connection.
	e.dbBackupSvc = NewDBBackupService(e.encryptor, e.storage, e.config, e.db.Conn())
	// Recreate scanner/deduplicator against the fresh connection.
	e.rebindAfterReopen()

	// Import restore_jobs back into the fresh DB.
	if len(localJobs) > 0 {
		if err := e.db.RestoreJobRepo.ImportBatch(localJobs); err != nil {
			e.logger.Warn("failed to re-import restore jobs after pull", "error", err)
		}
	}

	return true, nil
}

// RefreshFromOSS pulls the latest authoritative oss.db from OSS, preserving
// local restore_jobs across the DB overwrite. It is exposed for the
// "refresh-oss-db" API so the operator can manually re-sync the local DB from
// OSS without restarting the service (e.g. after another machine performed a
// backup). Returns (true, nil) when a new DB was pulled; (false, nil) when OSS
// has no oss.db yet (first-time deployment).
func (e *Engine) RefreshFromOSS(ctx context.Context) (bool, error) {
	return e.pullAndPreserveRestoreJobs(ctx)
}

// StorageStatus summarizes the health of the OSS connection, the authoritative
// oss.db snapshot in OSS, and the readiness of the backup/restore engine. It
// backs the dashboard "OSS 存储信息" status light.
type StorageStatus struct {
	OSSConnected bool   `json:"oss_connected"` // can reach the OSS remote
	OSSDBExists  bool   `json:"oss_db_exists"` // oss.db.enc present in OSS
	OSSDBParsed  bool   `json:"oss_db_parsed"` // working DB is open & queryable
	Ready        bool   `json:"ready"`         // engine initialized, backup/restore usable
	OSSError     string `json:"oss_error,omitempty"`
	DBError      string `json:"db_error,omitempty"`
}

// StorageStatusFor reports current OSS / DB / engine health for the UI status
// light. Checks are short and best-effort: OSS reachability via Ping, oss.db
// presence via Exists, and working-DB parse via a connection Ping. Any failure
// surfaces as a false flag plus a descriptive error, never as a server error.
func (e *Engine) StorageStatusFor(ctx context.Context) StorageStatus {
	st := StorageStatus{Ready: e.Ready()}

	svc := e.getDBBackupSvc()
	if svc == nil {
		st.OSSError = "storage not configured"
		st.DBError = "storage not configured"
		return st
	}

	// 1. OSS reachability (backup/restore upload/download).
	if err := e.storage.Ping(ctx); err != nil {
		st.OSSError = err.Error()
		return st
	}
	st.OSSConnected = true

	// 2. Does OSS hold an authoritative DB snapshot?
	exists, err := svc.OSSDBExists(ctx)
	if err != nil {
		st.DBError = err.Error()
		return st
	}
	st.OSSDBExists = exists

	// 3. Can the working DB be opened/queried? (parse check)
	if err := e.db.Conn().Ping(); err != nil {
		st.DBError = err.Error()
		return st
	}
	st.OSSDBParsed = true

	return st
}

// RunBackup executes a backup synchronously.
func (e *Engine) RunBackup(ctx context.Context) error {
	e.mu.Lock()
	if err := e.preBackupCheck(); err != nil {
		e.mu.Unlock()
		return err
	}

	// Pull latest DB from OSS before creating the backup record, so the
	// record lives in the freshest possible state.
	if _, err := e.pullAndPreserveRestoreJobs(ctx); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("pull oss.db before backup: %w", err)
	}

	backupID, err := e.db.BackupRepo.Create()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	return e.executeBackup(ctx, backupID)
}

// StartBackup creates a backup record and starts the backup asynchronously.
// Returns the backup ID immediately so callers can track progress via the API.
func (e *Engine) StartBackup() (int64, error) {
	e.mu.Lock()
	if err := e.preBackupCheck(); err != nil {
		e.mu.Unlock()
		return 0, err
	}

	// Pull latest DB from OSS before creating the backup record, so the
	// record lives in the freshest possible state.
	if _, err := e.pullAndPreserveRestoreJobs(context.Background()); err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("pull oss.db before backup: %w", err)
	}

	backupID, err := e.db.BackupRepo.Create()
	if err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("create backup record: %w", err)
	}
	e.mu.Unlock()

	go func() {
		ctx := context.Background()
		if err := e.executeBackup(ctx, backupID); err != nil {
			e.logger.Error("async backup failed", "backup_id", backupID, "error", err)
		}
	}()

	return backupID, nil
}

// preBackupCheck performs the mutual-exclusion checks that must hold while
// the engine mutex is locked. Returns an error if a backup or restore is
// already running. Caller must hold e.mu.
func (e *Engine) preBackupCheck() error {
	restoreRunning, _ := e.db.RestoreJobRepo.IsRunning()
	if restoreRunning {
		return fmt.Errorf("a restore is currently running; backup and restore cannot run concurrently")
	}
	if e.runningBackupID > 0 {
		return fmt.Errorf("a backup is already running")
	}
	running, err := e.db.BackupRepo.IsRunning()
	if err != nil {
		return fmt.Errorf("check running backup: %w", err)
	}
	if running {
		return fmt.Errorf("a backup is already running")
	}
	return nil
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

	// Safety guard: refuse to delete objects when the local database appears
	// empty but there are active (ref_count > 0) hashes. This detects the
	// pathological scenario where hash_index was somehow wiped but leftover
	// orphan records (ref_count=0) point at objects that GC would delete.
	// In a shared-bucket setup the objects could still belong to another
	// environment's backup. When hash_count is 0 we simply have no basis for
	// deciding what is safe to delete — abort.
	if hashCount, hErr := e.db.HashRepo.CountAll(); hErr == nil && hashCount == 0 {
		return fmt.Errorf(
			"garbage collection aborted: hash_index is empty. Without a hash index " +
				"there is no way to distinguish orphan objects from in-use ones. " +
				"Run the service so it pulls the DB from OSS first")
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
func (e *Engine) executeBackup(ctx context.Context, backupID int64) (retErr error) {
	// Set up cancellation tracking.
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancelFuncs[backupID] = cancel
	e.runningBackupID = backupID
	e.mu.Unlock()

	// failedFiles tracks the number of files that failed during processing.
	// A partial failure (some files succeed, some fail) results in
	// status=completed_with_errors rather than a total failure.
	var failedFiles int
	var fileErrors []string

	// Cleanup cancellation tracking on exit (runs last due to LIFO).
	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, backupID)
		e.runningBackupID = 0
		e.mu.Unlock()
	}()

	// Sync the DB to OSS (runs after UpdateStatus, before cleanup). Must
	// happen AFTER the terminal status (completed / completed_with_errors) is
	// written: this deferred PushOSSDB uploads the authoritative oss.db, and if
	// it ran before UpdateStatus the OSS snapshot would still carry a "running"
	// backup that a later pull would bring back locally, permanently blocking
	// restore with "a backup is currently running". Hard failures (retErr != nil)
	// skip the push, matching the original in-pipeline behaviour.
	defer func() {
		if retErr != nil {
			return
		}
		if svc := e.getDBBackupSvc(); svc != nil {
			pushCtx, pushCancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if err := svc.PushOSSDB(pushCtx); err != nil {
				e.logger.Error("failed to push oss.db to OSS after backup", "error", err)
				e.logEvent(backupID, models.LogLevelWarn, "数据库同步到OSS失败", err.Error())
			} else {
				e.logEvent(backupID, models.LogLevelInfo, "数据库已同步到OSS", "")
			}
			pushCancel()
		}
	}()

	// Update backup status on exit (runs before cleanup).
	defer func() {
		if retErr != nil {
			// A hard error (scan failure, dedup failure, etc.) before or during
			// file processing — mark as failed/cancelled.
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
		} else if failedFiles > 0 {
			// All pipeline phases completed but some individual files failed.
			errMsg := fmt.Sprintf("%d file(s) failed: %s", failedFiles, strings.Join(fileErrors, "; "))
			if len(errMsg) > 2000 {
				errMsg = errMsg[:2000] + "..."
			}
			_ = e.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusCompletedWithErrors, errMsg)
			e.logEvent(backupID, models.LogLevelWarn, "backup completed with errors",
				fmt.Sprintf("failed=%d", failedFiles))
			if e.progress != nil {
				e.progress.PublishPhase(backupID, models.PhaseCompleted,
					fmt.Sprintf("备份完成（%d 个文件失败）", failedFiles))
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
	e.logger.Info("backup started", "backup_id", backupID)
	e.logEvent(backupID, models.LogLevelInfo, "backup started", "")
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
	// There is no full/incremental distinction: every backup processes all
	// tracked files. Unchanged files are rewritten to Modified so the dedup
	// pipeline records them in backup_files (their hash already exists in
	// hash_index, so no upload happens — dedup skip).
	var (
		changedFiles []scanner.FileChange
		deletedPaths []string
	)
	fileChangeMap := make(map[string]scanner.FileChange)
	for _, change := range scanResult.Changes {
		fileChangeMap[change.Path] = change
		switch change.ChangeType {
		case scanner.Deleted:
			deletedPaths = append(deletedPaths, change.Path)
		case scanner.Unchanged:
			// Rewrite Unchanged → Modified so dedup processes them and they
			// get a backup_files entry for this session.
			rewritten := change
			rewritten.ChangeType = scanner.Modified
			changedFiles = append(changedFiles, rewritten)
		case scanner.Added, scanner.Modified:
			changedFiles = append(changedFiles, change)
		}
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

	tmpParent := e.config.TempDir()
	if err := os.MkdirAll(tmpParent, 0o700); err != nil {
		return fmt.Errorf("create temp parent directory %q: %w", tmpParent, err)
	}
	tmpDir, err := os.MkdirTemp(tmpParent, "nas-backup-*")
	if err != nil {
		return fmt.Errorf("create temp directory in %q: %w", tmpParent, err)
	}
	defer os.RemoveAll(tmpDir)
	e.logger.Info("using temp directory", "dir", tmpDir)

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
		encIV        string
		compressType string
		storedSize   int64
	}
	pendingByHash := make(map[string]pendingMeta)

	for i := range dedupResult.ToUpload {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entry := &dedupResult.ToUpload[i]

		e.progress.PublishLog(backupID, "info", "开始处理文件",
			fmt.Sprintf("[%d/%d] %s (%s)", i+1, len(dedupResult.ToUpload), entry.Path, formatSize(entry.Size)))

		// Pre-flight disk space check: require at least 2x the file size as free space
		// in the temp directory (compressed + encrypted intermediates).
		// For large files this prevents ENosPC halfway through encryption.
		if entry.Size > 100*1024*1024 { // only check files > 100MB to avoid statfs overhead
			freeBytes, spaceErr := availableDiskSpace(tmpDir)
			if spaceErr != nil {
				e.logger.Warn("cannot check available disk space", "dir", tmpDir, "error", spaceErr)
			} else {
				requiredBytes := entry.Size * 2
				if freeBytes < requiredBytes {
					errMsg := fmt.Sprintf("insufficient temp space: have %s, need ~%s for %s",
						formatSize(freeBytes), formatSize(requiredBytes), entry.Path)
					e.logEvent(backupID, models.LogLevelError, "文件处理失败",
						fmt.Sprintf("path=%s error=%s", entry.Path, errMsg))
					e.logger.Error("skipping file due to insufficient temp space",
						"path", entry.Path, "free", freeBytes, "required", requiredBytes)
					failedFiles++
					fileErrors = append(fileErrors, fmt.Sprintf("%s: %s", entry.Path, errMsg))
					// Upsert file record first so we can mark it failed
					fileID, upsertErr := e.db.FileRepo.Upsert(entry.Path, entry.Size, entry.ModTime, entry.NewHash, entry.Inode)
					if upsertErr == nil {
						_ = e.db.FileRepo.MarkBackupFailed(fileID, backupID, errMsg)
					}
					continue
				}
			}
		}

		fileID, origSize, storedSize, compressType, storageKey, encIV, err := e.processAndUploadFile(
			ctx, entry, tmpDir, backupID)
		if err != nil {
			e.logEvent(backupID, models.LogLevelError, "文件处理失败",
				fmt.Sprintf("path=%s error=%v", entry.Path, err))
			e.logger.Error("file processing failed, continuing with next file",
				"path", entry.Path, "error", err)
			failedFiles++
			shortErr := err.Error()
			if len(shortErr) > 300 {
				shortErr = shortErr[:300] + "..."
			}
			fileErrors = append(fileErrors, fmt.Sprintf("%s: %s", entry.Path, shortErr))
			// Mark the file as failed in the DB. If we have a fileID (from upsert
			// inside processAndUploadFile before the failure), use it; otherwise
			// upsert first then mark.
			if fileID > 0 {
				_ = e.db.FileRepo.MarkBackupFailed(fileID, backupID, err.Error())
			} else {
				// Upsert may have failed; try one more time just to get an ID for status tracking.
				if fid, upsertErr := e.db.FileRepo.Upsert(entry.Path, entry.Size, entry.ModTime, entry.NewHash, entry.Inode); upsertErr == nil {
					_ = e.db.FileRepo.MarkBackupFailed(fid, backupID, err.Error())
				}
			}
			// Clean up any partial temp files for this fileID to free space.
			if fileID > 0 {
				os.Remove(filepath.Join(tmpDir, fmt.Sprintf("%d_compressed", fileID)))
				os.Remove(filepath.Join(tmpDir, fmt.Sprintf("%d_encrypted.enc", fileID)))
			}
			continue
		}

		// Mark successful backup for this file.
		if markErr := e.db.FileRepo.MarkBackupSuccess(fileID, backupID); markErr != nil {
			e.logger.Warn("failed to mark file backup success", "file_id", fileID, "error", markErr)
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
		var upsertErr error
		fc, hasFC := fileChangeMap[skipped.Path]
		if hasFC {
			fileID, upsertErr = e.db.FileRepo.Upsert(fc.Path, fc.Size, fc.ModTime, fc.NewHash, fc.Inode)
			if upsertErr != nil {
				e.logger.Error("upsert file record for dedup'd file",
					"path", skipped.Path, "error", upsertErr)
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

		// Mark dedup-skipped files as successfully backed up (content already exists in OSS).
		if markErr := e.db.FileRepo.MarkBackupSuccess(fileID, backupID); markErr != nil {
			e.logger.Warn("failed to mark dedup'd file backup success", "file_id", fileID, "error", markErr)
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
		fmt.Sprintf("total_files=%d failed=%d total_size=%s uploaded=%s",
			totalFiles, failedFiles, formatSize(totalOriginalSize), formatSize(totalUploadedSize)))
	if err := e.db.BackupRepo.UpdateStats(backupID,
		totalFiles,
		int(totalOriginalSize),
		int(totalUploadedSize),
		len(dedupResult.Skipped),
		failedFiles,
		compressSaved,
	); err != nil {
		return fmt.Errorf("update backup stats: %w", err)
	}
	if e.progress != nil {
		e.progress.PublishProgress(backupID, models.PhaseFinalizing, 1, 1, 100)
	}

	if failedFiles > 0 {
		e.logger.Warn("backup completed with errors",
			"backup_id", backupID,
			"total_files", totalFiles,
			"failed_files", failedFiles,
			"total_size", totalOriginalSize,
			"uploaded_size", totalUploadedSize,
			"skipped_dedup", len(dedupResult.Skipped),
			"compress_saved", compressSaved,
			"deleted", len(deletedPaths))
	} else {
		e.logger.Info("backup completed successfully",
			"backup_id", backupID,
			"total_files", totalFiles,
			"total_size", totalOriginalSize,
			"uploaded_size", totalUploadedSize,
			"skipped_dedup", len(dedupResult.Skipped),
			"compress_saved", compressSaved,
			"deleted", len(deletedPaths))
	}

	// PushOSSDB now runs in a deferred function AFTER the terminal status is
	// written (see the defer at the top of executeBackup), so the authoritative
	// oss.db never carries a "running" backup.

	return nil
}

// processAndUploadFile handles compress → encrypt → upload → verify for a single file.
func (e *Engine) processAndUploadFile(
	ctx context.Context,
	entry *dedup.DedupFileEntry,
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
		storageKey = e.generateStorageKey(entry.NewHash)
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

// generateStorageKey builds the OSS object key for a file content hash.
// The key is content-addressed (hash-based), with no backup-type prefix:
//   data/<hash-prefix>/<hash>.enc
func (e *Engine) generateStorageKey(hash string) string {
	hashPrefix := "00"
	if len(hash) >= 2 {
		hashPrefix = hash[:2]
	}
	return fmt.Sprintf("data/%s/%s.enc", hashPrefix, hash)
}

// availableDiskSpace returns the number of free bytes available on the filesystem containing dir.
func availableDiskSpace(dir string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", dir, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
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
