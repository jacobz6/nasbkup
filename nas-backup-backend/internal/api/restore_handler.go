package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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

// handleRestore restores files from backup.
func (r *Router) handleRestore(w http.ResponseWriter, req *http.Request) {
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

	// Validate output directory exists and is a directory.
	info, err := os.Stat(restoreReq.OutputDir)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("output_dir does not exist: %v", err), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		r.jsonError(w, "output_dir must be a directory", http.StatusBadRequest)
		return
	}

	// Use a detached context with a generous timeout: restore may need to wait
	// for ColdArchive thaw (up to 30 min/object). Binding to req.Context()
	// caused the restore to be cancelled when the HTTP client disconnected or
	// the server's WriteTimeout elapsed, leaving partial/pending restores.
	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()

	result, err := r.restorer.Restore(ctx, &restoreReq)
	if err != nil {
		// Client may already be gone; log the failure either way.
		slog.Error("restore failed",
			"output_dir", restoreReq.OutputDir, "error", err)
		r.jsonError(w, fmt.Sprintf("restore failed: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("restore completed",
		"restored", result.RestoredFiles, "failed", len(result.FailedFiles),
		"output_dir", restoreReq.OutputDir)
	r.jsonResponse(w, result, http.StatusOK)
}

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
