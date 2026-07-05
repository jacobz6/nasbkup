package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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

// handleGarbageCollection triggers garbage collection asynchronously.
func (r *Router) handleGarbageCollection(w http.ResponseWriter, req *http.Request) {
	go func() {
		if err := r.engine.RunGarbageCollection(context.Background()); err != nil {
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
