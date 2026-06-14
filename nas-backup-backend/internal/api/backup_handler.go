package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/nas-backup/internal/models"
)

// handleBackupTrigger starts a new backup job.
func (r *Router) handleBackupTrigger(w http.ResponseWriter, req *http.Request) {
	var triggerReq models.BackupTriggerRequest
	if err := json.NewDecoder(req.Body).Decode(&triggerReq); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if triggerReq.Type != models.BackupTypeFull && triggerReq.Type != models.BackupTypeIncremental {
		r.jsonError(w, "type must be 'full' or 'incremental'", http.StatusBadRequest)
		return
	}

	backupID, err := r.engine.StartBackup(triggerReq.Type)
	if err != nil {
		r.jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	r.jsonResponse(w, map[string]interface{}{
		"backup_id": backupID,
		"status":    "pending",
	}, http.StatusAccepted)
}

// handleBackupCancel cancels a running backup.
func (r *Router) handleBackupCancel(w http.ResponseWriter, req *http.Request) {
	backupIDStr := req.URL.Query().Get("backup_id")
	if backupIDStr != "" {
		backupID, err := strconv.ParseInt(backupIDStr, 10, 64)
		if err != nil {
			r.jsonError(w, "invalid backup_id", http.StatusBadRequest)
			return
		}
		if err := r.engine.Cancel(backupID); err != nil {
			r.jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		r.jsonResponse(w, map[string]string{"status": "cancelled"}, http.StatusOK)
		return
	}

	// Try to find the running backup automatically.
	runningID, ok := r.engine.RunningBackupID()
	if !ok {
		r.jsonError(w, "no backup is currently running", http.StatusNotFound)
		return
	}
	if err := r.engine.Cancel(runningID); err != nil {
		r.jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	r.jsonResponse(w, map[string]string{"status": "cancelled"}, http.StatusOK)
}

// handleBackupStatus returns current backup status.
func (r *Router) handleBackupStatus(w http.ResponseWriter, req *http.Request) {
	isRunning, _ := r.db.BackupRepo.IsRunning()
	runningID, _ := r.engine.RunningBackupID()

	var runningBackup *models.BackupRecord
	if runningID > 0 {
		runningBackup, _ = r.db.BackupRepo.GetByID(runningID)
	}

	r.jsonResponse(w, map[string]interface{}{
		"is_running":     isRunning,
		"running_backup": runningBackup,
	}, http.StatusOK)
}
