package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoadDefaultConfig 测试加载默认配置
func TestLoadDefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	minConfig := `
database:
  path: "` + tmpDir + `/backup.db"
`
	if err := os.WriteFile(cfgPath, []byte(minConfig), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected default Server.Port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected default Server.Host %q, got %q", "0.0.0.0", cfg.Server.Host)
	}
	if !cfg.Backup.Compression.Enabled {
		t.Error("expected Compression.Enabled to be true by default")
	}
	if cfg.Backup.Compression.Level != 19 {
		t.Errorf("expected default Compression.Level 19, got %d", cfg.Backup.Compression.Level)
	}
	if cfg.Backup.Retention.OrphanGraceDays != 180 {
		t.Errorf("expected default OrphanGraceDays 180, got %d", cfg.Backup.Retention.OrphanGraceDays)
	}
}

// TestLoadFullConfig 测试加载完整配置
func TestLoadFullConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	fullConfig := `
server:
  host: "127.0.0.1"
  port: 9090
database:
  path: "` + tmpDir + `/backup.db"
backup:
  schedule:
    enabled: true
    cron_expr: "0 2 * * *"
    timezone: "Asia/Shanghai"
  compression:
    enabled: true
    algorithm: "zstd"
    level: 10
  retention:
    version_keep_count: 3
    orphan_grace_days: 90
    full_reset_interval_months: 6
    keep_deleted_days: 30
  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "` + tmpDir + `/master.key"
oss:
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  bucket: "test-bucket"
  access_key_id: "test-ak-id"
  access_key_secret: "test-secret"
  storage_class: "ColdArchive"
rclone:
  binary_path: "/usr/local/bin/rclone"
  config_path: "` + tmpDir + `/rclone.conf"
  remote_name: "oss-crypt"
logging:
  level: "debug"
  file_path: "` + tmpDir + `/logs/nas-backup.log"
  max_size_mb: 100
  max_files: 5
`
	if err := os.WriteFile(cfgPath, []byte(fullConfig), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("expected Server.Port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected Server.Host %q, got %q", "127.0.0.1", cfg.Server.Host)
	}
	if !cfg.Backup.Schedule.Enabled {
		t.Error("expected Schedule.Enabled to be true")
	}
	if cfg.Backup.Schedule.CronExpr != "0 2 * * *" {
		t.Errorf("expected CronExpr %q, got %q", "0 2 * * *", cfg.Backup.Schedule.CronExpr)
	}
	if cfg.OSS.Bucket != "test-bucket" {
		t.Errorf("expected OSS.Bucket %q, got %q", "test-bucket", cfg.OSS.Bucket)
	}
	if cfg.Backup.Compression.Level != 10 {
		t.Errorf("expected Compression.Level 10, got %d", cfg.Backup.Compression.Level)
	}
}

// TestLoadNonexistentFile 测试加载不存在的配置文件
func TestLoadNonexistentFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected default config for nonexistent file, got error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil default config")
	}
}

// TestLoadInvalidYAML 测试加载无效的 YAML 配置
func TestLoadInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	invalidYAML := `
invalid: yaml: content
  - missing: [bracket
    not valid yaml at all: : : :
`
	if err := os.WriteFile(cfgPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// TestDefaultConfig 测试默认配置的合理性
func TestDefaultConfigFn(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected default Host %q, got %q", "0.0.0.0", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default Port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Database.Path == "" {
		t.Error("expected default Database.Path to be set")
	}
	if !cfg.Backup.Schedule.Enabled {
		t.Error("expected default Schedule.Enabled to be true")
	}
	if cfg.Backup.Schedule.CronExpr == "" {
		t.Error("expected default CronExpr to be set")
	}
	if len(cfg.Backup.Exclusions) == 0 {
		t.Error("expected default Exclusions to be populated")
	}
	if len(cfg.Backup.Compression.SkipTypes) == 0 {
		t.Error("expected default SkipTypes to be populated")
	}
}

// TestToModelsScheduleConfig 测试调度配置转换
func TestToModelsScheduleConfig(t *testing.T) {
	cfg := &AppConfig{
		Backup: BackupConfig{
			Schedule: ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 */6 * * *",
				Timezone: "Asia/Shanghai",
			},
		},
	}

	modelsCfg := cfg.ToModelsScheduleConfig()
	if !modelsCfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if modelsCfg.CronExpr != "0 */6 * * *" {
		t.Errorf("expected CronExpr %q, got %q", "0 */6 * * *", modelsCfg.CronExpr)
	}
}

// TestToModelsCompressionConfig 测试压缩配置转换
func TestToModelsCompressionConfig(t *testing.T) {
	cfg := &AppConfig{
		Backup: BackupConfig{
			Compression: CompressionConfig{
				Enabled:   true,
				Algorithm: "zstd",
				Level:     15,
				SkipTypes: []string{".mp4", ".zip"},
			},
		},
	}

	modelsCfg := cfg.ToModelsCompressionConfig()
	if !modelsCfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if modelsCfg.Algorithm != "zstd" {
		t.Errorf("expected Algorithm %q, got %q", "zstd", modelsCfg.Algorithm)
	}
	if modelsCfg.Level != 15 {
		t.Errorf("expected Level 15, got %d", modelsCfg.Level)
	}
	if len(modelsCfg.SkipTypes) != 2 {
		t.Errorf("expected 2 SkipTypes, got %d", len(modelsCfg.SkipTypes))
	}
}

// TestToModelsRetentionConfig 测试保留配置转换
func TestToModelsRetentionConfig(t *testing.T) {
	cfg := &AppConfig{
		Backup: BackupConfig{
			Retention: RetentionConfig{
				VersionKeepCount:  5,
				OrphanGraceDays:   120,
				FullResetInterval: 9,
				KeepDeletedDays:   60,
			},
		},
	}

	modelsCfg := cfg.ToModelsRetentionConfig()
	if modelsCfg.VersionKeepCount != 5 {
		t.Errorf("expected VersionKeepCount 5, got %d", modelsCfg.VersionKeepCount)
	}
	if modelsCfg.OrphanGraceDays != 120 {
		t.Errorf("expected OrphanGraceDays 120, got %d", modelsCfg.OrphanGraceDays)
	}
}

// TestToModelsUploadConfig 测试上传配置转换
func TestToModelsUploadConfig(t *testing.T) {
	cfg := &AppConfig{
		OSS: OSSConfig{
			StorageClass: "ColdArchive",
		},
	}

	modelsCfg := cfg.ToModelsUploadConfig()
	if modelsCfg.StorageClass != "ColdArchive" {
		t.Errorf("expected StorageClass %q, got %q", "ColdArchive", modelsCfg.StorageClass)
	}
}

// TestToModelsEncryptionConfig 测试加密配置转换
func TestToModelsEncryptionConfig(t *testing.T) {
	cfg := &AppConfig{
		Backup: BackupConfig{
			Encryption: EncryptionConfig{
				Algorithm:   "AES-256-GCM",
				KeyFilePath: "/path/to/key",
			},
		},
	}

	modelsCfg := cfg.ToModelsEncryptionConfig()
	if modelsCfg.Algorithm != "AES-256-GCM" {
		t.Errorf("expected Algorithm %q, got %q", "AES-256-GCM", modelsCfg.Algorithm)
	}
}

// TestValidatePortRange 测试端口范围验证
func TestValidatePortRange(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Path = tmpDir + "/test.db"
	data, _ := yaml.Marshal(cfg)
	os.WriteFile(cfgPath, data, 0644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for port 0")
	}

	cfg2 := DefaultConfig()
	cfg2.Server.Port = 65536
	cfg2.Database.Path = tmpDir + "/test2.db"
	data2, _ := yaml.Marshal(cfg2)
	os.WriteFile(cfgPath, data2, 0644)

	_, err = Load(cfgPath)
	if err == nil {
		t.Error("expected error for port 65536")
	}
}

// TestValidateCompressionLevel 测试压缩级别验证
func TestValidateCompressionLevel(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Backup.Compression.Enabled = true
	cfg.Backup.Compression.Level = 0
	cfg.Database.Path = tmpDir + "/test.db"
	data, _ := yaml.Marshal(cfg)
	os.WriteFile(cfgPath, data, 0644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for compression level 0")
	}
}

// TestValidateSchedule 测试调度配置验证
func TestValidateSchedule(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Backup.Schedule.Enabled = true
	cfg.Backup.Schedule.CronExpr = ""
	cfg.Database.Path = tmpDir + "/test.db"
	data, _ := yaml.Marshal(cfg)
	os.WriteFile(cfgPath, data, 0644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for enabled schedule with empty cron expression")
	}
}

// TestCompressionSkipTypesDefaults 测试默认压缩跳过类型
func TestCompressionSkipTypesDefaults(t *testing.T) {
	cfg := DefaultConfig()

	expectedSkipTypes := []string{
		".mp4", ".mkv", ".mov", ".avi", ".wmv",
		".jpg", ".jpeg", ".png", ".webp", ".gif",
		".mp3", ".flac", ".aac", ".ogg",
		".zip", ".7z", ".gz", ".rar", ".bz2", ".xz",
		".docx", ".xlsx", ".pptx", ".pdf",
	}

	skipMap := make(map[string]bool)
	for _, s := range cfg.Backup.Compression.SkipTypes {
		skipMap[s] = true
	}

	for _, ext := range expectedSkipTypes {
		if !skipMap[ext] {
			t.Errorf("expected %q in default SkipTypes", ext)
		}
	}
}

// TestExclusionDefaults 测试默认排除规则
func TestExclusionDefaults(t *testing.T) {
	cfg := DefaultConfig()

	expectedExclusions := map[string]string{
		"*.tmp":        "extension",
		"*.log":        "extension",
		"node_modules": "directory",
		".git":         "directory",
		"__pycache__":  "directory",
		".DS_Store":    "pattern",
		"Thumbs.db":    "pattern",
	}

	for _, rule := range cfg.Backup.Exclusions {
		expectedType, ok := expectedExclusions[rule.Pattern]
		if !ok {
			continue // 可能有额外的排除规则，不报错
		}
		if rule.RuleType != expectedType {
			t.Errorf("expected RuleType %q for %q, got %q", expectedType, rule.Pattern, rule.RuleType)
		}
		if !rule.Enabled {
			t.Errorf("expected exclusion %q to be enabled", rule.Pattern)
		}
	}
}

// TestConfigRoundTrip 测试配置序列化/反序列化
func TestConfigRoundTrip(t *testing.T) {
	orig := DefaultConfig()
	orig.Database.Path = t.TempDir() + "/test.db"

	data, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("yaml.Marshal failed: %v", err)
	}

	var loaded AppConfig
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}

	if loaded.Server.Port != orig.Server.Port {
		t.Errorf("roundtrip: Port mismatch %d vs %d", loaded.Server.Port, orig.Server.Port)
	}
	if loaded.Server.Host != orig.Server.Host {
		t.Errorf("roundtrip: Host mismatch %q vs %q", loaded.Server.Host, orig.Server.Host)
	}
}
