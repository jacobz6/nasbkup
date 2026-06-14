package api

import (
        "context"
        "database/sql"
        "encoding/json"
        "fmt"
        "net/http"
        "os"
        "path/filepath"
        "sort"
        "strconv"
        "strings"
        "time"

        "github.com/nas-backup/internal/models"
)

// ──────────────────────────────────────────────────────────────────────────────
// Dashboard handlers
// ──────────────────────────────────────────────────────────────────────────────

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

func (r *Router) handleDashboardHistory(w http.ResponseWriter, req *http.Request) {
        page, _ := strconv.Atoi(req.URL.Query().Get("page"))
        size, _ := strconv.Atoi(req.URL.Query().Get("size"))
        if page < 1 {
                page = 1
        }
        if size < 1 {
                size = 20
        }
        offset := (page - 1) * size

        records, err := r.db.BackupRepo.List(size, offset)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("list backups: %v", err), http.StatusInternalServerError)
                return
        }

        r.jsonPaginatedResponse(w, records, int64(len(records)), page, size)
}

// ──────────────────────────────────────────────────────────────────────────────
// Backup handlers
// ──────────────────────────────────────────────────────────────────────────────

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

// ──────────────────────────────────────────────────────────────────────────────
// File system browsing
// ──────────────────────────────────────────────────────────────────────────────

func (r *Router) handleFSBrowse(w http.ResponseWriter, req *http.Request) {
        path := req.URL.Query().Get("path")
        if path == "" {
                path = "/"
        }

        // Clean the path to prevent directory traversal.
        path = filepath.Clean(path)

        // Read the directory.
        entries, err := os.ReadDir(path)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("read directory %q: %v", path, err), http.StatusBadRequest)
                return
        }

        // Get backup directories and exclusion rules for status computation.
        backupDirs, _ := r.db.ConfigRepo.ListDirectories()
        exclusionRules, _ := r.db.ConfigRepo.GetEnabledExclusionRules()

        // Build a set of backup directory paths for quick lookup.
        backupPathSet := make(map[string]bool)
        for _, dir := range backupDirs {
                if dir.Enabled {
                        backupPathSet[dir.Path] = true
                }
        }

        result := &models.FSBrowseResult{
                Path:    path,
                Entries: make([]models.FSEntry, 0, len(entries)),
        }

        // Compute parent path.
        if path != "/" {
                result.ParentPath = filepath.Dir(path)
        }

        for _, entry := range entries {
                fullPath := filepath.Join(path, entry.Name())
                info, err := entry.Info()
                if err != nil {
                        continue
                }

                fsEntry := models.FSEntry{
                        Name:    entry.Name(),
                        Path:    fullPath,
                        IsDir:   entry.IsDir(),
                        Size:    info.Size(),
                        ModTime: info.ModTime().Format(time.RFC3339),
                }

                // Determine backup status.
                fsEntry.InBackup = r.isPathInBackup(fullPath, entry.IsDir(), backupDirs)
                fsEntry.WillBackup = r.willPathBeBackedUp(fullPath, entry.IsDir(), backupDirs, exclusionRules)

                // For files, check if there's an update.
                if !entry.IsDir() {
                        fsEntry.HasUpdate = r.fileHasUpdate(fullPath, info)
                }

                result.Entries = append(result.Entries, fsEntry)
        }

        // Sort: directories first, then files, both alphabetically.
        sort.SliceStable(result.Entries, func(i, j int) bool {
                if result.Entries[i].IsDir != result.Entries[j].IsDir {
                        return result.Entries[i].IsDir
                }
                return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
        })

        r.jsonResponse(w, result, http.StatusOK)
}

// isPathInBackup checks if a path is covered by any enabled backup directory.
func (r *Router) isPathInBackup(path string, isDir bool, backupDirs []*models.BackupDirectory) bool {
        for _, dir := range backupDirs {
                if !dir.Enabled {
                        continue
                }
                // Exact match.
                if path == dir.Path {
                        return true
                }
                // Path is under a backup directory.
                if strings.HasPrefix(path, dir.Path+"/") {
                        return true
                }
                // For directories, check if a backup directory is under this path.
                if isDir && strings.HasPrefix(dir.Path, path+"/") {
                        return true
                }
        }
        return false
}

// willPathBeBackedUp checks if a path will actually be included in the next backup,
// considering both backup directories and exclusion rules.
func (r *Router) willPathBeBackedUp(path string, isDir bool, backupDirs []*models.BackupDirectory, exclusions []*models.ExclusionRule) bool {
        // First, check if the path is under any enabled backup directory.
        covered := false
        for _, dir := range backupDirs {
                if !dir.Enabled {
                        continue
                }
                if path == dir.Path || strings.HasPrefix(path, dir.Path+"/") {
                        covered = true
                        break
                }
        }
        if !covered {
                return false
        }

        // For directories, if covered, they will be backed up (unless excluded).
        if isDir {
                return !r.isPathExcluded(path, exclusions)
        }

        // For files, check exclusion rules.
        return !r.isPathExcluded(path, exclusions)
}

// isPathExcluded checks if a path matches any enabled exclusion rule.
func (r *Router) isPathExcluded(path string, exclusions []*models.ExclusionRule) bool {
        for _, rule := range exclusions {
                if !rule.Enabled {
                        continue
                }
                switch rule.RuleType {
                case "extension":
                        pat := strings.TrimPrefix(rule.Pattern, "*.")
                        pat = strings.TrimPrefix(pat, ".")
                        if pat == "" {
                                continue
                        }
                        ext := strings.TrimPrefix(filepath.Ext(path), ".")
                        if strings.EqualFold(ext, pat) {
                                return true
                        }
                case "directory":
                        for _, component := range strings.Split(filepath.ToSlash(path), "/") {
                                matched, _ := filepath.Match(rule.Pattern, component)
                                if matched {
                                        return true
                                }
                        }
                case "pattern":
                        matched, err := filepath.Match(rule.Pattern, filepath.Base(path))
                        if err == nil && matched {
                                return true
                        }
                }
        }
        return false
}

// fileHasUpdate checks if a file has been modified since the last backup.
func (r *Router) fileHasUpdate(path string, info os.FileInfo) bool {
        rec, err := r.db.FileRepo.GetByPath(path)
        if err != nil || rec == nil {
                // File not in DB — it's new, so it has an "update".
                return true
        }
        // File exists in DB but mod time or size differs.
        return !info.ModTime().Equal(rec.ModTime) || info.Size() != rec.Size
}

// ──────────────────────────────────────────────────────────────────────────────
// Content — Directory handlers
// ──────────────────────────────────────────────────────────────────────────────

func (r *Router) handleListDirectories(w http.ResponseWriter, req *http.Request) {
        dirs, err := r.db.ConfigRepo.ListDirectories()
        if err != nil {
                r.jsonError(w, fmt.Sprintf("list directories: %v", err), http.StatusInternalServerError)
                return
        }
        r.jsonResponse(w, dirs, http.StatusOK)
}

func (r *Router) handleAddDirectory(w http.ResponseWriter, req *http.Request) {
        var dir models.BackupDirectory
        if err := json.NewDecoder(req.Body).Decode(&dir); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }
        if dir.Path == "" {
                r.jsonError(w, "path is required", http.StatusBadRequest)
                return
        }

        id, err := r.db.ConfigRepo.AddDirectory(dir.Path, dir.Recursive, dir.Enabled, dir.Description)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("add directory: %v", err), http.StatusInternalServerError)
                return
        }
        dir.ID = id
        r.jsonResponse(w, dir, http.StatusCreated)
}

func (r *Router) handleUpdateDirectory(w http.ResponseWriter, req *http.Request) {
        id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
        if err != nil {
                r.jsonError(w, "invalid directory ID", http.StatusBadRequest)
                return
        }

        var dir models.BackupDirectory
        if err := json.NewDecoder(req.Body).Decode(&dir); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }
        if dir.Path == "" {
                r.jsonError(w, "path is required", http.StatusBadRequest)
                return
        }

        if err := r.db.ConfigRepo.UpdateDirectory(id, dir.Path, dir.Recursive, dir.Enabled, dir.Description); err != nil {
                r.jsonError(w, fmt.Sprintf("update directory: %v", err), http.StatusInternalServerError)
                return
        }
        dir.ID = id
        r.jsonResponse(w, dir, http.StatusOK)
}

func (r *Router) handleDeleteDirectory(w http.ResponseWriter, req *http.Request) {
        id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
        if err != nil {
                r.jsonError(w, "invalid directory ID", http.StatusBadRequest)
                return
        }

        if err := r.db.ConfigRepo.DeleteDirectory(id); err != nil {
                r.jsonError(w, fmt.Sprintf("delete directory: %v", err), http.StatusNotFound)
                return
        }
        r.jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Content — Exclusion handlers
// ──────────────────────────────────────────────────────────────────────────────

func (r *Router) handleListExclusions(w http.ResponseWriter, req *http.Request) {
        rules, err := r.db.ConfigRepo.ListExclusionRules()
        if err != nil {
                r.jsonError(w, fmt.Sprintf("list exclusions: %v", err), http.StatusInternalServerError)
                return
        }
        r.jsonResponse(w, rules, http.StatusOK)
}

func (r *Router) handleAddExclusion(w http.ResponseWriter, req *http.Request) {
        var rule models.ExclusionRule
        if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }
        if rule.Pattern == "" {
                r.jsonError(w, "pattern is required", http.StatusBadRequest)
                return
        }
        if rule.RuleType == "" {
                rule.RuleType = "pattern"
        }

        id, err := r.db.ConfigRepo.AddExclusionRule(rule.Pattern, rule.RuleType, rule.Enabled)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("add exclusion rule: %v", err), http.StatusInternalServerError)
                return
        }
        rule.ID = id
        r.jsonResponse(w, rule, http.StatusCreated)
}

func (r *Router) handleUpdateExclusion(w http.ResponseWriter, req *http.Request) {
        id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
        if err != nil {
                r.jsonError(w, "invalid exclusion ID", http.StatusBadRequest)
                return
        }

        var rule models.ExclusionRule
        if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }
        if rule.Pattern == "" {
                r.jsonError(w, "pattern is required", http.StatusBadRequest)
                return
        }

        if err := r.db.ConfigRepo.UpdateExclusionRule(id, rule.Pattern, rule.RuleType, rule.Enabled); err != nil {
                r.jsonError(w, fmt.Sprintf("update exclusion rule: %v", err), http.StatusInternalServerError)
                return
        }
        rule.ID = id
        r.jsonResponse(w, rule, http.StatusOK)
}

func (r *Router) handleDeleteExclusion(w http.ResponseWriter, req *http.Request) {
        id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
        if err != nil {
                r.jsonError(w, "invalid exclusion ID", http.StatusBadRequest)
                return
        }

        if err := r.db.ConfigRepo.DeleteExclusionRule(id); err != nil {
                r.jsonError(w, fmt.Sprintf("delete exclusion rule: %v", err), http.StatusNotFound)
                return
        }
        r.jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Strategy — Schedule handlers
// ──────────────────────────────────────────────────────────────────────────────

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

        // Update the running scheduler if applicable.
        if r.scheduler != nil && r.scheduler.IsEnabled() {
                if err := r.scheduler.UpdateSchedule(cfg.CronExpr); err != nil {
                        r.jsonError(w, fmt.Sprintf("update scheduler: %v", err), http.StatusInternalServerError)
                        return
                }
        }

        r.jsonResponse(w, cfg, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Strategy — Compression handlers
// ──────────────────────────────────────────────────────────────────────────────

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
// Strategy — Upload handlers
// ──────────────────────────────────────────────────────────────────────────────

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

func (r *Router) handleUpdateUpload(w http.ResponseWriter, req *http.Request) {
        var cfg models.UploadConfig
        if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }

        kvPairs := map[string]string{
                "upload.storage_class":    cfg.StorageClass,
                "upload.max_concurrency":  strconv.Itoa(cfg.MaxConcurrency),
                "upload.chunk_size_mb":    strconv.Itoa(cfg.ChunkSizeMB),
                "upload.retry_count":      strconv.Itoa(cfg.RetryCount),
                "upload.retry_delay_sec":  strconv.Itoa(cfg.RetryDelaySec),
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
// Strategy — Retention handlers
// ──────────────────────────────────────────────────────────────────────────────

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

func (r *Router) handleUpdateRetention(w http.ResponseWriter, req *http.Request) {
        var cfg models.RetentionConfig
        if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
                r.jsonError(w, "invalid request body", http.StatusBadRequest)
                return
        }

        kvPairs := map[string]string{
                "retention.version_keep_count":   strconv.Itoa(cfg.VersionKeepCount),
                "retention.orphan_grace_days":    strconv.Itoa(cfg.OrphanGraceDays),
                "retention.full_reset_interval":  strconv.Itoa(cfg.FullResetInterval),
                "retention.keep_deleted_days":    strconv.Itoa(cfg.KeepDeletedDays),
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
// Strategy — Encryption handlers
// ──────────────────────────────────────────────────────────────────────────────

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

// ──────────────────────────────────────────────────────────────────────────────
// Log handlers
// ──────────────────────────────────────────────────────────────────────────────

func (r *Router) handleListLogs(w http.ResponseWriter, req *http.Request) {
        q := req.URL.Query()

        filter := &models.LogFilter{
                Page:     1,
                PageSize: 50,
        }

        if v := q.Get("backup_id"); v != "" {
                if id, err := strconv.ParseInt(v, 10, 64); err == nil {
                        filter.BackupID = &id
                }
        }
        if v := q.Get("level"); v != "" {
                level := models.LogLevel(v)
                filter.Level = &level
        }
        if v := q.Get("search"); v != "" {
                filter.Search = v
        }
        if v := q.Get("start_time"); v != "" {
                if t, err := time.Parse(time.RFC3339, v); err == nil {
                        filter.StartTime = &t
                }
        }
        if v := q.Get("end_time"); v != "" {
                if t, err := time.Parse(time.RFC3339, v); err == nil {
                        filter.EndTime = &t
                }
        }
        if v := q.Get("page"); v != "" {
                if p, err := strconv.Atoi(v); err == nil && p > 0 {
                        filter.Page = p
                }
        }
        if v := q.Get("page_size"); v != "" {
                if s, err := strconv.Atoi(v); err == nil && s > 0 {
                        filter.PageSize = s
                }
        }

        records, total, err := r.db.LogRepo.List(filter)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("list logs: %v", err), http.StatusInternalServerError)
                return
        }

        r.jsonPaginatedResponse(w, records, total, filter.Page, filter.PageSize)
}

func (r *Router) handleGetLog(w http.ResponseWriter, req *http.Request) {
        id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
        if err != nil {
                r.jsonError(w, "invalid log ID", http.StatusBadRequest)
                return
        }

        // Fetch the log entry by ID using a direct query.
        // A proper GetByID method should be added to LogRepository;
        // this implementation queries the database directly as a workaround.
        var rec models.LogRecord
        var backupID sql.NullInt64
        var createdAt string

        row := r.db.DB().QueryRow(`
                SELECT id, backup_id, level, message, detail, created_at
                FROM backup_logs WHERE id = ?`, id)
        if err := row.Scan(&rec.ID, &backupID, &rec.Level, &rec.Message, &rec.Detail, &createdAt); err != nil {
                if err == sql.ErrNoRows {
                        r.jsonError(w, "log entry not found", http.StatusNotFound)
                } else {
                        r.jsonError(w, fmt.Sprintf("query log: %v", err), http.StatusInternalServerError)
                }
                return
        }

        if backupID.Valid {
                rec.BackupID = &backupID.Int64
        }
        t, parseErr := time.Parse(time.RFC3339, createdAt)
        if parseErr != nil {
                // Try alternative SQLite datetime formats before failing.
                altFormats := []string{
                        "2006-01-02 15:04:05",
                        "2006-01-02 15:04:05.999999999",
                        "2006-01-02 15:04:05Z07:00",
                        "2006-01-02T15:04:05",
                }
                parsed := false
                for _, f := range altFormats {
                        if alt, altErr := time.Parse(f, createdAt); altErr == nil {
                                t = alt
                                parsed = true
                                break
                        }
                }
                if !parsed {
                        // Fall back to current time instead of returning an error.
                        t = time.Now().UTC()
                }
        }
        rec.CreatedAt = t

        r.jsonResponse(w, rec, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Restore & Garbage Collection handlers
// ──────────────────────────────────────────────────────────────────────────────

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

        result, err := r.restorer.Restore(context.Background(), &restoreReq)
        if err != nil {
                r.jsonError(w, fmt.Sprintf("restore failed: %v", err), http.StatusInternalServerError)
                return
        }

        r.jsonResponse(w, result, http.StatusOK)
}

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

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// parseStringSlice parses a comma-separated string into a slice.
func parseStringSlice(s string) []string {
        if s == "" {
                return nil
        }
        parts := strings.Split(s, ",")
        result := make([]string, 0, len(parts))
        for _, p := range parts {
                p = strings.TrimSpace(p)
                if p != "" {
                        result = append(result, p)
                }
        }
        return result
}

// formatStringSlice formats a string slice as a comma-separated string.
func formatStringSlice(parts []string) string {
        return strings.Join(parts, ",")
}
