package models

import (
	"testing"
	"time"
)

// TestFileStatusConstants 测试 FileStatus 常量值
func TestFileStatusConstants(t *testing.T) {
	if FileStatusActive != "active" {
		t.Errorf("expected FileStatusActive %q, got %q", "active", FileStatusActive)
	}
	if FileStatusDeleted != "deleted" {
		t.Errorf("expected FileStatusDeleted %q, got %q", "deleted", FileStatusDeleted)
	}
}

// TestBackupTypeConstants 测试 BackupType 常量值
func TestBackupTypeConstants(t *testing.T) {
	if BackupTypeFull != "full" {
		t.Errorf("expected BackupTypeFull %q, got %q", "full", BackupTypeFull)
	}
	if BackupTypeIncremental != "incremental" {
		t.Errorf("expected BackupTypeIncremental %q, got %q", "incremental", BackupTypeIncremental)
	}
}

// TestBackupStatusConstants 测试 BackupStatus 常量值
func TestBackupStatusConstants(t *testing.T) {
	if BackupStatusPending != "pending" {
		t.Errorf("expected BackupStatusPending %q, got %q", "pending", BackupStatusPending)
	}
	if BackupStatusRunning != "running" {
		t.Errorf("expected BackupStatusRunning %q, got %q", "running", BackupStatusRunning)
	}
	if BackupStatusCompleted != "completed" {
		t.Errorf("expected BackupStatusCompleted %q, got %q", "completed", BackupStatusCompleted)
	}
	if BackupStatusFailed != "failed" {
		t.Errorf("expected BackupStatusFailed %q, got %q", "failed", BackupStatusFailed)
	}
	if BackupStatusCancelled != "cancelled" {
		t.Errorf("expected BackupStatusCancelled %q, got %q", "cancelled", BackupStatusCancelled)
	}
}

// TestLogLevelConstants 测试 LogLevel 常量值
func TestLogLevelConstants(t *testing.T) {
	if LogLevelDebug != "debug" {
		t.Errorf("expected LogLevelDebug %q, got %q", "debug", LogLevelDebug)
	}
	if LogLevelInfo != "info" {
		t.Errorf("expected LogLevelInfo %q, got %q", "info", LogLevelInfo)
	}
	if LogLevelWarn != "warn" {
		t.Errorf("expected LogLevelWarn %q, got %q", "warn", LogLevelWarn)
	}
	if LogLevelError != "error" {
		t.Errorf("expected LogLevelError %q, got %q", "error", LogLevelError)
	}
}

// TestFileRecord 测试 FileRecord 结构体
func TestFileRecord(t *testing.T) {
	now := time.Now()
	rec := FileRecord{
		ID:        1,
		Path:      "/data/file.txt",
		Size:      1024,
		ModTime:   now,
		Hash:      "abc123",
		Status:    FileStatusActive,
		BackupID:  42,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if rec.ID != 1 {
		t.Errorf("expected ID 1, got %d", rec.ID)
	}
	if rec.Path != "/data/file.txt" {
		t.Errorf("expected Path %q, got %q", "/data/file.txt", rec.Path)
	}
	if rec.Size != 1024 {
		t.Errorf("expected Size 1024, got %d", rec.Size)
	}
	if rec.Hash != "abc123" {
		t.Errorf("expected Hash %q, got %q", "abc123", rec.Hash)
	}
	if rec.Status != FileStatusActive {
		t.Errorf("expected Status Active, got %q", rec.Status)
	}
}

// TestBackupRecord 测试 BackupRecord 结构体
func TestBackupRecord(t *testing.T) {
	now := time.Now()
	rec := BackupRecord{
		ID:             1,
		Type:           BackupTypeFull,
		Status:         BackupStatusCompleted,
		BaseBackupID:   nil,
		TotalFiles:     500,
		TotalSize:      1024 * 1024 * 100,
		UploadedSize:   1024 * 1024 * 80,
		SkippedByDedup: 200,
		SkippedByInc:   100,
		CompressSaved:  1024 * 1024 * 50,
		StartedAt:      &now,
		CompletedAt:    &now,
		ErrorMessage:   "",
		CreatedAt:      now,
	}

	if rec.ID != 1 {
		t.Errorf("expected ID 1, got %d", rec.ID)
	}
	if rec.Type != BackupTypeFull {
		t.Errorf("expected Type Full, got %q", rec.Type)
	}
	if rec.Status != BackupStatusCompleted {
		t.Errorf("expected Status Completed, got %q", rec.Status)
	}
	if rec.TotalFiles != 500 {
		t.Errorf("expected TotalFiles 500, got %d", rec.TotalFiles)
	}
	if rec.SkippedByDedup != 200 {
		t.Errorf("expected SkippedByDedup 200, got %d", rec.SkippedByDedup)
	}
}

// TestBackupFileRecord 测试 BackupFileRecord 结构体
func TestBackupFileRecord(t *testing.T) {
	rec := BackupFileRecord{
		BackupID:     1,
		FileID:       2,
		StorageKey:   "oss/path/encrypted.enc",
		EncryptedIV:  "base64iv",
		AuthTag:      "tag123",
		CompressType: "zstd",
		OriginalSize: 1024,
		StoredSize:   512,
	}

	if rec.BackupID != 1 {
		t.Errorf("expected BackupID 1, got %d", rec.BackupID)
	}
	if rec.FileID != 2 {
		t.Errorf("expected FileID 2, got %d", rec.FileID)
	}
	if rec.StorageKey != "oss/path/encrypted.enc" {
		t.Errorf("expected StorageKey %q, got %q", "oss/path/encrypted.enc", rec.StorageKey)
	}
	if rec.CompressType != "zstd" {
		t.Errorf("expected CompressType %q, got %q", "zstd", rec.CompressType)
	}
}

// TestHashIndexRecord 测试 HashIndexRecord 结构体
func TestHashIndexRecord(t *testing.T) {
	now := time.Now()
	rec := HashIndexRecord{
		ID:         1,
		Hash:       "sha256hash",
		FileSize:   2048,
		StorageKey: "storage/key.enc",
		RefCount:   3,
		CreatedAt:  now,
	}

	if rec.ID != 1 {
		t.Errorf("expected ID 1, got %d", rec.ID)
	}
	if rec.Hash != "sha256hash" {
		t.Errorf("expected Hash %q, got %q", "sha256hash", rec.Hash)
	}
	if rec.FileSize != 2048 {
		t.Errorf("expected FileSize 2048, got %d", rec.FileSize)
	}
	if rec.RefCount != 3 {
		t.Errorf("expected RefCount 3, got %d", rec.RefCount)
	}
}

// TestLogRecord 测试 LogRecord 结构体
func TestLogRecord(t *testing.T) {
	now := time.Now()
	rec := LogRecord{
		ID:        1,
		Level:     LogLevelInfo,
		Message:   "backup started",
		Detail:    "full backup of /data",
		CreatedAt: now,
	}

	if rec.ID != 1 {
		t.Errorf("expected ID 1, got %d", rec.ID)
	}
	if rec.Level != LogLevelInfo {
		t.Errorf("expected Level Info, got %q", rec.Level)
	}
	if rec.Message != "backup started" {
		t.Errorf("expected Message %q, got %q", "backup started", rec.Message)
	}
}

// TestConfigRecord 测试 ConfigRecord 结构体
func TestConfigRecord(t *testing.T) {
	now := time.Now()
	rec := ConfigRecord{
		Key:       "test.key",
		Value:     "test_value",
		UpdatedAt: now,
	}

	if rec.Key != "test.key" {
		t.Errorf("expected Key %q, got %q", "test.key", rec.Key)
	}
	if rec.Value != "test_value" {
		t.Errorf("expected Value %q, got %q", "test_value", rec.Value)
	}
}

// TestDashboardStats 测试 DashboardStats 结构体
func TestDashboardStats(t *testing.T) {
	now := time.Now()
	stats := DashboardStats{
		TotalFiles:          1000,
		TotalSize:           1024 * 1024 * 500,
		OSSStorageUsed:      1024 * 1024 * 300,
		OSSQuotaBytes:       1024 * 1024 * 1024,
		BackupCount:         15,
		UniqueHashCount:     800,
		NeedsReconcile:      false,
		OSSInfo: OSSInfo{
			StorageClass: "ColdArchive",
			Endpoint:     "oss-cn-hangzhou.aliyuncs.com",
			Bucket:       "my-bucket",
			RemoteName:   "oss-crypt",
			Region:       "cn-hangzhou",
		},
		LastBackupTime:      &now,
		LastBackupStatus:    BackupStatusCompleted,
		NextBackupTime:      &now,
		ActiveBackupRunning: false,
	}

	if stats.TotalFiles != 1000 {
		t.Errorf("expected TotalFiles 1000, got %d", stats.TotalFiles)
	}
	if stats.LastBackupStatus != BackupStatusCompleted {
		t.Errorf("expected LastBackupStatus Completed, got %q", stats.LastBackupStatus)
	}
	if stats.ActiveBackupRunning {
		t.Error("expected ActiveBackupRunning to be false")
	}
}

// TestBackupDirectory 测试 BackupDirectory 结构体
func TestBackupDirectory(t *testing.T) {
	dir := BackupDirectory{
		ID:          1,
		Path:        "/data",
		Recursive:   true,
		Enabled:     true,
		Description: "Main data directory",
	}

	if dir.ID != 1 {
		t.Errorf("expected ID 1, got %d", dir.ID)
	}
	if dir.Path != "/data" {
		t.Errorf("expected Path %q, got %q", "/data", dir.Path)
	}
	if !dir.Recursive {
		t.Error("expected Recursive to be true")
	}
	if !dir.Enabled {
		t.Error("expected Enabled to be true")
	}
}

// TestExclusionRule 测试 ExclusionRule 结构体
func TestExclusionRule(t *testing.T) {
	rule := ExclusionRule{
		ID:       1,
		Pattern:  "*.tmp",
		RuleType: "extension",
		Enabled:  true,
	}

	if rule.ID != 1 {
		t.Errorf("expected ID 1, got %d", rule.ID)
	}
	if rule.Pattern != "*.tmp" {
		t.Errorf("expected Pattern %q, got %q", "*.tmp", rule.Pattern)
	}
	if rule.RuleType != "extension" {
		t.Errorf("expected RuleType %q, got %q", "extension", rule.RuleType)
	}
}

// TestFileSizeLimit 测试 FileSizeLimit 结构体
func TestFileSizeLimit(t *testing.T) {
	limit := FileSizeLimit{
		MaxFileSize: 1024 * 1024 * 100,
		MinFileSize: 100,
	}

	if limit.MaxFileSize != 1024*1024*100 {
		t.Errorf("expected MaxFileSize %d, got %d", 1024*1024*100, limit.MaxFileSize)
	}
	if limit.MinFileSize != 100 {
		t.Errorf("expected MinFileSize 100, got %d", limit.MinFileSize)
	}
}

// TestContentConfig 测试 ContentConfig 结构体
func TestContentConfig(t *testing.T) {
	cfg := ContentConfig{
		Directories: []BackupDirectory{
			{Path: "/data", Recursive: true, Enabled: true},
		},
		Exclusions: []ExclusionRule{
			{Pattern: "*.tmp", RuleType: "extension", Enabled: true},
		},
		SizeLimit: FileSizeLimit{
			MaxFileSize: 0,
			MinFileSize: 0,
		},
	}

	if len(cfg.Directories) != 1 {
		t.Errorf("expected 1 directory, got %d", len(cfg.Directories))
	}
	if len(cfg.Exclusions) != 1 {
		t.Errorf("expected 1 exclusion, got %d", len(cfg.Exclusions))
	}
}

// TestScheduleConfig 测试 ScheduleConfig 结构体
func TestScheduleConfig(t *testing.T) {
	cfg := ScheduleConfig{
		Enabled:    true,
		CronExpr:   "0 2 * * *",
		Timezone:   "Asia/Shanghai",
	}

	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg.CronExpr != "0 2 * * *" {
		t.Errorf("expected CronExpr %q, got %q", "0 2 * * *", cfg.CronExpr)
	}
	if cfg.Timezone != "Asia/Shanghai" {
		t.Errorf("expected Timezone %q, got %q", "Asia/Shanghai", cfg.Timezone)
	}
}

// TestCompressionConfig 测试 CompressionConfig 结构体
func TestCompressionConfig(t *testing.T) {
	cfg := CompressionConfig{
		Enabled:   true,
		Algorithm: "zstd",
		Level:     19,
		SkipTypes: []string{".mp4", ".zip"},
	}

	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if cfg.Algorithm != "zstd" {
		t.Errorf("expected Algorithm %q, got %q", "zstd", cfg.Algorithm)
	}
	if cfg.Level != 19 {
		t.Errorf("expected Level 19, got %d", cfg.Level)
	}
	if len(cfg.SkipTypes) != 2 {
		t.Errorf("expected 2 SkipTypes, got %d", len(cfg.SkipTypes))
	}
}

// TestUploadConfig 测试 UploadConfig 结构体
func TestUploadConfig(t *testing.T) {
	cfg := UploadConfig{
		StorageClass:   "ColdArchive",
		MaxConcurrency: 4,
		ChunkSizeMB:    64,
		RetryCount:     3,
		RetryDelaySec:  5,
	}

	if cfg.StorageClass != "ColdArchive" {
		t.Errorf("expected StorageClass %q, got %q", "ColdArchive", cfg.StorageClass)
	}
	if cfg.MaxConcurrency != 4 {
		t.Errorf("expected MaxConcurrency 4, got %d", cfg.MaxConcurrency)
	}
}

// TestRetentionConfig 测试 RetentionConfig 结构体
func TestRetentionConfig(t *testing.T) {
	cfg := RetentionConfig{
		VersionKeepCount:  3,
		OrphanGraceDays:   180,
		FullResetInterval: 6,
		KeepDeletedDays:   90,
	}

	if cfg.VersionKeepCount != 3 {
		t.Errorf("expected VersionKeepCount 3, got %d", cfg.VersionKeepCount)
	}
	if cfg.OrphanGraceDays != 180 {
		t.Errorf("expected OrphanGraceDays 180, got %d", cfg.OrphanGraceDays)
	}
	if cfg.FullResetInterval != 6 {
		t.Errorf("expected FullResetInterval 6, got %d", cfg.FullResetInterval)
	}
}

// TestEncryptionConfig 测试 EncryptionConfig 结构体
func TestEncryptionConfig(t *testing.T) {
	cfg := EncryptionConfig{
		Algorithm:   "AES-256-GCM",
		KeyFilePath: "./data/master.key",
	}

	if cfg.Algorithm != "AES-256-GCM" {
		t.Errorf("expected Algorithm %q, got %q", "AES-256-GCM", cfg.Algorithm)
	}
	if cfg.KeyFilePath != "./data/master.key" {
		t.Errorf("expected KeyFilePath %q, got %q", "./data/master.key", cfg.KeyFilePath)
	}
}

// TestStrategyConfig 测试 StrategyConfig 结构体
func TestStrategyConfig(t *testing.T) {
	cfg := StrategyConfig{
		Schedule: ScheduleConfig{
			Enabled:  true,
			CronExpr: "0 2 * * *",
		},
		Compression: CompressionConfig{
			Enabled: true,
		},
		Upload: UploadConfig{
			StorageClass: "ColdArchive",
		},
		Retention: RetentionConfig{
			VersionKeepCount: 1,
		},
		Encryption: EncryptionConfig{
			Algorithm: "AES-256-GCM",
		},
	}

	if !cfg.Schedule.Enabled {
		t.Error("expected Schedule.Enabled to be true")
	}
	if !cfg.Compression.Enabled {
		t.Error("expected Compression.Enabled to be true")
	}
}

// TestLogFilter 测试 LogFilter 结构体
func TestLogFilter(t *testing.T) {
	level := LogLevelWarn
	filter := LogFilter{
		Level:    &level,
		Search:   "error",
		Page:     1,
		PageSize: 20,
	}

	if *filter.Level != LogLevelWarn {
		t.Errorf("expected Level Warn, got %q", *filter.Level)
	}
	if filter.Search != "error" {
		t.Errorf("expected Search %q, got %q", "error", filter.Search)
	}
	if filter.Page != 1 {
		t.Errorf("expected Page 1, got %d", filter.Page)
	}
}

// TestLogListResult 测试 LogListResult 结构体
func TestLogListResult(t *testing.T) {
	result := LogListResult{
		Items:    []LogRecord{{ID: 1, Message: "test"}},
		Total:    100,
		Page:     1,
		PageSize: 20,
	}

	if len(result.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(result.Items))
	}
	if result.Total != 100 {
		t.Errorf("expected Total 100, got %d", result.Total)
	}
}

// TestBackupTriggerRequest 测试 BackupTriggerRequest 结构体
func TestBackupTriggerRequest(t *testing.T) {
	req := BackupTriggerRequest{
		Type: BackupTypeFull,
	}

	if req.Type != BackupTypeFull {
		t.Errorf("expected Type Full, got %q", req.Type)
	}
}

// TestRestoreRequest 测试 RestoreRequest 结构体
func TestRestoreRequest(t *testing.T) {
	backupID := int64(42)
	req := RestoreRequest{
		Paths:     []string{"/data/file1.txt", "/data/dir/"},
		Pattern:   "*.conf",
		BackupID:  &backupID,
		OutputDir: "/restore",
		Expedited: true,
	}

	if len(req.Paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(req.Paths))
	}
	if req.Pattern != "*.conf" {
		t.Errorf("expected Pattern %q, got %q", "*.conf", req.Pattern)
	}
	if *req.BackupID != 42 {
		t.Errorf("expected BackupID 42, got %d", *req.BackupID)
	}
	if !req.Expedited {
		t.Error("expected Expedited to be true")
	}
}

// TestRestoreResult 测试 RestoreResult 结构体
func TestRestoreResult(t *testing.T) {
	result := RestoreResult{
		TotalFiles:    100,
		RestoredFiles: 95,
		FailedFiles:   []string{"/data/err1.txt", "/data/err2.txt"},
		TotalSize:     1024 * 1024 * 50,
		ElapsedMs:     30000,
	}

	if result.TotalFiles != 100 {
		t.Errorf("expected TotalFiles 100, got %d", result.TotalFiles)
	}
	if result.RestoredFiles != 95 {
		t.Errorf("expected RestoredFiles 95, got %d", result.RestoredFiles)
	}
	if len(result.FailedFiles) != 2 {
		t.Errorf("expected 2 failed files, got %d", len(result.FailedFiles))
	}
}

// TestFSEntry 测试 FSEntry 结构体
func TestFSEntry(t *testing.T) {
	entry := FSEntry{
		Name:       "file.txt",
		Path:       "/data/file.txt",
		IsDir:      false,
		Size:       1024,
		ModTime:    "2024-01-01T00:00:00Z",
		InBackup:   true,
		HasUpdate:  false,
		WillBackup: true,
	}

	if entry.Name != "file.txt" {
		t.Errorf("expected Name %q, got %q", "file.txt", entry.Name)
	}
	if !entry.InBackup {
		t.Error("expected InBackup to be true")
	}
	if entry.HasUpdate {
		t.Error("expected HasUpdate to be false")
	}
}

// TestFSBrowseResult 测试 FSBrowseResult 结构体
func TestFSBrowseResult(t *testing.T) {
	result := FSBrowseResult{
		Path:       "/data",
		ParentPath: "/",
		Entries: []FSEntry{
			{Name: "file.txt", IsDir: false},
		},
	}

	if result.Path != "/data" {
		t.Errorf("expected Path %q, got %q", "/data", result.Path)
	}
	if len(result.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(result.Entries))
	}
}

// TestAPIResponse 测试 APIResponse 结构体
func TestAPIResponse(t *testing.T) {
	resp := APIResponse{
		Success: true,
		Data:    map[string]int{"count": 10},
	}

	if !resp.Success {
		t.Error("expected Success to be true")
	}
	if resp.Error != "" {
		t.Errorf("expected empty Error, got %q", resp.Error)
	}
}

// TestPaginatedResponse 测试 PaginatedResponse 结构体
func TestPaginatedResponse(t *testing.T) {
	resp := PaginatedResponse{
		Success: true,
		Total:   100,
		Page:    1,
		Size:    20,
	}

	if !resp.Success {
		t.Error("expected Success to be true")
	}
	if resp.Total != 100 {
		t.Errorf("expected Total 100, got %d", resp.Total)
	}
}
