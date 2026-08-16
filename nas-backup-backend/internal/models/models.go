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

// FileBackupStatus represents the last backup result for a single file.
// An empty string means the file has never been backed up (or status unknown).
type FileBackupStatus string

const (
	FileBackupStatusSuccess FileBackupStatus = "success" // last backup succeeded
	FileBackupStatusFailed  FileBackupStatus = "failed"  // last backup failed with error in last_backup_error
)

// FileRecord represents a single tracked file in the backup index.
type FileRecord struct {
	ID               int64            `json:"id"`
	Path             string           `json:"path"`
	Size             int64            `json:"size"`
	ModTime          time.Time        `json:"mod_time"`
	Hash             string           `json:"hash,omitempty"`
	Status           FileStatus       `json:"status"`
	BackupID         int64            `json:"backup_id,omitempty"`
	LastBackupStatus FileBackupStatus `json:"last_backup_status,omitempty"`
	LastBackupError  string           `json:"last_backup_error,omitempty"`
	LastBackupAt     *time.Time       `json:"last_backup_at,omitempty"`
	LastBackupID     int64            `json:"last_backup_id,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Backup session
// ---------------------------------------------------------------------------

// BackupStatus represents the current state of a backup session.
type BackupStatus string

const (
	BackupStatusPending              BackupStatus = "pending"
	BackupStatusRunning              BackupStatus = "running"
	BackupStatusCompleted            BackupStatus = "completed"
	BackupStatusCompletedWithErrors  BackupStatus = "completed_with_errors" // some files failed, others succeeded
	BackupStatusFailed               BackupStatus = "failed"
	BackupStatusCancelled            BackupStatus = "cancelled"
)

// IsTerminal returns true if the backup status represents a final state.
func (s BackupStatus) IsTerminal() bool {
	switch s {
	case BackupStatusCompleted, BackupStatusCompletedWithErrors, BackupStatusFailed, BackupStatusCancelled:
		return true
	default:
		return false
	}
}

// BackupRecord represents a single backup session.
// There is no full/incremental distinction and no base-backup chain: every
// backup is a standalone session.
type BackupRecord struct {
	ID             int64        `json:"id"`
	Status         BackupStatus `json:"status"`
	TotalFiles     int          `json:"total_files"`
	TotalSize      int64        `json:"total_size"`
	UploadedSize   int64        `json:"uploaded_size"`
	SkippedByDedup int          `json:"skipped_by_dedup"`
	FailedFiles    int          `json:"failed_files"`
	CompressSaved  int64        `json:"compress_saved"`
	StartedAt      *time.Time   `json:"started_at,omitempty"`
	CompletedAt    *time.Time   `json:"completed_at,omitempty"`
	ErrorMessage   string       `json:"error_message,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
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
	ID          int64      `json:"id"`
	Hash        string     `json:"hash"`
	FileSize    int64      `json:"file_size"`
	StorageKey  string     `json:"storage_key"`
	RefCount    int        `json:"ref_count"`
	CreatedAt   time.Time  `json:"created_at"`
	OrphanedAt  *time.Time `json:"orphaned_at,omitempty"`
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

// OSSInfo displays the rclone/OSS configuration details shown on the dashboard.
type OSSInfo struct {
	StorageClass string `json:"storage_class"` // configured storage class (e.g. "ColdArchive", "Archive")
	Endpoint     string `json:"endpoint"`       // OSS endpoint, e.g. "oss-cn-hangzhou.aliyuncs.com"
	Bucket       string `json:"bucket"`         // OSS bucket name
	RemoteName   string `json:"remote_name"`    // rclone remote name, e.g. "oss"
	Region       string `json:"region"`         // OSS region, e.g. "cn-hangzhou"
}

// DashboardStats aggregates the key metrics displayed on the dashboard.
type DashboardStats struct {
	TotalFiles          int64        `json:"total_files"`
	TotalSize           int64        `json:"total_size"`
	OSSStorageUsed      int64        `json:"oss_storage_used"`
	OSSQuotaBytes       int64        `json:"oss_quota_bytes"`
	BackupCount         int64        `json:"backup_count"`
	UniqueHashCount     int64        `json:"unique_hash_count"`
	NeedsReconcile      bool         `json:"needs_reconcile"`
	OSSInfo             OSSInfo      `json:"oss_info"`
	LastBackupTime      *time.Time   `json:"last_backup_time,omitempty"`
	LastBackupStatus    BackupStatus `json:"last_backup_status"`
	NextBackupTime      *time.Time   `json:"next_backup_time,omitempty"`
	ActiveBackupRunning bool         `json:"active_backup_running"`
	EngineReady         bool         `json:"engine_ready"` // true when InitFromOSS completed
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
	OSSQuotaBytes   int64  `json:"oss_quota_bytes"`  // user-defined OSS storage quota in bytes; 0 = unlimited
}

// RetentionConfig defines how long to keep old data.
type RetentionConfig struct {
	OrphanGraceDays  int  `json:"orphan_grace_days"`   // days before cleaning orphan data
	KeepDeletedDays  int  `json:"keep_deleted_days"`   // days to retain deleted file data
	DBBkupKeepCount  int  `json:"db_bkup_keep_count"`  // max number of oss.db .bkup snapshots to retain
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
// The legacy "type" field is ignored: every backup is now a standalone
// session (no full/incremental distinction).
type BackupTriggerRequest struct {
	Type string `json:"type,omitempty"` // accepted for backward compatibility, ignored
}

// --- Restore ---

// RestoreRequest specifies what to restore and where.
type RestoreRequest struct {
	Paths            []string `json:"paths"`                       // file/directory paths to restore
	Pattern          string   `json:"pattern,omitempty"`           // glob pattern for batch restore
	BackupID         *int64   `json:"backup_id,omitempty"`         // specific backup to restore from
	OutputDir        string   `json:"output_dir"`                  // where to place restored files (ignored if RestoreToOriginal is true)
	RestoreToOriginal bool    `json:"restore_to_original"`         // if true, restore each file to its original path
	Expedited        bool     `json:"expedited"`                   // use expedited OSS thaw
	ConflictStrategy string   `json:"conflict_strategy,omitempty"`  // "overwrite" | "skip" | "rename"

	// FallbackBaseDir is set internally (NOT exposed to API callers) when the
	// original backup path cannot be written on the current machine. This
	// happens when a backup created on a Linux machine (e.g. /home/user/...)
	// is being restored on macOS (where /home is an automount that cannot
	// accept user-created subdirs). When set, restoreFile prepends this
	// directory to the original path, producing e.g.
	//   <FallbackBaseDir>/home/user/file.jpg
	// instead of failing with "operation not supported" on mkdir /home/user.
	FallbackBaseDir string `json:"-"`
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
	Name             string           `json:"name"`
	Path             string           `json:"path"`
	IsDir            bool             `json:"is_dir"`
	Size             int64            `json:"size"`
	ModTime          string           `json:"mod_time"`
	InBackup         bool             `json:"in_backup"`   // Whether this path is covered by a backup directory (full or partial)
	PartialBackup    bool             `json:"partial_backup"` // For directories: only some sub-paths are backup targets
	HasUpdate        bool             `json:"has_update"`  // Whether the file has been modified since last backup
	WillBackup       bool             `json:"will_backup"` // Whether this will be included in next backup
	LastBackupStatus FileBackupStatus `json:"last_backup_status,omitempty"` // "success" / "failed" / "" (never backed up)
	LastBackupError  string           `json:"last_backup_error,omitempty"`  // error message if last_backup_status == "failed"
	LastBackupAt     string           `json:"last_backup_at,omitempty"`     // RFC3339 timestamp of last backup attempt
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

// ---------------------------------------------------------------------------
// Restore job tracking
// ---------------------------------------------------------------------------

// RestoreJobStatus represents the lifecycle state of a restore job.
type RestoreJobStatus string

const (
	RestoreJobStatusPending   RestoreJobStatus = "pending"
	RestoreJobStatusRunning   RestoreJobStatus = "running"
	RestoreJobStatusCompleted RestoreJobStatus = "completed"
	RestoreJobStatusFailed    RestoreJobStatus = "failed"
	RestoreJobStatusCancelled RestoreJobStatus = "cancelled"
)

// RestoreJobRecord tracks a single restore operation persisted in the database.
type RestoreJobRecord struct {
	ID               int64             `json:"id"`
	Status           RestoreJobStatus  `json:"status"`
	Paths            []string          `json:"paths,omitempty"`
	Pattern          string            `json:"pattern,omitempty"`
	BackupID         *int64            `json:"backup_id,omitempty"`
	OutputDir        string            `json:"output_dir"`
	Expedited        bool              `json:"expedited"`
	ConflictStrategy string            `json:"conflict_strategy"`
	TotalFiles       int               `json:"total_files"`
	RestoredFiles    int               `json:"restored_files"`
	FailedFiles      []string          `json:"failed_files,omitempty"`
	TotalSize        int64             `json:"total_size"`
	RestoredSize     int64             `json:"restored_size"`
	ElapsedMs        int64             `json:"elapsed_ms,omitempty"`
	ErrorMessage     string            `json:"error_message,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
}

// RestorePhase represents the current phase of a running restore job.
type RestorePhase string

const (
	RestorePhasePreparing   RestorePhase = "preparing"
	RestorePhaseThawing     RestorePhase = "thawing"
	RestorePhaseDownloading RestorePhase = "downloading"
	RestorePhaseDecrypting  RestorePhase = "decrypting"
	RestorePhaseDecompressing RestorePhase = "decompressing"
	RestorePhaseVerifying   RestorePhase = "verifying"
	RestorePhaseMoving      RestorePhase = "moving"
	RestorePhaseCompleted   RestorePhase = "completed"
	RestorePhaseFailed      RestorePhase = "failed"
	RestorePhaseCancelled   RestorePhase = "cancelled"
)

// RestoreProgressEvent is sent via SSE for restore operations.
type RestoreProgressEvent struct {
	Type          string       `json:"type"`                       // "phase", "progress", "file", "log", "connected"
	JobID         int64        `json:"job_id"`
	Phase         RestorePhase `json:"phase,omitempty"`
	PhaseName     string       `json:"phase_name,omitempty"`
	Current       int          `json:"current,omitempty"`
	Total         int          `json:"total,omitempty"`
	Percent       float64      `json:"percent,omitempty"`
	Message       string       `json:"message,omitempty"`
	Detail        string       `json:"detail,omitempty"`
	Level         string       `json:"level,omitempty"`            // for log events
	FilePath      string       `json:"file_path,omitempty"`
	FileSize      int64        `json:"file_size,omitempty"`
	RestoredSize  int64        `json:"restored_size,omitempty"`
	TotalSize     int64        `json:"total_size,omitempty"`
	Timestamp     time.Time    `json:"timestamp"`
}

// FileProgressCallback is invoked by the Restorer after each file is processed
// during a restore. It enables the RestoreJobManager to relay per-file progress
// to the SSE broker without coupling the two components directly.
type FileProgressCallback func(filePath string, fileSize int64, restored bool, err error)

// RestorableFile represents a file that can be restored, enriched with its
// backup metadata (storage key, compression type, backup count, etc.).
// Used by GET /api/restore/files to let the frontend browse what is available.
type RestorableFile struct {
	ID             int64     `json:"id"`
	Path           string    `json:"path"`
	Size           int64     `json:"size"`
	ModTime        time.Time `json:"mod_time"`
	Hash           string    `json:"hash,omitempty"`
	Status         string    `json:"status"`
	BackupCount    int       `json:"backup_count"`
	LatestBackupID int64     `json:"latest_backup_id"`
	LatestBackupAt time.Time `json:"latest_backup_at"`
	StorageKey     string    `json:"storage_key,omitempty"`
	CompressType   string    `json:"compress_type,omitempty"`
	OriginalSize   int64     `json:"original_size"`
	StoredSize     int64     `json:"stored_size"`
}
