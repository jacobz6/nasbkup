package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nas-backup/internal/backup"
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
			// If the backup is not in memory (e.g. process restarted), try to
			// mark it as failed in the DB so the user can start a new backup.
			_ = r.db.BackupRepo.UpdateStatus(backupID, models.BackupStatusFailed, "cancelled (process restarted)")
			r.jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		r.jsonResponse(w, map[string]string{"status": "cancelled"}, http.StatusOK)
		return
	}

	// Try to find the running backup from in-memory state first.
	runningID, ok := r.engine.RunningBackupID()
	if ok {
		if err := r.engine.Cancel(runningID); err != nil {
			r.jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		r.jsonResponse(w, map[string]string{"status": "cancelled"}, http.StatusOK)
		return
	}

	// No in-memory running backup — check DB for stale "running" records (e.g.
	// left over from a process crash) and clean them up.
	runningDB, err := r.db.BackupRepo.GetRunning()
	if err == nil && runningDB != nil {
		_ = r.db.BackupRepo.UpdateStatus(runningDB.ID, models.BackupStatusFailed, "cancelled (stale record)")
		r.jsonResponse(w, map[string]string{"status": "cancelled", "note": "cleared stale running record"}, http.StatusOK)
		return
	}

	r.jsonError(w, "no backup is currently running", http.StatusNotFound)
}

// handleBackupStatus returns current backup status.
func (r *Router) handleBackupStatus(w http.ResponseWriter, req *http.Request) {
	dbRunning, _ := r.db.BackupRepo.IsRunning()
	runningID, memRunning := r.engine.RunningBackupID()
	isRunning := dbRunning || memRunning

	var runningBackup *models.BackupRecord
	if runningID > 0 {
		runningBackup, _ = r.db.BackupRepo.GetByID(runningID)
	} else if dbRunning {
		runningBackup, _ = r.db.BackupRepo.GetRunning()
	}

	r.jsonResponse(w, map[string]interface{}{
		"is_running":     isRunning,
		"running_backup": runningBackup,
	}, http.StatusOK)
}

// handleBackupProgressStream serves Server-Sent Events for real-time backup progress.
func (r *Router) handleBackupProgressStream(w http.ResponseWriter, req *http.Request) {
	// 用 ResponseController 穿透中间件包装层（如 statusWriter），
	// 获取底层的 Flusher 并禁用写超时。
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		slog.Warn("failed to disable write deadline for SSE", "error", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	pb := r.engine.ProgressBroker()
	ch, unsub := pb.Subscribe()
	defer unsub()

	connectedEvent := models.ProgressEvent{
		Type:      "connected",
		Message:   "connected",
		Timestamp: time.Now(),
	}
	if err := backup.WriteSSEEvent(w, connectedEvent); err != nil {
		return
	}
	_ = rc.Flush()

	runningID, memRunning := r.engine.RunningBackupID()
	if memRunning && runningID > 0 {
		rec, dbErr := r.db.BackupRepo.GetByID(runningID)
		if dbErr != nil {
			slog.Warn("failed to get running backup record for SSE",
				"backup_id", runningID, "error", dbErr)
		}
		if rec != nil {
			statusEvent := models.ProgressEvent{
				Type:      "phase",
				BackupID:  runningID,
				Phase:     models.PhaseUploading,
				PhaseName: "备份运行中",
				Message:   fmt.Sprintf("备份 #%d 正在运行", runningID),
				Timestamp: time.Now(),
			}
			_ = backup.WriteSSEEvent(w, statusEvent)
			_ = rc.Flush()
		}
	}

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
			if err := backup.WriteSSEEvent(w, event); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}
