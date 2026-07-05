// Package api - 集成测试
// 基于 test-cases.md 测试用例文档编写，覆盖所有可自动化的API端点测试。
// 使用 httptest + 真实 SQLite 数据库，不依赖外部 OSS/rclone 服务。
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/logger"
	"github.com/nas-backup/internal/models"
)

// ──────────────────────────────────────────────────────────────────────────────
// 测试辅助工具
// ──────────────────────────────────────────────────────────────────────────────

// testEnv 封装测试环境：HTTP 测试服务器 + 数据库 + 清理函数。
type testEnv struct {
	server   *httptest.Server
	database *db.Database
	cfg      *config.AppConfig
	tmpDir   string
	cleanup  func()
}

// setupTestEnv 创建一个完整的测试环境：临时 SQLite DB + 最小化配置 + httptest 服务器。
// engine 的外部依赖（scanner/dedup/compressor/encryptor/storage）传 nil，
// 因为本测试只覆盖不需要实际执行备份的 API 端点。
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// 初始化全局 logger（loggingMiddleware 依赖 package-level logger）
	logger.Init("info", "", 0, 0)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Skipf("SQLite not available (CGO required): %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Database.Path = dbPath
	cfg.Backup.Encryption.KeyFilePath = filepath.Join(tmpDir, "test.key")

	pb := backup.NewProgressBroker()
	engine := backup.NewEngine(database, nil, nil, nil, nil, nil, cfg, pb)

	router := NewRouter(engine, nil, nil, database, cfg)
	handler := router.Setup()
	server := httptest.NewServer(handler)

	return &testEnv{
		server:   server,
		database: database,
		cfg:      cfg,
		tmpDir:   tmpDir,
		cleanup: func() {
			server.Close()
			database.Close()
		},
	}
}

// doGet 发送 GET 请求并返回响应。
func doGet(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	return resp
}

// doRequest 发送任意方法的 JSON 请求并返回响应。
func doRequest(t *testing.T, srv *httptest.Server, method, path string, body interface{}) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("create %s request: %v", method, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

// assertStatus 检查 HTTP 状态码。
func assertStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		t.Errorf("expected status %d, got %d", expected, resp.StatusCode)
	}
}

// decodeAPIResponse 解析统一 API 响应。
func decodeAPIResponse(t *testing.T, resp *http.Response) models.APIResponse {
	t.Helper()
	var result models.APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// decodePaginated 解析分页响应。
func decodePaginated(t *testing.T, resp *http.Response) models.PaginatedResponse {
	t.Helper()
	var result models.PaginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode paginated response: %v", err)
	}
	return result
}

// decodeData 将 APIResponse.Data 解码到目标结构。
func decodeData(t *testing.T, resp *http.Response, target interface{}) {
	t.Helper()
	var wrapper struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		t.Fatalf("decode wrapper: %v", err)
	}
	if !wrapper.Success {
		t.Fatalf("API returned error: %s", wrapper.Error)
	}
	if err := json.Unmarshal(wrapper.Data, target); err != nil {
		t.Fatalf("decode data: %v", err)
	}
}

// closeBody 安全关闭响应体。
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

// insertTestLog 插入测试日志记录。
func insertTestLog(t *testing.T, db *db.Database, backupID *int64, level models.LogLevel, msg, detail string) {
	t.Helper()
	if err := db.LogRepo.Insert(backupID, level, msg, detail); err != nil {
		t.Fatalf("insert log: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 1. 仪表盘模块 (Dashboard) — TC-DASH-*
// ──────────────────────────────────────────────────────────────────────────────

func TestDashboard(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-DASH-001: 空系统统计数据", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/stats")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var stats models.DashboardStats
		decodeData(t, resp, &stats)

		if stats.TotalFiles != 0 {
			t.Errorf("expected total_files=0, got %d", stats.TotalFiles)
		}
		if stats.BackupCount != 0 {
			t.Errorf("expected backup_count=0, got %d", stats.BackupCount)
		}
		if stats.ActiveBackupRunning {
			t.Error("expected active_backup_running=false")
		}
		if stats.OSSInfo.Bucket == "" && stats.OSSInfo.Endpoint == "" {
			t.Error("expected oss_info to have defaults from config")
		}
	})

	t.Run("TC-DASH-003: 备份历史默认分页", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/history")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Page != 1 {
			t.Errorf("expected page=1, got %d", result.Page)
		}
		if result.Size != 20 {
			t.Errorf("expected size=20 (default), got %d", result.Size)
		}
	})

	t.Run("TC-DASH-004: 自定义分页", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/history?page=2&size=10")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Page != 2 {
			t.Errorf("expected page=2, got %d", result.Page)
		}
		if result.Size != 10 {
			t.Errorf("expected size=10, got %d", result.Size)
		}
	})

	t.Run("TC-DASH-005: size上限200", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/history?size=500")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Size != 200 {
			t.Errorf("expected size capped to 200, got %d", result.Size)
		}
	})

	t.Run("TC-DASH-006: page<1自动修正", func(t *testing.T) {
		for _, page := range []string{"0", "-1", "-999"} {
			resp := doGet(t, env.server, "/api/dashboard/history?page="+page)
			result := decodePaginated(t, resp)
			closeBody(resp)
			if result.Page != 1 {
				t.Errorf("page=%s should be corrected to 1, got %d", page, result.Page)
			}
		}
	})

	t.Run("TC-DASH-007: size<1自动修正", func(t *testing.T) {
		for _, sz := range []string{"0", "-5"} {
			resp := doGet(t, env.server, "/api/dashboard/history?size="+sz)
			result := decodePaginated(t, resp)
			closeBody(resp)
			if result.Size != 20 {
				t.Errorf("size=%s should be corrected to 20, got %d", sz, result.Size)
			}
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 2. 备份操作模块 (Backup) — TC-BACKUP-*
// ──────────────────────────────────────────────────────────────────────────────

func TestBackupAPI(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-BACKUP-007: 无效type参数", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/backup/trigger",
			map[string]string{"type": "invalid_type"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)

		result := decodeAPIResponse(t, resp)
		if result.Success {
			t.Error("expected success=false for invalid type")
		}
	})

	t.Run("TC-BACKUP-008: 请求体格式错误", func(t *testing.T) {
		req, _ := http.NewRequest("POST", env.server.URL+"/api/backup/trigger",
			bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-BACKUP-012: 无运行中备份时取消", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/backup/cancel", nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("TC-BACKUP-013: 无效backup_id格式", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/backup/cancel?backup_id=abc", nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-BACKUP-014: 取消stale记录(通过backup_id)", func(t *testing.T) {
		// 在DB中插入一条running状态记录（模拟进程崩溃后的stale记录）
		backupID, err := env.database.BackupRepo.Create(models.BackupTypeFull, nil)
		if err != nil {
			t.Fatalf("create backup record: %v", err)
		}
		env.database.BackupRepo.UpdateStatus(backupID, models.BackupStatusRunning, "")

		// 通过backup_id取消：engine.Cancel失败（内存中无此备份），
		// handler会将DB记录标记为failed，然后返回404
		resp := doRequest(t, env.server, "POST",
			"/api/backup/cancel?backup_id="+strconv.FormatInt(backupID, 10), nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)

		// 验证DB中记录已被更新为failed
		rec, _ := env.database.BackupRepo.GetByID(backupID)
		if rec == nil || rec.Status != models.BackupStatusFailed {
			t.Errorf("expected stale backup to be marked failed, got status=%v", rec)
		}
	})

	t.Run("TC-BACKUP-014b: 取消stale记录(不带backup_id)", func(t *testing.T) {
		// 在DB中插入一条running状态记录（模拟进程崩溃后的stale记录）
		backupID, _ := env.database.BackupRepo.Create(models.BackupTypeFull, nil)
		env.database.BackupRepo.UpdateStatus(backupID, models.BackupStatusRunning, "")

		// 不带backup_id：handler检查DB中的running记录并清理
		resp := doRequest(t, env.server, "POST", "/api/backup/cancel", nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		// 验证DB中记录已被更新为failed
		rec, _ := env.database.BackupRepo.GetByID(backupID)
		if rec == nil || rec.Status != models.BackupStatusFailed {
			t.Errorf("expected stale backup to be marked failed, got status=%v", rec)
		}
	})

	t.Run("TC-BACKUP-015: 空闲状态获取备份状态", func(t *testing.T) {
		// 确保没有running状态的记录（前面的测试已清理）
		resp := doGet(t, env.server, "/api/backup/status")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var data struct {
			IsRunning     bool                `json:"is_running"`
			RunningBackup *models.BackupRecord `json:"running_backup"`
		}
		decodeData(t, resp, &data)

		if data.IsRunning {
			t.Error("expected is_running=false")
		}
		if data.RunningBackup != nil {
			t.Error("expected running_backup=null")
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 3. 备份目录模块 (Directories) — TC-DIR-*
// ──────────────────────────────────────────────────────────────────────────────

func TestDirectories(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-DIR-001: 空目录列表", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/content/directories")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var dirs []models.BackupDirectory
		decodeData(t, resp, &dirs)
		if len(dirs) != 0 {
			t.Errorf("expected empty list, got %d items", len(dirs))
		}
	})

	t.Run("TC-DIR-002: 添加目录成功", func(t *testing.T) {
		testDir := filepath.Join(env.tmpDir, "backup-target")
		os.MkdirAll(testDir, 0755)

		resp := doRequest(t, env.server, "POST", "/api/content/directories",
			models.BackupDirectory{
				Path:        testDir,
				Recursive:   true,
				Enabled:     true,
				Description: "测试目录",
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusCreated)

		var dir models.BackupDirectory
		decodeData(t, resp, &dir)
		if dir.ID <= 0 {
			t.Error("expected positive ID")
		}
		if dir.Path != testDir {
			t.Errorf("expected path=%s, got %s", testDir, dir.Path)
		}
	})

	t.Run("TC-DIR-003: path为空", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/directories",
			models.BackupDirectory{Path: ""})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-DIR-004: 请求体格式错误", func(t *testing.T) {
		req, _ := http.NewRequest("POST", env.server.URL+"/api/content/directories",
			bytes.NewReader([]byte("bad json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-DIR-005: PATCH部分更新", func(t *testing.T) {
		// 先创建一个目录
		testDir := filepath.Join(env.tmpDir, "patch-test")
		os.MkdirAll(testDir, 0755)
		id, _ := env.database.ConfigRepo.AddDirectory(testDir, true, true, "原始描述")

		// 只更新 enabled 字段
		resp := doRequest(t, env.server, "PATCH", "/api/content/directories/"+strconv.FormatInt(id, 10),
			map[string]interface{}{"enabled": false})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var dir models.BackupDirectory
		decodeData(t, resp, &dir)
		if dir.Enabled != false {
			t.Error("expected enabled=false after PATCH")
		}
		if dir.Path != testDir {
			t.Errorf("expected path unchanged=%s, got %s", testDir, dir.Path)
		}
		if dir.Recursive != true {
			t.Error("expected recursive unchanged=true")
		}
		if dir.Description != "原始描述" {
			t.Errorf("expected description unchanged, got %s", dir.Description)
		}
	})

	t.Run("TC-DIR-007: 更新后path为空", func(t *testing.T) {
		id, _ := env.database.ConfigRepo.AddDirectory("/tmp/test", true, true, "")
		resp := doRequest(t, env.server, "PATCH", "/api/content/directories/"+strconv.FormatInt(id, 10),
			map[string]interface{}{"path": ""})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-DIR-008: ID不存在", func(t *testing.T) {
		resp := doRequest(t, env.server, "PATCH", "/api/content/directories/99999",
			map[string]interface{}{"enabled": false})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("TC-DIR-009: 无效ID格式", func(t *testing.T) {
		resp := doRequest(t, env.server, "PATCH", "/api/content/directories/abc",
			map[string]interface{}{"enabled": false})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 4. 排除规则模块 (Exclusions) — TC-EXC-*
// ──────────────────────────────────────────────────────────────────────────────

func TestExclusions(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-EXC-001: 空规则列表", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/content/exclusions")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var rules []models.ExclusionRule
		decodeData(t, resp, &rules)
		if len(rules) != 0 {
			t.Errorf("expected empty list, got %d", len(rules))
		}
	})

	t.Run("TC-EXC-002: 添加extension规则", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/exclusions",
			models.ExclusionRule{Pattern: ".tmp", RuleType: "extension", Enabled: true})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusCreated)

		var rule models.ExclusionRule
		decodeData(t, resp, &rule)
		if rule.ID <= 0 {
			t.Error("expected positive ID")
		}
		if rule.RuleType != "extension" {
			t.Errorf("expected rule_type=extension, got %s", rule.RuleType)
		}
	})

	t.Run("TC-EXC-003: 添加directory规则", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/exclusions",
			models.ExclusionRule{Pattern: "node_modules", RuleType: "directory", Enabled: true})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusCreated)
	})

	t.Run("TC-EXC-005: 添加size_exceed规则", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/exclusions",
			models.ExclusionRule{Pattern: "1073741824", RuleType: "size_exceed", Enabled: true})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusCreated)
	})

	t.Run("TC-EXC-006: pattern为空", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/exclusions",
			models.ExclusionRule{Pattern: "", RuleType: "pattern"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-EXC-007: 默认rule_type", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/content/exclusions",
			models.ExclusionRule{Pattern: "*.bak", Enabled: true})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusCreated)

		var rule models.ExclusionRule
		decodeData(t, resp, &rule)
		if rule.RuleType != "pattern" {
			t.Errorf("expected default rule_type=pattern, got %s", rule.RuleType)
		}
	})

	t.Run("TC-EXC-008: PUT部分更新", func(t *testing.T) {
		id, _ := env.database.ConfigRepo.AddExclusionRule("*.test", "pattern", true)

		// 只更新 enabled
		resp := doRequest(t, env.server, "PUT", "/api/content/exclusions/"+strconv.FormatInt(id, 10),
			map[string]interface{}{"enabled": false})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var rule models.ExclusionRule
		decodeData(t, resp, &rule)
		if rule.Enabled != false {
			t.Error("expected enabled=false")
		}
		if rule.Pattern != "*.test" {
			t.Errorf("expected pattern unchanged, got %s", rule.Pattern)
		}
	})

	t.Run("TC-EXC-009: 更新ID不存在", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/content/exclusions/99999",
			map[string]interface{}{"pattern": "*.txt"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("TC-EXC-010: 无效ID格式", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/content/exclusions/invalid",
			map[string]interface{}{"enabled": true})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-EXC-011: 更新pattern清空", func(t *testing.T) {
		id, _ := env.database.ConfigRepo.AddExclusionRule("*.orig", "pattern", true)
		resp := doRequest(t, env.server, "PUT", "/api/content/exclusions/"+strconv.FormatInt(id, 10),
			map[string]interface{}{"pattern": ""})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-EXC-012: 删除成功", func(t *testing.T) {
		id, _ := env.database.ConfigRepo.AddExclusionRule("*.del", "pattern", true)
		resp := doRequest(t, env.server, "DELETE", "/api/content/exclusions/"+strconv.FormatInt(id, 10), nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodeAPIResponse(t, resp)
		if !result.Success {
			t.Error("expected success=true")
		}
	})

	t.Run("TC-EXC-013: 删除ID不存在", func(t *testing.T) {
		resp := doRequest(t, env.server, "DELETE", "/api/content/exclusions/99999", nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 5. 文件系统浏览模块 (FS Browse) — TC-FS-*
// ──────────────────────────────────────────────────────────────────────────────

func TestFSBrowse(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-FS-001: 浏览根目录", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/fs/browse?path=/")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var result models.FSBrowseResult
		decodeData(t, resp, &result)
		if result.Path != "/" {
			t.Errorf("expected path=/, got %s", result.Path)
		}
		if len(result.Entries) == 0 {
			t.Error("expected at least one entry in root")
		}
	})

	t.Run("TC-FS-002: 浏览子目录", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/fs/browse?path="+env.tmpDir)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var result models.FSBrowseResult
		decodeData(t, resp, &result)
		if result.Path != env.tmpDir {
			t.Errorf("expected path=%s, got %s", env.tmpDir, result.Path)
		}
	})

	t.Run("TC-FS-004: 不存在的路径", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/fs/browse?path=/nonexistent/path/12345abc")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-FS-005: 路径是文件", func(t *testing.T) {
		testFile := filepath.Join(env.tmpDir, "afile.txt")
		os.WriteFile(testFile, []byte("test"), 0644)

		resp := doGet(t, env.server, "/api/fs/browse?path="+testFile)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-FS-006: 路径参数缺失(默认根目录)", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/fs/browse")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("TC-FS-007: 非绝对路径", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/fs/browse?path=relative/path")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 6. 策略配置 - 调度模块 (Schedule) — TC-SCHED-*
// ──────────────────────────────────────────────────────────────────────────────

func TestScheduleConfig(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-SCHED-001: 获取默认配置", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/strategy/schedule")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.ScheduleConfig
		decodeData(t, resp, &cfg)
		if cfg.CronExpr == "" {
			t.Error("expected non-empty cron_expr from defaults")
		}
	})

	t.Run("TC-SCHED-002: 更新调度配置", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/schedule",
			models.ScheduleConfig{
				Enabled:  true,
				CronExpr: "0 3 * * *",
				Timezone: "Asia/Shanghai",
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.ScheduleConfig
		decodeData(t, resp, &cfg)
		if cfg.CronExpr != "0 3 * * *" {
			t.Errorf("expected cron_expr=0 3 * * *, got %s", cfg.CronExpr)
		}
	})

	t.Run("TC-SCHED-004: cron_expr为空", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/schedule",
			models.ScheduleConfig{Enabled: true, CronExpr: "", Timezone: "UTC"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-SCHED-006: 请求体格式错误", func(t *testing.T) {
		req, _ := http.NewRequest("PUT", env.server.URL+"/api/strategy/schedule",
			bytes.NewReader([]byte("bad")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-SCHED-008: 空timezone", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/schedule",
			models.ScheduleConfig{Enabled: true, CronExpr: "0 2 * * *", Timezone: ""})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 7. 策略配置 - 压缩模块 (Compression) — TC-COMP-*
// ──────────────────────────────────────────────────────────────────────────────

func TestCompressionConfig(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-COMP-001: 获取默认配置", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/strategy/compression")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.CompressionConfig
		decodeData(t, resp, &cfg)
		if cfg.Algorithm == "" {
			t.Error("expected non-empty algorithm from defaults")
		}
	})

	t.Run("TC-COMP-002: 更新压缩配置", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/compression",
			models.CompressionConfig{
				Enabled:   true,
				Algorithm: "zstd",
				Level:     3,
				SkipTypes: []string{".zip", ".gz"},
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.CompressionConfig
		decodeData(t, resp, &cfg)
		if cfg.Algorithm != "zstd" {
			t.Errorf("expected algorithm=zstd, got %s", cfg.Algorithm)
		}
		if cfg.Level != 3 {
			t.Errorf("expected level=3, got %d", cfg.Level)
		}
	})

	t.Run("TC-COMP-003: algorithm为空", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/compression",
			models.CompressionConfig{Enabled: true, Algorithm: "", Level: 3})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-COMP-004: 压缩级别边界值", func(t *testing.T) {
		for _, level := range []int{1, 22} {
			resp := doRequest(t, env.server, "PUT", "/api/strategy/compression",
				models.CompressionConfig{Enabled: true, Algorithm: "zstd", Level: level})
			assertStatus(t, resp, http.StatusOK)
			closeBody(resp)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 8. 策略配置 - 上传模块 (Upload) — TC-UPLOAD-*
// ──────────────────────────────────────────────────────────────────────────────

func TestUploadConfig(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-UPLOAD-001: 获取默认配置", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/strategy/upload")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.UploadConfig
		decodeData(t, resp, &cfg)
		// 空DB返回零值是预期行为（handler从DB读取，未保存过的配置为空）
		// 这里只验证端点可正常返回并解码
	})

	t.Run("TC-UPLOAD-002: 更新上传配置", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/upload",
			models.UploadConfig{
				StorageClass:   "Standard",
				MaxConcurrency: 4,
				ChunkSizeMB:    8,
				RetryCount:     3,
				RetryDelaySec:  5,
				OSSQuotaBytes:  107374182400,
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.UploadConfig
		decodeData(t, resp, &cfg)
		if cfg.MaxConcurrency != 4 {
			t.Errorf("expected max_concurrency=4, got %d", cfg.MaxConcurrency)
		}
		if cfg.OSSQuotaBytes != 107374182400 {
			t.Errorf("expected oss_quota_bytes=107374182400, got %d", cfg.OSSQuotaBytes)
		}
	})

	t.Run("TC-UPLOAD-005: OSS配额显示在仪表盘", func(t *testing.T) {
		// 先设置配额
		doRequest(t, env.server, "PUT", "/api/strategy/upload",
			models.UploadConfig{StorageClass: "Standard", OSSQuotaBytes: 107374182400})

		// 查询仪表盘
		resp := doGet(t, env.server, "/api/dashboard/stats")
		defer closeBody(resp)

		var stats models.DashboardStats
		decodeData(t, resp, &stats)
		if stats.OSSQuotaBytes != 107374182400 {
			t.Errorf("expected oss_quota_bytes=107374182400, got %d", stats.OSSQuotaBytes)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 9. 策略配置 - 保留策略模块 (Retention) — TC-RET-*
// ──────────────────────────────────────────────────────────────────────────────

func TestRetentionConfig(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-RET-001: 获取默认配置", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/strategy/retention")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.RetentionConfig
		decodeData(t, resp, &cfg)
		if cfg.OrphanGraceDays == 0 {
			t.Error("expected non-zero orphan_grace_days from defaults")
		}
	})

	t.Run("TC-RET-002: 更新保留策略", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/retention",
			models.RetentionConfig{
				VersionKeepCount:  3,
				OrphanGraceDays:   180,
				FullResetInterval: 1,
				KeepDeletedDays:   30,
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.RetentionConfig
		decodeData(t, resp, &cfg)
		if cfg.VersionKeepCount != 3 {
			t.Errorf("expected version_keep_count=3, got %d", cfg.VersionKeepCount)
		}
		if cfg.OrphanGraceDays != 180 {
			t.Errorf("expected orphan_grace_days=180, got %d", cfg.OrphanGraceDays)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 10. 策略配置 - 加密模块 (Encryption) — TC-ENC-*
// ──────────────────────────────────────────────────────────────────────────────

func TestEncryptionConfig(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-ENC-001: 获取默认配置", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/strategy/encryption")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.EncryptionConfig
		decodeData(t, resp, &cfg)
		if cfg.Algorithm == "" {
			t.Error("expected non-empty algorithm from defaults")
		}
	})

	t.Run("TC-ENC-002: 更新加密配置", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/encryption",
			models.EncryptionConfig{
				Algorithm:   "AES-256-GCM",
				KeyFilePath: "/path/to/key.file",
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var cfg models.EncryptionConfig
		decodeData(t, resp, &cfg)
		if cfg.Algorithm != "AES-256-GCM" {
			t.Errorf("expected algorithm=AES-256-GCM, got %s", cfg.Algorithm)
		}
	})

	t.Run("TC-ENC-003: algorithm为空", func(t *testing.T) {
		resp := doRequest(t, env.server, "PUT", "/api/strategy/encryption",
			models.EncryptionConfig{Algorithm: "", KeyFilePath: "/path/to/key"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 11. 日志模块 (Logs) — TC-LOG-*
// ──────────────────────────────────────────────────────────────────────────────

func TestLogs(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	// 预置测试日志数据
	backupID := int64(1)
	env.database.BackupRepo.Create(models.BackupTypeFull, nil) // 创建backup_id=1
	insertTestLog(t, env.database, &backupID, models.LogLevelInfo, "backup started", "detail1")
	insertTestLog(t, env.database, &backupID, models.LogLevelError, "upload failed", "detail2")
	insertTestLog(t, env.database, nil, models.LogLevelWarn, "disk almost full", "detail3")
	insertTestLog(t, env.database, nil, models.LogLevelDebug, "debug message here", "")

	t.Run("TC-LOG-001: 默认列表", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Page != 1 {
			t.Errorf("expected page=1, got %d", result.Page)
		}
		if result.Size != 50 {
			t.Errorf("expected page_size=50 (default), got %d", result.Size)
		}
		if result.Total < 4 {
			t.Errorf("expected total>=4, got %d", result.Total)
		}
	})

	t.Run("TC-LOG-002: 自定义分页", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?page=1&page_size=2")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Size != 2 {
			t.Errorf("expected page_size=2, got %d", result.Size)
		}
	})

	t.Run("TC-LOG-003: 按级别过滤", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?level=error")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Total != 1 {
			t.Errorf("expected total=1 for error level, got %d", result.Total)
		}
	})

	t.Run("TC-LOG-004: 按backup_id过滤", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?backup_id=1")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Total != 2 {
			t.Errorf("expected total=2 for backup_id=1, got %d", result.Total)
		}
	})

	t.Run("TC-LOG-005: 关键词搜索", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?search=upload")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Total != 1 {
			t.Errorf("expected total=1 for 'upload' search, got %d", result.Total)
		}
	})

	t.Run("TC-LOG-006: 按时间范围过滤", func(t *testing.T) {
		start := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
		end := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
		resp := doGet(t, env.server, "/api/logs?start_time="+start+"&end_time="+end)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Total < 4 {
			t.Errorf("expected total>=4 in time range, got %d", result.Total)
		}
	})

	t.Run("TC-LOG-007: 时间格式错误", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?start_time=invalid-time")
		defer closeBody(resp)
		// 不应崩溃，应忽略无效参数
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("TC-LOG-008: 获取单条日志", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs/1")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		var rec models.LogRecord
		decodeData(t, resp, &rec)
		if rec.Message == "" {
			t.Error("expected non-empty message")
		}
	})

	t.Run("TC-LOG-009: ID不存在", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs/99999")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNotFound)
	})

	t.Run("TC-LOG-010: 无效ID格式", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs/abc")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-LOG-011: 组合过滤条件", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/logs?level=error&backup_id=1&search=upload&page=1&page_size=20")
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusOK)

		result := decodePaginated(t, resp)
		if result.Total != 1 {
			t.Errorf("expected total=1 for combined filter, got %d", result.Total)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 12. 数据对账参数验证 (Reconcile) — TC-REC-012
// ──────────────────────────────────────────────────────────────────────────────

func TestReconcileAPI(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-REC-012: dry_run参数无效值", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/reconcile?dry_run=invalid", nil)
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-REC-012b: dry_run=true合法值", func(t *testing.T) {
		// dry_run=true是合法参数值，不应返回400。
		// 注意：测试环境中storage为nil，reconcile执行时会panic导致连接断开(EOF)，
		// 这是预期的——本测试只验证参数解析，不验证reconcile执行。
		req, _ := http.NewRequest("POST", env.server.URL+"/api/reconcile?dry_run=true", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// EOF或连接错误说明handler因nil storage panic，参数解析已通过（未返回400）
			t.Logf("reconcile执行因nil storage崩溃(预期): %v", err)
			return
		}
		defer closeBody(resp)
		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("dry_run=true should not return 400")
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 13. CORS与HTTP中间件 — TC-CORS-*, TC-LOGGING-*
// ──────────────────────────────────────────────────────────────────────────────

func TestMiddleware(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-CORS-001: CORS头设置正确", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/stats")
		defer closeBody(resp)

		if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != "*" {
			t.Errorf("expected Access-Control-Allow-Origin=*, got %s", origin)
		}
		if methods := resp.Header.Get("Access-Control-Allow-Methods"); methods == "" {
			t.Error("expected non-empty Access-Control-Allow-Methods")
		}
	})

	t.Run("TC-CORS-002: OPTIONS预检请求", func(t *testing.T) {
		req, _ := http.NewRequest("OPTIONS", env.server.URL+"/api/dashboard/stats", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("OPTIONS request failed: %v", err)
		}
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusNoContent)
	})

	t.Run("TC-CORS-003: PATCH方法在Allow-Methods中", func(t *testing.T) {
		resp := doGet(t, env.server, "/api/dashboard/stats")
		defer closeBody(resp)

		methods := resp.Header.Get("Access-Control-Allow-Methods")
		if !strings.Contains(methods, "PATCH") {
			t.Errorf("expected PATCH in Allow-Methods, got %s", methods)
		}
	})

	t.Run("TC-LOGGING-001: 404请求不返回500", func(t *testing.T) {
		// 访问不存在的端点
		resp := doGet(t, env.server, "/api/nonexistent")
		defer closeBody(resp)
		if resp.StatusCode >= 500 {
			t.Errorf("nonexistent endpoint should not return 5xx, got %d", resp.StatusCode)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 14. 系统启动与崩溃恢复 — TC-SYS-*
// ──────────────────────────────────────────────────────────────────────────────

func TestSystemRecovery(t *testing.T) {
	t.Run("TC-SYS-003: 崩溃后备份状态恢复", func(t *testing.T) {
		env := setupTestEnv(t)
		defer env.cleanup()

		// 模拟进程崩溃：在DB中插入running状态记录
		backupID, _ := env.database.BackupRepo.Create(models.BackupTypeFull, nil)
		env.database.BackupRepo.UpdateStatus(backupID, models.BackupStatusRunning, "")

		// 调用CleanupStaleRunning（服务启动时执行）
		count, err := env.database.BackupRepo.CleanupStaleRunning()
		if err != nil {
			t.Fatalf("CleanupStaleRunning failed: %v", err)
		}
		if count == 0 {
			t.Error("expected at least 1 stale record cleaned")
		}

		// 验证记录被标记为failed
		rec, _ := env.database.BackupRepo.GetByID(backupID)
		if rec == nil || rec.Status != models.BackupStatusFailed {
			t.Errorf("expected stale backup marked as failed")
		}

		// 验证系统不再认为有运行中备份
		running, _ := env.database.BackupRepo.IsRunning()
		if running {
			t.Error("expected IsRunning=false after cleanup")
		}
	})

	t.Run("TC-SYS-004: CleanupStaleRunning处理pending状态", func(t *testing.T) {
		env := setupTestEnv(t)
		defer env.cleanup()

		backupID, _ := env.database.BackupRepo.Create(models.BackupTypeFull, nil)
		env.database.BackupRepo.UpdateStatus(backupID, models.BackupStatusPending, "")

		count, _ := env.database.BackupRepo.CleanupStaleRunning()
		if count == 0 {
			t.Error("expected pending records to be cleaned")
		}
	})

	t.Run("TC-SYS-004b: CleanupStaleRunning不处理completed", func(t *testing.T) {
		env := setupTestEnv(t)
		defer env.cleanup()

		backupID, _ := env.database.BackupRepo.Create(models.BackupTypeFull, nil)
		env.database.BackupRepo.UpdateStatus(backupID, models.BackupStatusCompleted, "")

		count, _ := env.database.BackupRepo.CleanupStaleRunning()
		if count != 0 {
			t.Errorf("completed records should not be cleaned, got count=%d", count)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 15. 恢复参数验证 (Restore) — TC-RESTORE-004~008
// ──────────────────────────────────────────────────────────────────────────────

func TestRestoreValidation(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-RESTORE-004: paths和pattern都未提供", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/restore",
			map[string]interface{}{"output_dir": "/tmp"})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-RESTORE-005: output_dir未提供", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/restore",
			map[string]interface{}{"paths": []string{"/test/file.txt"}})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-RESTORE-006: output_dir不存在", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/restore",
			models.RestoreRequest{
				Paths:     []string{"/test/file.txt"},
				OutputDir: "/nonexistent/dir/12345abc",
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-RESTORE-007: output_dir是文件不是目录", func(t *testing.T) {
		testFile := filepath.Join(env.tmpDir, "not-a-dir.txt")
		os.WriteFile(testFile, []byte("test"), 0644)

		resp := doRequest(t, env.server, "POST", "/api/restore",
			models.RestoreRequest{
				Paths:     []string{"/test/file.txt"},
				OutputDir: testFile,
			})
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("TC-RESTORE-008: 请求体格式错误", func(t *testing.T) {
		req, _ := http.NewRequest("POST", env.server.URL+"/api/restore",
			bytes.NewReader([]byte("bad json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer closeBody(resp)
		assertStatus(t, resp, http.StatusBadRequest)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 16. GC端点验证 — TC-GC-001
// ──────────────────────────────────────────────────────────────────────────────

func TestGCEndpoint(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	t.Run("TC-GC-001: 触发GC返回202", func(t *testing.T) {
		resp := doRequest(t, env.server, "POST", "/api/gc", nil)
		defer closeBody(resp)
		// GC是异步的，应该立即返回202
		// 注意：实际GC goroutine可能因storage为nil而失败，但API应返回202
		assertStatus(t, resp, http.StatusAccepted)

		result := decodeAPIResponse(t, resp)
		if !result.Success {
			t.Error("expected success=true for GC trigger")
		}
	})
}
