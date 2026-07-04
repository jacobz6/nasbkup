package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/nas-backup/internal/models"
)

// handleDashboardStats returns dashboard statistics.
func (r *Router) handleDashboardStats(w http.ResponseWriter, req *http.Request) {
	activeFiles, _ := r.db.FileRepo.CountByStatus(models.FileStatusActive)
	activeSize, _ := r.db.FileRepo.TotalSizeByStatus(models.FileStatusActive)
	dedupSaved, _ := r.db.HashRepo.TotalDedupSaved()
	latestBackup, _ := r.db.BackupRepo.GetLatestCompleted()
	isRunning, _ := r.db.BackupRepo.IsRunning()

	var lastBackupTime *time.Time
	var lastBackupStatus models.BackupStatus
	if latestBackup != nil {
		lastBackupTime = latestBackup.CompletedAt
		lastBackupStatus = latestBackup.Status
	}

	var nextBackupTime *time.Time
	if r.scheduler != nil && r.scheduler.IsEnabled() {
		next := r.scheduler.NextRun()
		if !next.IsZero() {
			nextBackupTime = &next
		}
	}

	stats := &models.DashboardStats{
		TotalFiles:          activeFiles,
		TotalSize:           activeSize,
		BackedUpFiles:       activeFiles,
		BackedUpSize:        activeSize,
		LastBackupTime:      lastBackupTime,
		LastBackupStatus:    lastBackupStatus,
		NextBackupTime:      nextBackupTime,
		SavedByDedup:        dedupSaved,
		ActiveBackupRunning: isRunning,
	}

	r.jsonResponse(w, stats, http.StatusOK)
}

// handleDashboardHistory returns backup history with pagination.
func (r *Router) handleDashboardHistory(w http.ResponseWriter, req *http.Request) {
	page, size := parsePagination(req)
	offset := (page - 1) * size

	records, total, err := r.db.BackupRepo.List(size, offset)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list backups: %v", err), http.StatusInternalServerError)
		return
	}

	r.jsonPaginatedResponse(w, records, total, page, size)
}
