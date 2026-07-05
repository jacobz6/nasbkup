// Package models defines all domain types used across the NAS backup system.
// These types serve as the contract between layers — database, business logic,
// and API — ensuring consistent data shapes throughout the application.
package models

import (
	"time"
)

// ---------------------------------------------------------------------------
// File tracking
// ---------------------------------------------------------------------------

// FileStatus represents the current lifecycle state of a tracked file.
type FileStatus string

const (
	FileStatusActive  FileStatus = "active"
	FileStatusDeleted FileStatus = "deleted"
)

// FileRecord represents a single tracked file in the backup index.
type FileRecord struct {
	ID        int64      `json:"id"`
	Path      string     `json:"path"`
	Size      int64      `json:"size"`
	ModTime   time.Time  `json:"mod_time"`
	Hash      string     `json:"hash,omitempty"`
	Status    FileStatus `json:"status"`
	BackupID  int64      `json:"backup_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Backup session
// ---------------------------------------------------------------------------

// BackupType distinguishes full backups from incremental ones.
type BackupType string

const (
	BackupTypeFull         BackupType = "full"
	BackupTypeIncremental  BackupType = "incremental"
	BackupTypeAuto         BackupType = "auto"
)

// BackupStatus represents the current state of a backup session.
type BackupStatus string

const (
	BackupStatusPending    BackupStatus = "pending"
	BackupStatusRunning    BackupStatus = "running"
	BackupStatusCompleted  BackupStatus = "completed"
	BackupStatusFailed     BackupStatus = "failed"
	BackupStatusCancelled  BackupStatus = "cancelled"
)

// BackupRecord represents a single backup session.
type BackupRecord struct {
	ID             int64       `json:"id"`
	Type           BackupType  `json:"type"`
	Status         BackupStatus `json:"status"`
	BaseBackupID   *int64      `json:"base_backup_id,omitempty"`
	TotalFiles     int         `json:"total_files"`
	TotalSize      int64       `json:"total_size"`
	UploadedSize   int64       `json:"uploaded_size"`
	SkippedByDedup int         `json:"skipped_by_dedup"`
	SkippedByInc   int         `json:"skipped_by_incremental"`
	CompressSaved  int64       `json:"compress_saved"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	CompletedAt    *time.Time  `json:"completed_at,omitempty"`
	ErrorMessage   string      `json:"error_message,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Backup-file junction (many-to-many)
// ---------------------------------------------------------------------------

// BackupFileRecord captures the per-file encryption and storage metadata
// produced during a specific backup session.
type BackupFileRecord struct {
	BackupID      int64  `json:"backup_id"`
	FileID        int64  `json:"file_id"`
	StorageKey    string `json:"storage_key"`
	EncryptedIV   string `json:"encrypted_iv"`
	AuthTag       string `json:"auth_tag"`
	CompressType  string `json:"compress_type"`  // "zstd" or "none"
	OriginalSize  int64  `json:"original_size"`
	StoredSize    int64  `json:"stored_size"`
}

// ---------------------------------------------------------------------------
// Global hash index (dedup)
// ---------------------------------------------------------------------------

// HashIndexRecord maps a content hash to its single physical storage location.
// Multiple file paths may reference the same hash (dedup).
type HashIndexRecord struct {
	ID          int64     `json:"id"`
	Hash        string    `json:"hash"`
	FileSize    int64     `json:"file_size"`
	StorageKey  string    `json:"storage_key"`
	RefCount    int       `json:"ref_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Backup logs
// ---------------------------------------------------------------------------

// LogLevel represents severity of a log entry.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogRecord captures a single log event during backup operations.
type LogRecord struct {
	ID        int64     `json:"id"`
	BackupID  *int64    `json:"backup_id,omitempty"`
	Level     LogLevel  `json:"level"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Configuration (key-value store)
// ---------------------------------------------------------------------------

// ConfigRecord stores a single configuration entry.
type ConfigRecord struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// API request/response types
// ---------------------------------------------------------------------------

// --- Dashboard ---

// DashboardStats aggregates the key metrics displayed on the dashboard.
type DashboardStats struct {
	TotalFiles         int64   `json:"total_files"`
	TotalSize          int64   `json:"total_size"`
	BackedUpFiles      int64   `json:"backed_up_files"`
	BackedUpSize       int64   `json:"backed_up_size"`
	OSSStorageUsed     int64   `json:"oss_storage_used"`
	LastBackupTime     *time.Time `json:"last_backup_time,omitempty"`
	LastBackupStatus   BackupStatus `json:"last_backup_status"`
	NextBackupTime     *time.Time `json:"next_backup_time,omitempty"`
	SavedByDedup       int64   `json:"saved_by_dedup"`
	SavedByCompress    int64   `json:"saved_by_compress"`
	ActiveBackupRunning bool   `json:"active_backup_running"`
}

// --- Content selection ---

// BackupDirectory defines a directory to be included in backups.
type BackupDirectory struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Recursive   bool   `json:"recursive"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
}

// ExclusionRule defines a pattern-based rule to exclude files from backup.
type ExclusionRule struct {
	ID       int64  `json:"id"`
	Pattern  string `json:"pattern"`  // glob pattern, e.g. "*.tmp", "node_modules"
	RuleType string `json:"rule_type"` // "extension", "directory", "pattern", "size_exceed"
	Enabled  bool   `json:"enabled"`
}

// FileSizeLimit defines size-based inclusion/exclusion boundaries.
type FileSizeLimit struct {
	MaxFileSize int64 `json:"max_file_size"` // 0 means no limit
	MinFileSize int64 `json:"min_file_size"` // 0 means no limit
}

// ContentConfig holds all content selection settings.
type ContentConfig struct {
	Directories []BackupDirectory `json:"directories"`
	Exclusions  []ExclusionRule   `json:"exclusions"`
	SizeLimit   FileSizeLimit     `json:"size_limit"`
}

// --- Strategy settings ---

// ScheduleConfig defines when backups are triggered.
type ScheduleConfig struct {
	Enabled    bool   `json:"enabled"`
	CronExpr   string `json:"cron_expr"`    // e.g. "0 3 1 * *"
	Timezone   string `json:"timezone"`     // e.g. "Asia/Shanghai"
}

// CompressionConfig defines compression behavior.
type CompressionConfig struct {
	Enabled    bool   `json:"enabled"`
	Algorithm  string `json:"algorithm"`    // "zstd"
	Level      int    `json:"level"`        // 1-22 for zstd
	SkipTypes  []string `json:"skip_types"` // file extensions to skip compression
}

// UploadConfig defines upload behavior.
type UploadConfig struct {
	StorageClass    string `json:"storage_class"`    // "ColdArchive", "Archive"
	MaxConcurrency  int    `json:"max_concurrency"`
	ChunkSizeMB     int    `json:"chunk_size_mb"`
	RetryCount      int    `json:"retry_count"`
	RetryDelaySec   int    `json:"retry_delay_sec"`
}

// RetentionConfig defines how long to keep old data.
type RetentionConfig struct {
	VersionKeepCount   int  `json:"version_keep_count"`    // 1 = latest only
	OrphanGraceDays    int  `json:"orphan_grace_days"`     // days before cleaning orphan data
	FullResetInterval  int  `json:"full_reset_interval"`   // months between full resets
	KeepDeletedDays    int  `json:"keep_deleted_days"`     // days to retain deleted file data
}

// EncryptionConfig defines encryption behavior.
type EncryptionConfig struct {
	Algorithm    string `json:"algorithm"`     // "AES-256-GCM"
	KeyFilePath  string `json:"key_file_path"` // path to master key file
}

// StrategyConfig holds all strategy settings.
type StrategyConfig struct {
	Schedule    ScheduleConfig    `json:"schedule"`
	Compression CompressionConfig `json:"compression"`
	Upload      UploadConfig      `json:"upload"`
	Retention   RetentionConfig   `json:"retention"`
	Encryption  EncryptionConfig  `json:"encryption"`
}

// --- Logs ---

// LogFilter defines query parameters for filtering log entries.
type LogFilter struct {
	BackupID  *int64    `json:"backup_id,omitempty"`
	Level     *LogLevel `json:"level,omitempty"`
	Search    string    `json:"search,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	Page      int       `json:"page"`
	PageSize  int       `json:"page_size"`
}

// LogListResult wraps a page of log entries with pagination metadata.
type LogListResult struct {
	Items    []LogRecord `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

// --- Backup trigger ---

// BackupTriggerRequest is the API request body to manually trigger a backup.
type BackupTriggerRequest struct {
	Type BackupType `json:"type"` // "full" or "incremental"
}

// --- Restore ---

// RestoreRequest specifies what to restore and where.
type RestoreRequest struct {
	Paths      []string `json:"paths"`       // file/directory paths to restore
	Pattern    string   `json:"pattern,omitempty"` // glob pattern for batch restore
	BackupID   *int64   `json:"backup_id,omitempty"` // specific backup to restore from
	OutputDir  string   `json:"output_dir"`   // where to place restored files
	Expedited  bool     `json:"expedited"`    // use expedited OSS thaw
}

// RestoreResult summarizes a restore operation.
type RestoreResult struct {
	TotalFiles    int      `json:"total_files"`
	RestoredFiles int      `json:"restored_files"`
	FailedFiles   []string `json:"failed_files,omitempty"`
	TotalSize     int64    `json:"total_size"`
	ElapsedMs     int64    `json:"elapsed_ms"`
}

// --- File system browsing ---

// FSEntry represents a single file or directory entry in the file browser.
type FSEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time"`
	InBackup  bool   `json:"in_backup"`   // Whether this path is covered by a backup directory (full or partial)
	PartialBackup bool `json:"partial_backup"` // For directories: only some sub-paths are backup targets
	HasUpdate bool   `json:"has_update"`  // Whether the file has been modified since last backup
	WillBackup bool  `json:"will_backup"` // Whether this will be included in next backup
}

// FSBrowseResult is the response for the file system browse API.
type FSBrowseResult struct {
	Path       string     `json:"path"`
	ParentPath string     `json:"parent_path,omitempty"`
	Entries    []FSEntry  `json:"entries"`
}

// --- Backup Progress (SSE) ---

// BackupPhase represents the current phase of a running backup.
type BackupPhase string

const (
	PhaseScanning     BackupPhase = "scanning"
	PhaseHashing      BackupPhase = "hashing"
	PhaseDeduplicating BackupPhase = "deduplicating"
	PhaseUploading    BackupPhase = "uploading"
	PhaseFinalizing   BackupPhase = "finalizing"
	PhaseCompleted    BackupPhase = "completed"
	PhaseFailed       BackupPhase = "failed"
	PhaseCancelled    BackupPhase = "cancelled"
)

// ProgressEvent is sent via SSE to notify clients of backup progress.
type ProgressEvent struct {
	Type      string      `json:"type"`                // "phase", "progress", "log", "file"
	BackupID  int64       `json:"backup_id"`
	Phase     BackupPhase `json:"phase,omitempty"`
	PhaseName string      `json:"phase_name,omitempty"`
	Current   int         `json:"current,omitempty"`   // files processed in current phase
	Total     int         `json:"total,omitempty"`     // total files in current phase
	Percent   float64     `json:"percent,omitempty"`   // 0-100 overall
	Message   string      `json:"message,omitempty"`
	Detail    string      `json:"detail,omitempty"`
	Level     string      `json:"level,omitempty"`     // for log events: info/warn/error
	FilePath  string      `json:"file_path,omitempty"` // for file events
	FileSize  int64       `json:"file_size,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// --- Generic API response ---

// APIResponse is the standard envelope for all API responses.
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// PaginatedResponse wraps a paginated data payload.
type PaginatedResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Total   int64       `json:"total"`
	Page    int         `json:"page"`
	Size    int         `json:"size"`
}
