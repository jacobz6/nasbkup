package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/nas-backup/internal/models"
)

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

	result, err := r.restorer.Restore(context.Background(), &restoreReq)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("restore failed: %v", err), http.StatusInternalServerError)
		return
	}

	r.jsonResponse(w, result, http.StatusOK)
}

// handleGarbageCollection triggers garbage collection asynchronously.
func (r *Router) handleGarbageCollection(w http.ResponseWriter, req *http.Request) {
	go func() {
		if err := r.engine.RunGarbageCollection(context.Background()); err != nil {
			_ = r.db.LogRepo.Insert(nil, models.LogLevelError,
				"garbage collection failed", err.Error())
		} else {
			_ = r.db.LogRepo.Insert(nil, models.LogLevelInfo,
				"garbage collection completed", "")
		}
	}()

	r.jsonResponse(w, map[string]string{"status": "started"}, http.StatusAccepted)
}
