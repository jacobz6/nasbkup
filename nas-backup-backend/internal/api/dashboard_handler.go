package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nas-backup/internal/models"
)

// handleDashboardStats returns dashboard statistics.
func (r *Router) handleDashboardStats(w http.ResponseWriter, req *http.Request) {
	activeFiles, _ := r.db.FileRepo.CountByStatus(models.FileStatusActive)
	activeSize, _ := r.db.FileRepo.TotalSizeByStatus(models.FileStatusActive)
	ossUsed, _ := r.db.HashRepo.OSSStorageUsed()
	uniqueHashes, _ := r.db.HashRepo.CountActiveHashes()
	backupCount, _ := r.db.BackupRepo.CountByStatus(models.BackupStatusCompleted)
	latestBackup, _ := r.db.BackupRepo.GetLatestCompleted()
	dbRunning, _ := r.db.BackupRepo.IsRunning()
	_, memRunning := r.engine.RunningBackupID()
	isRunning := dbRunning || memRunning

	// OSS quota is stored in config_kv (set via the Strategy page upload config).
	ossQuotaBytes := int64(0)
	if quotaStr, _ := r.db.ConfigRepo.Get("upload.oss_quota_bytes"); quotaStr != "" {
		if v, err := strconv.ParseInt(quotaStr, 10, 64); err == nil {
			ossQuotaBytes = v
		}
	}
	// Storage class is also stored in config_kv; fall back to the YAML config.
	storageClass, _ := r.db.ConfigRepo.Get("upload.storage_class")
	if storageClass == "" {
		storageClass = r.config.OSS.StorageClass
	}

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
		OSSStorageUsed:      ossUsed,
		OSSQuotaBytes:       ossQuotaBytes,
		BackupCount:         backupCount,
		UniqueHashCount:     uniqueHashes,
		NeedsReconcile:      r.engine.NeedsReconcile(),
		OSSInfo: models.OSSInfo{
			StorageClass: storageClass,
			Endpoint:     r.config.OSS.Endpoint,
			Bucket:       r.config.OSS.Bucket,
			RemoteName:   r.config.Rclone.RemoteName,
			Region:       r.config.OSS.Region,
		},
		LastBackupTime:      lastBackupTime,
		LastBackupStatus:    lastBackupStatus,
		NextBackupTime:      nextBackupTime,
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
