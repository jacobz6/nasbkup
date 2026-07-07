package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/models"
)

// restoreTimeout is the maximum duration for a restore operation. Restore may
// involve thawing ColdArchive/Archive objects, which can take up to 30 minutes
// per object (and longer for many objects). The restore runs detached from the
// HTTP request context so that a client disconnect does NOT cancel an in-flight
// thaw — previously req.Context() was cancelled when the client gave up,
// aborting the restore mid-flight.
const restoreTimeout = 4 * time.Hour

// reconcileTimeout is the maximum duration for a reconciliation pass. Listing
// all OSS objects + batch existence checks over a large bucket can take
// several minutes, so use a detached context to avoid being killed by the
// HTTP server's WriteTimeout (60s).
const reconcileTimeout = 30 * time.Minute

// --- Restore handlers (async job-based) ---

// handleRestoreCreate creates an async restore job and starts it.
// Replaces the old synchronous handleRestore.
func (r *Router) handleRestoreCreate(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		r.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var restoreReq models.RestoreRequest
	if err := json.NewDecoder(req.Body).Decode(&restoreReq); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(restoreReq.Paths) == 0 && restoreReq.Pattern == "" {
		r.jsonError(w, "paths or pattern is required", http.StatusBadRequest)
		return
	}
	if restoreReq.OutputDir == "" {
		r.jsonError(w, "output_dir is required", http.StatusBadRequest)
		return
	}

	// Delegate to RestoreJobManager for async job creation + start.
	job, err := r.restoreJobMgr.CreateJob(&restoreReq)
	if err != nil {
		r.jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	// Start the job asynchronously.
	if err := r.restoreJobMgr.StartJob(job.ID); err != nil {
		r.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	r.jsonResponse(w, map[string]interface{}{
		"job_id":      job.ID,
		"status":      job.Status,
		"total_files": job.TotalFiles,
		"total_size":  job.TotalSize,
	}, http.StatusAccepted)
}

// handleRestoreListFiles lists restorable files with pagination and search.
// GET /api/restore/files?dir_path=...&backup_id=...&search=...&page=1&size=20
func (r *Router) handleRestoreListFiles(w http.ResponseWriter, req *http.Request) {
	dirPath := req.URL.Query().Get("dir_path")
	search := req.URL.Query().Get("search")
	backupIDStr := req.URL.Query().Get("backup_id")
	page, size := parsePagination(req)

	var backupID *int64
	if backupIDStr != "" {
		id, err := strconv.ParseInt(backupIDStr, 10, 64)
		if err != nil {
			r.jsonError(w, "invalid backup_id", http.StatusBadRequest)
			return
		}
		backupID = &id
	}

	files, err := r.restorer.ListRestorableFiles(dirPath, backupID)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list restorable files: %v", err), http.StatusInternalServerError)
		return
	}

	// Filter by search if provided.
	if search != "" {
		filtered := make([]*models.FileRecord, 0)
		for _, f := range files {
			if containsPath(f.Path, search) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}

	// Build enriched response with backup metadata.
	type enrichedFile struct {
		ID      int64  `json:"id"`
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		ModTime string `json:"mod_time"`
		Hash    string `json:"hash,omitempty"`
		Status  string `json:"status"`
	}

	total := int64(len(files))
	start := (page - 1) * size
	end := start + size
	if start > int(total) {
		start = int(total)
	}
	if end > int(total) {
		end = int(total)
	}

	result := make([]enrichedFile, 0, end-start)
	for _, f := range files[start:end] {
		result = append(result, enrichedFile{
			ID:      f.ID,
			Path:    f.Path,
			Size:    f.Size,
			ModTime: f.ModTime.Format(time.RFC3339),
			Hash:    f.Hash,
			Status:  string(f.Status),
		})
	}

	r.jsonPaginatedResponse(w, result, total, page, size)
}

// containsPath checks if the path contains the search string (case-insensitive).
func containsPath(path, search string) bool {
	return len(search) == 0 ||
		containsAny([]string{path}, search)
}

// handleRestoreProgressStream serves SSE for restore job progress.
func (r *Router) handleRestoreProgressStream(w http.ResponseWriter, req *http.Request) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Warn("failed to disable write deadline for restore SSE", "error", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	pb := r.restoreJobMgr.ProgressBroker()
	ch, history, unsub := pb.Subscribe()
	defer unsub()

	// Send connected event.
	connectedEvent := models.RestoreProgressEvent{
		Type:      "connected",
		Message:   "connected",
		Timestamp: time.Now(),
	}
	if err := backup.WriteRestoreSSEEvent(w, connectedEvent); err != nil {
		return
	}

	// Replay history.
	for _, event := range history {
		if err := backup.WriteRestoreSSEEvent(w, event); err != nil {
			return
		}
	}
	_ = rc.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := req.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			_ = rc.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := backup.WriteRestoreSSEEvent(w, event); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// handleRestoreListJobs lists restore job history.
// GET /api/restore/jobs?page=1&size=10&status=completed
func (r *Router) handleRestoreListJobs(w http.ResponseWriter, req *http.Request) {
	page, size := parsePagination(req)
	status := req.URL.Query().Get("status")

	jobs, total, err := r.restoreJobMgr.ListJobs(page, size, status)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list restore jobs: %v", err), http.StatusInternalServerError)
		return
	}

	if jobs == nil {
		jobs = make([]*models.RestoreJobRecord, 0)
	}

	r.jsonPaginatedResponse(w, jobs, total, page, size)
}

// handleRestoreGetJob returns a single restore job by ID.
func (r *Router) handleRestoreGetJob(w http.ResponseWriter, req *http.Request) {
	idStr := req.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		r.jsonError(w, "invalid job id", http.StatusBadRequest)
		return
	}

	job, err := r.restoreJobMgr.GetJob(id)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("get restore job: %v", err), http.StatusInternalServerError)
		return
	}
	if job == nil {
		r.jsonError(w, "restore job not found", http.StatusNotFound)
		return
	}

	r.jsonResponse(w, job, http.StatusOK)
}

// handleRestoreCancelJob cancels a running restore job.
func (r *Router) handleRestoreCancelJob(w http.ResponseWriter, req *http.Request) {
	idStr := req.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		r.jsonError(w, "invalid job id", http.StatusBadRequest)
		return
	}

	if err := r.restoreJobMgr.CancelJob(id); err != nil {
		r.jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	r.jsonResponse(w, map[string]string{"status": "cancelled"}, http.StatusOK)
}

// handleListBackups lists completed backup sessions for version selection.
// GET /api/backups?page=1&size=10
func (r *Router) handleListBackups(w http.ResponseWriter, req *http.Request) {
	page, size := parsePagination(req)
	offset := (page - 1) * size

	backups, total, err := r.db.BackupRepo.List(size, offset)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list backups: %v", err), http.StatusInternalServerError)
		return
	}

	if backups == nil {
		backups = make([]*models.BackupRecord, 0)
	}

	r.jsonPaginatedResponse(w, backups, total, page, size)
}

// --- Non-restore handlers (GC, Reconcile, Storage Health) ---

// gcTimeout is the maximum duration for a garbage collection pass. Deleting
// many orphan OSS objects can take several minutes; use a detached context
// with a generous timeout.
const gcTimeout = 30 * time.Minute

// handleGarbageCollection triggers garbage collection asynchronously.
func (r *Router) handleGarbageCollection(w http.ResponseWriter, req *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gcTimeout)
		defer cancel()
		if err := r.engine.RunGarbageCollection(ctx); err != nil {
			slog.Error("garbage collection failed", "error", err)
			_ = r.db.LogRepo.Insert(nil, models.LogLevelError,
				"garbage collection failed", err.Error())
		} else {
			slog.Info("garbage collection completed")
			_ = r.db.LogRepo.Insert(nil, models.LogLevelInfo,
				"garbage collection completed", "")
		}
	}()

	r.jsonResponse(w, map[string]string{"status": "started"}, http.StatusAccepted)
}

// handleReconcile runs a single reconciliation pass that syncs OSS objects,
// hash_index, backup_files, and backup status. The endpoint is synchronous
// because reconcile produces a detailed report that the operator wants to
// inspect immediately; long-running reconciles over very large OSS buckets
// are still bounded by the request context.
//
// Query parameters:
//   dry_run=true|false  override cfg.Reconcile.DryRun for this call.
//                       If omitted, the config default is used.
//
// If a backup is currently running, reconcile returns 409 Conflict so the
// caller can retry after the backup finishes.
func (r *Router) handleReconcile(w http.ResponseWriter, req *http.Request) {
	dryRun := r.config.Reconcile.DryRun
	if v := req.URL.Query().Get("dry_run"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			r.jsonError(w, "invalid dry_run query parameter (use true/false)", http.StatusBadRequest)
			return
		}
		dryRun = b
	}

	// Use a detached context with a generous timeout: reconcile over a large
	// bucket (listing all OSS objects, batch-checking failed backups) can take
	// several minutes. Binding to req.Context() caused reconciliation to be
	// cancelled when the server's WriteTimeout (60s) elapsed, aborting mid-fix.
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	report, err := r.engine.Reconcile(ctx, dryRun)
	if err != nil {
		// A running backup returns an error; surface it as 409 so the client
		// can distinguish from a real failure.
		if report != nil && len(report.Errors) > 0 && containsAny(report.Errors, "currently running") {
			r.jsonError(w, err.Error(), http.StatusConflict)
			return
		}
		slog.Error("reconcile failed", "dry_run", dryRun, "error", err)
		_ = r.db.LogRepo.Insert(nil, models.LogLevelError,
			"reconcile failed", err.Error())
		// Even on error we return the partial report so the operator can see
		// what was discovered before the failure.
		if report != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Error:   err.Error(),
				Data:    report,
			})
			return
		}
		r.jsonError(w, fmt.Sprintf("reconcile failed: %v", err), http.StatusInternalServerError)
		return
	}

	level := models.LogLevelInfo
	msg := "reconcile completed (dry-run, no fixes applied)"
	if !dryRun {
		msg = fmt.Sprintf("reconcile completed: %d fixes applied", len(report.AppliedFixes))
	}
	_ = r.db.LogRepo.Insert(nil, level, msg,
		fmt.Sprintf("oss_orphans=%d dangling_ref0=%d dangling_refn=%d orphan_bf=%d ref_mismatch=%d failed_with_files=%d completed_no_files=%d errors=%d",
			len(report.OSSOnlyOrphans),
			len(report.DanglingHashIndexesRefZero),
			len(report.DanglingHashIndexesRefNonZero),
			len(report.OrphanBackupFiles),
			len(report.RefCountMismatches),
			len(report.FailedBackupsWithFiles),
			len(report.CompletedBackupsNoFiles),
			len(report.Errors),
		))

	r.jsonResponse(w, report, http.StatusOK)
}

// containsAny reports whether any string in haystack contains substr.
func containsAny(haystack []string, substr string) bool {
	for _, s := range haystack {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// handleStorageHealth verifies OSS connectivity by pinging the configured remote.
// Returns 200 with status "ok" when the OSS remote responds, 503 with the
// error message when it does not. Used by operators to verify that the OSS
// configuration, credentials, and network path are all healthy.
func (r *Router) handleStorageHealth(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	err := r.restorer.PingStorage(req.Context())
	duration := time.Since(start)

	if err != nil {
		slog.Warn("storage health check failed",
			"error", err, "duration", duration)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Error:   fmt.Sprintf("OSS unreachable: %v", err),
			Data: map[string]interface{}{
				"latency_ms": duration.Milliseconds(),
			},
		})
		return
	}

	r.jsonResponse(w, map[string]interface{}{
		"status":     "ok",
		"latency_ms": duration.Milliseconds(),
	}, http.StatusOK)
}
