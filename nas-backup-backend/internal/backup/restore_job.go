package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
)

// RestoreJobManager manages the lifecycle of async restore jobs.
// It creates, starts, cancels, and tracks restore operations.
type RestoreJobManager struct {
	db       *db.Database
	restorer *Restorer
	progress *RestoreProgressBroker
	config   *config.AppConfig

	mu          sync.Mutex
	activeJobID int64
	cancelFuncs map[int64]context.CancelFunc
}

// NewRestoreJobManager creates a new RestoreJobManager.
func NewRestoreJobManager(database *db.Database, restorer *Restorer, progress *RestoreProgressBroker, cfg *config.AppConfig) *RestoreJobManager {
	return &RestoreJobManager{
		db:         database,
		restorer:   restorer,
		progress:   progress,
		config:     cfg,
		cancelFuncs: make(map[int64]context.CancelFunc),
	}
}

// CreateJob validates the request, resolves files, persists a restore_jobs
// record in pending status, and returns the job record. The caller is
// responsible for calling StartJob to actually begin the restore.
func (m *RestoreJobManager) CreateJob(req *models.RestoreRequest) (*models.RestoreJobRecord, error) {
	// --- mutual exclusion: check no backup is running ---
	backupRunning, _ := m.db.BackupRepo.IsRunning()
	if backupRunning {
		return nil, fmt.Errorf("a backup is currently running; restore and backup cannot run concurrently")
	}

	// --- check no other restore is running ---
	restoreRunning, _ := m.db.RestoreJobRepo.IsRunning()
	if restoreRunning {
		return nil, fmt.Errorf("a restore is already running")
	}

	// --- validate output directory ---
	// Skip output dir validation when restoring to original paths.
	if !req.RestoreToOriginal {
		allowedDirs := m.allowedRestoreDirs()
		if err := ValidateOutputDir(req.OutputDir, allowedDirs); err != nil {
			return nil, err
		}

		// --- ensure output dir exists and is a directory ---
		if fi, err := os.Stat(req.OutputDir); err != nil {
			if os.IsNotExist(err) {
				if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
					return nil, fmt.Errorf("create output directory %q: %w", req.OutputDir, err)
				}
			} else {
				return nil, fmt.Errorf("stat output directory %q: %w", req.OutputDir, err)
			}
		} else if !fi.IsDir() {
			return nil, fmt.Errorf("output path %q exists but is not a directory", req.OutputDir)
		}
	}

	// --- resolve files to get total count/size ---
	files, err := m.restorer.ResolveFilesForPreview(req)
	if err != nil {
		return nil, fmt.Errorf("resolve files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found matching the request")
	}

	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}

	conflictStrategy := "skip"
	if req.ConflictStrategy != "" {
		conflictStrategy = req.ConflictStrategy
	}

	// For "restore to original", store a sentinel in OutputDir for the
	// job record so the restore worker knows to use original paths.
	outputDir := req.OutputDir
	if req.RestoreToOriginal {
		outputDir = "__original__"
	}

	rec := &models.RestoreJobRecord{
		Status:           models.RestoreJobStatusPending,
		Paths:            req.Paths,
		Pattern:          req.Pattern,
		BackupID:         req.BackupID,
		OutputDir:        outputDir,
		Expedited:        req.Expedited,
		ConflictStrategy: conflictStrategy,
		TotalFiles:       len(files),
		TotalSize:        totalSize,
	}

	id, err := m.db.RestoreJobRepo.Create(rec)
	if err != nil {
		return nil, fmt.Errorf("persist restore job: %w", err)
	}
	rec.ID = id

	return rec, nil
}

// StartJob asynchronously executes a restore job in a goroutine.
// The job must already exist in the database with status 'pending'.
func (m *RestoreJobManager) StartJob(jobID int64) error {
	job, err := m.db.RestoreJobRepo.GetByID(jobID)
	if err != nil {
		return fmt.Errorf("get job %d: %w", jobID, err)
	}
	if job == nil {
		return fmt.Errorf("restore job %d not found", jobID)
	}
	if job.Status != models.RestoreJobStatusPending {
		return fmt.Errorf("restore job %d is not in pending status (current: %s)", jobID, job.Status)
	}

	// Parse paths from stored JSON.
	req := &models.RestoreRequest{
		Paths:             job.Paths,
		Pattern:           job.Pattern,
		BackupID:          job.BackupID,
		OutputDir:         job.OutputDir,
		RestoreToOriginal: job.OutputDir == "__original__",
		Expedited:         job.Expedited,
		ConflictStrategy:  job.ConflictStrategy,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)

	m.mu.Lock()
	m.activeJobID = jobID
	m.cancelFuncs[jobID] = cancel
	m.mu.Unlock()

	go m.executeJob(ctx, cancel, jobID, req)

	return nil
}

// executeJob runs the restore in a goroutine, updating the job record and
// publishing SSE events throughout.
func (m *RestoreJobManager) executeJob(ctx context.Context, cancel context.CancelFunc, jobID int64, req *models.RestoreRequest) {
	defer func() {
		m.mu.Lock()
		delete(m.cancelFuncs, jobID)
		if m.activeJobID == jobID {
			m.activeJobID = 0
		}
		m.mu.Unlock()
		cancel()
	}()

	// Fetch the job record to get total counts (captured once at start).
	job, err := m.db.RestoreJobRepo.GetByID(jobID)
	if err != nil || job == nil {
		slog.Error("failed to load restore job for execution", "job_id", jobID, "error", err)
		return
	}

	// Mark as running.
	if err := m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusRunning, ""); err != nil {
		slog.Error("failed to mark restore job as running", "job_id", jobID, "error", err)
		return
	}
	m.progress.ClearHistory()
	m.progress.PublishPhase(jobID, models.RestorePhasePreparing, "开始准备恢复任务...")

	start := time.Now()

	// Track progress in real-time via callback.
	var (
		mu          sync.Mutex
		restoredCount int
		restoredSize  int64
		failedPaths   []string
	)

	// Capture total values once at start to avoid per-file DB queries.
	totalFiles := job.TotalFiles
	totalSize := job.TotalSize

	onProgress := func(filePath string, fileSize int64, restored bool, err error) {
		mu.Lock()
		defer mu.Unlock()

		if restored {
			restoredCount++
			restoredSize += fileSize
			m.progress.PublishFile(jobID, filePath, fileSize, "已恢复")
		} else {
			failedPaths = append(failedPaths, filePath)
			msg := fmt.Sprintf("恢复失败: %s", filePath)
			if err != nil {
				msg = fmt.Sprintf("%s: %v", msg, err)
			}
			m.progress.PublishLog(jobID, "error", msg, "")
		}

		var percent float64
		if totalFiles > 0 {
			percent = float64(restoredCount) / float64(totalFiles) * 100
		}
		m.progress.PublishProgress(jobID, restoredCount, totalFiles, percent, restoredSize, totalSize)

		// Update DB periodically (every 10 files or on failure to reduce write pressure).
		if restoredCount%10 == 0 || !restored {
			_ = m.db.RestoreJobRepo.UpdateProgress(jobID, restoredCount, restoredSize, failedPaths)
		}
	}

	opts := &RestoreOptions{
		ConflictStrategy: req.ConflictStrategy,
		OnFileProgress:   onProgress,
	}

	m.progress.PublishPhase(jobID, models.RestorePhaseDownloading, "正在恢复文件...")
	result, err := m.restorer.RestoreWithOptions(ctx, req, opts)

	elapsedMs := time.Since(start).Milliseconds()

	mu.Lock()
	finalFailed := make([]string, len(failedPaths))
	copy(finalFailed, failedPaths)
	mu.Unlock()

	// Collect final counters from result (if available), otherwise fall back to callback-tracked values.
	finalRestoredFiles := restoredCount
	finalRestoredSize := restoredSize
	if result != nil {
		finalRestoredFiles = result.RestoredFiles
		finalRestoredSize = result.TotalSize
		finalFailed = append(finalFailed, result.FailedFiles...)
	}

	if err != nil {
		if ctx.Err() != nil {
			// Cancelled.
			_ = m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusCancelled, "cancelled by user")
			m.progress.PublishPhase(jobID, models.RestorePhaseCancelled, "恢复已取消")
		} else {
			_ = m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusFailed, err.Error())
			m.progress.PublishPhase(jobID, models.RestorePhaseFailed, fmt.Sprintf("恢复失败: %v", err))
		}
		_ = m.db.RestoreJobRepo.UpdateCompleted(jobID, finalRestoredFiles, finalRestoredSize, elapsedMs, finalFailed)
		return
	}

	// Flush final progress.
	_ = m.db.RestoreJobRepo.UpdateProgress(jobID, finalRestoredFiles, finalRestoredSize, finalFailed)
	_ = m.db.RestoreJobRepo.UpdateCompleted(jobID, finalRestoredFiles, finalRestoredSize, elapsedMs, finalFailed)

	// If all files failed (0 restored, some failed), mark the job as failed
	// rather than completed — the user needs to know the restore didn't work.
	if finalRestoredFiles == 0 && len(finalFailed) > 0 {
		failSummary := fmt.Sprintf("所有 %d 个文件恢复均失败; 首个失败: %s", len(finalFailed), finalFailed[0])
		_ = m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusFailed, failSummary)
		m.progress.PublishPhase(jobID, models.RestorePhaseFailed, failSummary)
		return
	}

	// Mark completed (partial success or full success).
	_ = m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusCompleted, "")
	m.progress.PublishProgress(jobID, finalRestoredFiles, totalFiles, 100, finalRestoredSize, totalSize)
	m.progress.PublishPhase(jobID, models.RestorePhaseCompleted,
		fmt.Sprintf("恢复完成: %d/%d 个文件, %d 个失败", finalRestoredFiles, totalFiles, len(finalFailed)))
}

// CancelJob cancels a running restore job.
func (m *RestoreJobManager) CancelJob(jobID int64) error {
	m.mu.Lock()
	cancel, ok := m.cancelFuncs[jobID]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active restore job %d found (may have already completed)", jobID)
	}

	cancel()
	_ = m.db.RestoreJobRepo.UpdateStatus(jobID, models.RestoreJobStatusCancelled, "cancelled by user")
	m.progress.PublishPhase(jobID, models.RestorePhaseCancelled, "恢复已取消")
	return nil
}

// GetJob returns a restore job by ID.
func (m *RestoreJobManager) GetJob(jobID int64) (*models.RestoreJobRecord, error) {
	return m.db.RestoreJobRepo.GetByID(jobID)
}

// ListJobs lists restore jobs with pagination and optional status filter.
func (m *RestoreJobManager) ListJobs(page, size int, status string) ([]*models.RestoreJobRecord, int64, error) {
	offset := (page - 1) * size
	return m.db.RestoreJobRepo.List(size, offset, status)
}

// GetActiveJobID returns the currently active (running) restore job ID, or 0 if none.
func (m *RestoreJobManager) GetActiveJobID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeJobID
}

// CleanupStaleRunning marks any stale running/pending jobs as failed.
// Called at application startup.
func (m *RestoreJobManager) CleanupStaleRunning() (int64, error) {
	n, err := m.db.RestoreJobRepo.CleanupStaleRunning()
	if n > 0 {
		slog.Info("cleaned up stale restore jobs", "count", n)
	}
	return n, err
}

// ProgressBroker returns the underlying RestoreProgressBroker for SSE subscription.
func (m *RestoreJobManager) ProgressBroker() *RestoreProgressBroker {
	return m.progress
}

// allowedRestoreDirs returns the list of allowed base directories for restore output.
// If the config does not specify restore_base_dirs, the backup directories are used.
func (m *RestoreJobManager) allowedRestoreDirs() []string {
	if m.config == nil {
		return nil
	}
	if len(m.config.Server.RestoreBaseDirs) > 0 {
		return m.config.Server.RestoreBaseDirs
	}
	// Fall back to backup directories.
	dirs := make([]string, 0)
	for _, d := range m.config.Backup.Directories {
		if d.Enabled {
			dirs = append(dirs, filepath.Clean(d.Path))
		}
	}
	return dirs
}
