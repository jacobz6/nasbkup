package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nas-backup/internal/models"
)

// ──────────────────────────────────────────────────────────────────────────────
// Schedule handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleGetSchedule returns the schedule configuration.
func (r *Router) handleGetSchedule(w http.ResponseWriter, req *http.Request) {
	enabledStr, _ := r.db.ConfigRepo.Get("schedule.enabled")
	cronExpr, _ := r.db.ConfigRepo.Get("schedule.cron_expr")
	timezone, _ := r.db.ConfigRepo.Get("schedule.timezone")

	cfg := &models.ScheduleConfig{
		Enabled:  enabledStr == "true" || enabledStr == "1",
		CronExpr: cronExpr,
		Timezone: timezone,
	}

	// Fallback to app config if DB values are empty.
	if cfg.CronExpr == "" {
		cfg.CronExpr = r.config.Backup.Schedule.CronExpr
		cfg.Enabled = r.config.Backup.Schedule.Enabled
		cfg.Timezone = r.config.Backup.Schedule.Timezone
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// handleUpdateSchedule updates the schedule configuration.
func (r *Router) handleUpdateSchedule(w http.ResponseWriter, req *http.Request) {
	var cfg models.ScheduleConfig
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if cfg.CronExpr == "" {
		r.jsonError(w, "cron_expr is required", http.StatusBadRequest)
		return
	}

	enabledStr := "false"
	if cfg.Enabled {
		enabledStr = "true"
	}

	if err := r.db.ConfigRepo.Set("schedule.enabled", enabledStr); err != nil {
		r.jsonError(w, fmt.Sprintf("save schedule.enabled: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("schedule.cron_expr", cfg.CronExpr); err != nil {
		r.jsonError(w, fmt.Sprintf("save schedule.cron_expr: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("schedule.timezone", cfg.Timezone); err != nil {
		r.jsonError(w, fmt.Sprintf("save schedule.timezone: %v", err), http.StatusInternalServerError)
		return
	}

	// Update the running scheduler: stop if disabling, start if enabling,
	// update cron expression if already running.
	if r.scheduler != nil {
		if cfg.Enabled {
			if r.scheduler.IsEnabled() {
				if err := r.scheduler.UpdateSchedule(cfg.CronExpr); err != nil {
					r.jsonError(w, fmt.Sprintf("update scheduler: %v", err), http.StatusInternalServerError)
					return
				}
			} else {
				if err := r.scheduler.StartWithCron(cfg.CronExpr); err != nil {
					r.jsonError(w, fmt.Sprintf("start scheduler: %v", err), http.StatusInternalServerError)
					return
				}
			}
		} else {
			if r.scheduler.IsEnabled() {
				r.scheduler.Stop()
			}
		}
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Compression handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleGetCompression returns the compression configuration.
func (r *Router) handleGetCompression(w http.ResponseWriter, req *http.Request) {
	enabledStr, _ := r.db.ConfigRepo.Get("compression.enabled")
	algorithm, _ := r.db.ConfigRepo.Get("compression.algorithm")
	levelStr, _ := r.db.ConfigRepo.Get("compression.level")
	skipTypesStr, _ := r.db.ConfigRepo.Get("compression.skip_types")

	cfg := &models.CompressionConfig{
		Enabled:   enabledStr == "true" || enabledStr == "1",
		Algorithm: algorithm,
		SkipTypes: parseStringSlice(skipTypesStr),
	}
	if levelStr != "" {
		cfg.Level, _ = strconv.Atoi(levelStr)
	}

	// Fallback to app config.
	if cfg.Algorithm == "" {
		fallback := r.config.ToModelsCompressionConfig()
		cfg = &fallback
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// handleUpdateCompression updates the compression configuration.
func (r *Router) handleUpdateCompression(w http.ResponseWriter, req *http.Request) {
	var cfg models.CompressionConfig
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if cfg.Algorithm == "" {
		r.jsonError(w, "algorithm is required", http.StatusBadRequest)
		return
	}

	enabledStr := "false"
	if cfg.Enabled {
		enabledStr = "true"
	}

	if err := r.db.ConfigRepo.Set("compression.enabled", enabledStr); err != nil {
		r.jsonError(w, fmt.Sprintf("save compression.enabled: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("compression.algorithm", cfg.Algorithm); err != nil {
		r.jsonError(w, fmt.Sprintf("save compression.algorithm: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("compression.level", strconv.Itoa(cfg.Level)); err != nil {
		r.jsonError(w, fmt.Sprintf("save compression.level: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("compression.skip_types", formatStringSlice(cfg.SkipTypes)); err != nil {
		r.jsonError(w, fmt.Sprintf("save compression.skip_types: %v", err), http.StatusInternalServerError)
		return
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Upload handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleGetUpload returns the upload configuration.
func (r *Router) handleGetUpload(w http.ResponseWriter, req *http.Request) {
	storageClass, _ := r.db.ConfigRepo.Get("upload.storage_class")
	maxConcurrencyStr, _ := r.db.ConfigRepo.Get("upload.max_concurrency")
	chunkSizeStr, _ := r.db.ConfigRepo.Get("upload.chunk_size_mb")
	retryCountStr, _ := r.db.ConfigRepo.Get("upload.retry_count")
	retryDelayStr, _ := r.db.ConfigRepo.Get("upload.retry_delay_sec")

	cfg := &models.UploadConfig{
		StorageClass: storageClass,
	}
	if maxConcurrencyStr != "" {
		cfg.MaxConcurrency, _ = strconv.Atoi(maxConcurrencyStr)
	}
	if chunkSizeStr != "" {
		cfg.ChunkSizeMB, _ = strconv.Atoi(chunkSizeStr)
	}
	if retryCountStr != "" {
		cfg.RetryCount, _ = strconv.Atoi(retryCountStr)
	}
	if retryDelayStr != "" {
		cfg.RetryDelaySec, _ = strconv.Atoi(retryDelayStr)
	}

	// Fallback to app config defaults.
	if cfg.StorageClass == "" {
		fallback := r.config.ToModelsUploadConfig()
		cfg = &fallback
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// handleUpdateUpload updates the upload configuration.
func (r *Router) handleUpdateUpload(w http.ResponseWriter, req *http.Request) {
	var cfg models.UploadConfig
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	kvPairs := map[string]string{
		"upload.storage_class":   cfg.StorageClass,
		"upload.max_concurrency": strconv.Itoa(cfg.MaxConcurrency),
		"upload.chunk_size_mb":   strconv.Itoa(cfg.ChunkSizeMB),
		"upload.retry_count":     strconv.Itoa(cfg.RetryCount),
		"upload.retry_delay_sec": strconv.Itoa(cfg.RetryDelaySec),
	}

	for key, value := range kvPairs {
		if err := r.db.ConfigRepo.Set(key, value); err != nil {
			r.jsonError(w, fmt.Sprintf("save %s: %v", key, err), http.StatusInternalServerError)
			return
		}
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Retention handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleGetRetention returns the retention configuration.
func (r *Router) handleGetRetention(w http.ResponseWriter, req *http.Request) {
	versionKeepStr, _ := r.db.ConfigRepo.Get("retention.version_keep_count")
	orphanGraceStr, _ := r.db.ConfigRepo.Get("retention.orphan_grace_days")
	fullResetStr, _ := r.db.ConfigRepo.Get("retention.full_reset_interval")
	keepDeletedStr, _ := r.db.ConfigRepo.Get("retention.keep_deleted_days")

	cfg := &models.RetentionConfig{}
	if versionKeepStr != "" {
		cfg.VersionKeepCount, _ = strconv.Atoi(versionKeepStr)
	}
	if orphanGraceStr != "" {
		cfg.OrphanGraceDays, _ = strconv.Atoi(orphanGraceStr)
	}
	if fullResetStr != "" {
		cfg.FullResetInterval, _ = strconv.Atoi(fullResetStr)
	}
	if keepDeletedStr != "" {
		cfg.KeepDeletedDays, _ = strconv.Atoi(keepDeletedStr)
	}

	// Fallback to app config defaults.
	if cfg.VersionKeepCount == 0 {
		fallback := r.config.ToModelsRetentionConfig()
		cfg = &fallback
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// handleUpdateRetention updates the retention configuration.
func (r *Router) handleUpdateRetention(w http.ResponseWriter, req *http.Request) {
	var cfg models.RetentionConfig
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	kvPairs := map[string]string{
		"retention.version_keep_count":  strconv.Itoa(cfg.VersionKeepCount),
		"retention.orphan_grace_days":   strconv.Itoa(cfg.OrphanGraceDays),
		"retention.full_reset_interval": strconv.Itoa(cfg.FullResetInterval),
		"retention.keep_deleted_days":   strconv.Itoa(cfg.KeepDeletedDays),
	}

	for key, value := range kvPairs {
		if err := r.db.ConfigRepo.Set(key, value); err != nil {
			r.jsonError(w, fmt.Sprintf("save %s: %v", key, err), http.StatusInternalServerError)
			return
		}
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Encryption handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleGetEncryption returns the encryption configuration.
func (r *Router) handleGetEncryption(w http.ResponseWriter, req *http.Request) {
	algorithm, _ := r.db.ConfigRepo.Get("encryption.algorithm")
	keyFilePath, _ := r.db.ConfigRepo.Get("encryption.key_file_path")

	cfg := &models.EncryptionConfig{
		Algorithm:   algorithm,
		KeyFilePath: keyFilePath,
	}

	// Fallback to app config defaults.
	if cfg.Algorithm == "" {
		fallback := r.config.ToModelsEncryptionConfig()
		cfg = &fallback
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}

// handleUpdateEncryption updates the encryption configuration.
func (r *Router) handleUpdateEncryption(w http.ResponseWriter, req *http.Request) {
	var cfg models.EncryptionConfig
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if cfg.Algorithm == "" {
		r.jsonError(w, "algorithm is required", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.Set("encryption.algorithm", cfg.Algorithm); err != nil {
		r.jsonError(w, fmt.Sprintf("save encryption.algorithm: %v", err), http.StatusInternalServerError)
		return
	}
	if err := r.db.ConfigRepo.Set("encryption.key_file_path", cfg.KeyFilePath); err != nil {
		r.jsonError(w, fmt.Sprintf("save encryption.key_file_path: %v", err), http.StatusInternalServerError)
		return
	}

	r.jsonResponse(w, cfg, http.StatusOK)
}
