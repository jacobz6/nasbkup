// Package config handles loading, validating, and providing access to all
// application configuration. Configuration is loaded from a YAML file and
// can be overridden by environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nas-backup/internal/models"
	"gopkg.in/yaml.v3"
)

// AppConfig is the top-level application configuration.
type AppConfig struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Backup   BackupConfig   `yaml:"backup"`
	OSS      OSSConfig      `yaml:"oss"`
	Rclone   RcloneConfig   `yaml:"rclone"`
	Logging  LoggingConfig  `yaml:"logging"`
	Reconcile ReconcileConfig `yaml:"reconcile"`
}

// ServerConfig defines the HTTP server parameters.
type ServerConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	ReadTimeout  int    `yaml:"read_timeout_sec"`
	WriteTimeout int    `yaml:"write_timeout_sec"`
}

// DatabaseConfig defines the SQLite database parameters.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// BackupConfig collects all backup-related settings.
type BackupConfig struct {
	Directories []DirectoryConfig `yaml:"directories"`
	Exclusions  []ExclusionConfig `yaml:"exclusions"`
	SizeLimit   SizeLimitConfig   `yaml:"size_limit"`
	Schedule    ScheduleConfig    `yaml:"schedule"`
	Compression CompressionConfig `yaml:"compression"`
	Retention   RetentionConfig   `yaml:"retention"`
	Encryption  EncryptionConfig  `yaml:"encryption"`
}

// DirectoryConfig defines a single directory to back up.
type DirectoryConfig struct {
	Path        string `yaml:"path"`
	Recursive   bool   `yaml:"recursive"`
	Enabled     bool   `yaml:"enabled"`
	Description string `yaml:"description"`
}

// ExclusionConfig defines a single exclusion rule.
type ExclusionConfig struct {
	Pattern  string `yaml:"pattern"`
	RuleType string `yaml:"rule_type"`
	Enabled  bool   `yaml:"enabled"`
}

// SizeLimitConfig defines size boundaries for file inclusion.
type SizeLimitConfig struct {
	MaxFileSize int64 `yaml:"max_file_size"`
	MinFileSize int64 `yaml:"min_file_size"`
}

// ScheduleConfig defines when automatic backups are triggered.
type ScheduleConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CronExpr string `yaml:"cron_expr"`
	Timezone string `yaml:"timezone"`
}

// CompressionConfig defines compression behavior.
type CompressionConfig struct {
	Enabled   bool     `yaml:"enabled"`
	Algorithm string   `yaml:"algorithm"`
	Level     int      `yaml:"level"`
	SkipTypes []string `yaml:"skip_types"`
}

// RetentionConfig defines data retention and cleanup policies.
type RetentionConfig struct {
	VersionKeepCount  int `yaml:"version_keep_count"`
	OrphanGraceDays   int `yaml:"orphan_grace_days"`
	FullResetInterval int `yaml:"full_reset_interval_months"`
	KeepDeletedDays   int `yaml:"keep_deleted_days"`
}

// EncryptionConfig defines encryption parameters.
type EncryptionConfig struct {
	Algorithm   string `yaml:"algorithm"`
	KeyFilePath string `yaml:"key_file_path"`
}

// ReconcileConfig controls the system sync / reconciliation feature that
// keeps OSS objects, the hash_index, backup_files and backups status
// consistent after crashes or partial failures.
type ReconcileConfig struct {
	// DryRun controls the default behavior of the reconcile API. When true,
	// inconsistencies are detected and reported but NOT fixed. The API
	// accepts a ?dry_run=false query to override per-call.
	DryRun bool `yaml:"dry_run"`
	// OSSListPrefix is the OSS key prefix under which backup blobs are stored.
	// All objects under this prefix are listed and compared to DB storage_keys.
	OSSListPrefix string `yaml:"oss_list_prefix"`
}

// OSSConfig defines Alibaba Cloud OSS parameters.
type OSSConfig struct {
	Endpoint        string `yaml:"endpoint"`
	Bucket          string `yaml:"bucket"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	StorageClass    string `yaml:"storage_class"`
	Region          string `yaml:"region"`
}

// RcloneConfig defines rclone-specific parameters.
type RcloneConfig struct {
	BinaryPath string `yaml:"binary_path"`
	ConfigPath string `yaml:"config_path"`
	RemoteName string `yaml:"remote_name"`
}

// LoggingConfig defines logging behavior.
type LoggingConfig struct {
	Level    string `yaml:"level"`
	FilePath string `yaml:"file_path"`
	MaxSize  int    `yaml:"max_size_mb"`
	MaxFiles int    `yaml:"max_files"`
}

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			Host:         "0.0.0.0",
			Port:         8080,
			ReadTimeout:  30,
			WriteTimeout: 60,
		},
		Database: DatabaseConfig{
			Path: "./data/nas-backup.db",
		},
		Backup: BackupConfig{
			Directories: []DirectoryConfig{},
			Exclusions: []ExclusionConfig{
				{Pattern: "*.tmp", RuleType: "extension", Enabled: true},
				{Pattern: "*.log", RuleType: "extension", Enabled: true},
				{Pattern: "node_modules", RuleType: "directory", Enabled: true},
				{Pattern: ".git", RuleType: "directory", Enabled: true},
				{Pattern: "__pycache__", RuleType: "directory", Enabled: true},
				{Pattern: ".DS_Store", RuleType: "pattern", Enabled: true},
				{Pattern: "Thumbs.db", RuleType: "pattern", Enabled: true},
			},
			SizeLimit: SizeLimitConfig{
				MaxFileSize: 0,
				MinFileSize: 0,
			},
			Schedule: ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 3 1 * *",
				Timezone: "Asia/Shanghai",
			},
			Compression: CompressionConfig{
				Enabled:   true,
				Algorithm: "zstd",
				Level:     19,
				SkipTypes: []string{
					".mp4", ".mkv", ".mov", ".avi", ".wmv",
					".jpg", ".jpeg", ".png", ".webp", ".gif",
					".mp3", ".flac", ".aac", ".ogg",
					".zip", ".7z", ".gz", ".rar", ".bz2", ".xz",
					".docx", ".xlsx", ".pptx", ".pdf",
				},
			},
			Retention: RetentionConfig{
				VersionKeepCount:  1,
				OrphanGraceDays:   180,
				FullResetInterval: 6,
				KeepDeletedDays:   180,
			},
			Encryption: EncryptionConfig{
				Algorithm:   "AES-256-GCM",
				KeyFilePath: "./data/master.key",
			},
		},
		OSS: OSSConfig{
			Endpoint:     "oss-cn-hangzhou.aliyuncs.com",
			StorageClass: "",
			Region:       "cn-hangzhou",
		},
		Rclone: RcloneConfig{
			BinaryPath: "rclone",
			ConfigPath: "./data/rclone.conf",
			RemoteName: "oss-crypt",
		},
		Logging: LoggingConfig{
			Level:    "info",
			FilePath: "./data/logs/nas-backup.log",
			MaxSize:  50,
			MaxFiles: 10,
		},
		// Reconcile defaults to dry-run for safety. The operator must
		// explicitly switch to auto-fix via config or API override.
		Reconcile: ReconcileConfig{
			DryRun:        true,
			OSSListPrefix: "data/",
		},
	}
}

// Load reads the configuration from the given YAML file path.
// If the file does not exist, it returns the default configuration.
func Load(path string) (*AppConfig, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// Validate checks the configuration for consistency and correctness.
func (c *AppConfig) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}

	if c.Database.Path == "" {
		return fmt.Errorf("database path must not be empty")
	}

	for i, dir := range c.Backup.Directories {
		if dir.Path == "" {
			return fmt.Errorf("backup directory [%d] has empty path", i)
		}
	}

	if c.Backup.Schedule.Enabled && c.Backup.Schedule.CronExpr == "" {
		return fmt.Errorf("schedule is enabled but cron expression is empty")
	}

	if c.Backup.Compression.Enabled {
		if c.Backup.Compression.Level < 1 || c.Backup.Compression.Level > 22 {
			return fmt.Errorf("zstd compression level must be 1-22, got %d", c.Backup.Compression.Level)
		}
	}

	if c.Backup.Retention.OrphanGraceDays < 0 {
		return fmt.Errorf("orphan_grace_days must be >= 0")
	}

	if c.Backup.Retention.VersionKeepCount < 1 {
		return fmt.Errorf("version_keep_count must be >= 1")
	}

	if c.OSS.Bucket == "" && c.OSS.AccessKeyID == "" {
		// Allow starting without OSS config — will be configured via API.
	}

	return nil
}

// EnsureDataDirs creates all necessary data directories.
func (c *AppConfig) EnsureDataDirs() error {
	dirs := []string{
		filepath.Dir(c.Database.Path),
		filepath.Dir(c.Backup.Encryption.KeyFilePath),
		filepath.Dir(c.Rclone.ConfigPath),
		filepath.Dir(c.Logging.FilePath),
		"./data/tmp",
	}

	for _, dir := range dirs {
		if dir == "." || dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	return nil
}

// ToModelsScheduleConfig converts the config-layer type to the models-layer type.
func (c *AppConfig) ToModelsScheduleConfig() models.ScheduleConfig {
	return models.ScheduleConfig{
		Enabled:  c.Backup.Schedule.Enabled,
		CronExpr: c.Backup.Schedule.CronExpr,
		Timezone: c.Backup.Schedule.Timezone,
	}
}

// ToModelsCompressionConfig converts the config-layer type to the models-layer type.
func (c *AppConfig) ToModelsCompressionConfig() models.CompressionConfig {
	return models.CompressionConfig{
		Enabled:   c.Backup.Compression.Enabled,
		Algorithm: c.Backup.Compression.Algorithm,
		Level:     c.Backup.Compression.Level,
		SkipTypes: c.Backup.Compression.SkipTypes,
	}
}

// ToModelsRetentionConfig converts the config-layer type to the models-layer type.
func (c *AppConfig) ToModelsRetentionConfig() models.RetentionConfig {
	return models.RetentionConfig{
		VersionKeepCount:  c.Backup.Retention.VersionKeepCount,
		OrphanGraceDays:   c.Backup.Retention.OrphanGraceDays,
		FullResetInterval: c.Backup.Retention.FullResetInterval,
		KeepDeletedDays:   c.Backup.Retention.KeepDeletedDays,
	}
}

// ToModelsUploadConfig converts the config-layer type to the models-layer type.
func (c *AppConfig) ToModelsUploadConfig() models.UploadConfig {
	return models.UploadConfig{
		StorageClass:   c.OSS.StorageClass,
		MaxConcurrency: 4,
		ChunkSizeMB:    64,
		RetryCount:     3,
		RetryDelaySec:  5,
	}
}

// ToModelsEncryptionConfig converts the config-layer type to the models-layer type.
func (c *AppConfig) ToModelsEncryptionConfig() models.EncryptionConfig {
	return models.EncryptionConfig{
		Algorithm:   c.Backup.Encryption.Algorithm,
		KeyFilePath: c.Backup.Encryption.KeyFilePath,
	}
}

// Now returns the current time in the configured timezone.
func (c *AppConfig) Now() time.Time {
	tz, err := time.LoadLocation(c.Backup.Schedule.Timezone)
	if err != nil {
		tz = time.UTC
	}
	return time.Now().In(tz)
}
