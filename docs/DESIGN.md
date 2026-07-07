# NAS Backup 恢复功能架构设计文档 (DESIGN)

## 1. 总体架构

恢复功能作为现有 NAS Backup 系统的扩展模块，保持技术栈一致，不引入新框架或新依赖。

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Web UI (React + Vite)                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │
│  │   Restore    │  │  File Browse │  │  Progress    │  │   History    │    │
│  │    Page      │  │   Component  │  │    Panel     │  │    Panel     │    │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘    │
│         │                 │                 │                 │             │
│  ┌──────┴───────┐  ┌──────┴───────┐  ┌──────┴───────┐  ┌──────┴───────┐    │
│  │  restoreApi  │  │    api.ts    │  │  createRestore│  │  restoreApi  │   │
│  │  (REST API)  │  │  (types)     │  │ProgressStream│  │  (history)   │   │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘    │
└─────────┼─────────────────┼─────────────────┼─────────────────┼─────────────┘
          │ HTTP API Calls   │                 │ SSE             │
          ▼                  ▼                 ▼                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           HTTP Router (Go)                                   │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │
│  │ POST /api/   │  │ GET /api/    │  │ GET /api/    │  │ GET /api/    │    │
│  │ restore      │  │ restore/files│  │ restore/prog-│  │ backups      │    │
│  │              │  │              │  │ ress/stream  │  │              │    │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘    │
│         │                 │                 │                 │             │
│  ┌──────┴───────┐  ┌──────┴───────┐  ┌──────┴───────┐                      │
│  │ restoreJob   │  │  fileRepo    │  │ restoreProgr-│                      │
│  │ Manager      │  │  query       │  │ essBroker    │                      │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘                      │
└─────────┼─────────────────┼─────────────────┼──────────────────────────────┘
          │                 │                 │
          ▼                 ▼                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Business Logic Layer                                │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                       │
│  │  RestoreJob  │  │   Restorer   │  │ ProgressBroker│                     │
│  │  Manager     │  │   (existing) │  │ (restore SSE) │                     │
│  │  (async)     │  │   core flow  │  │               │                     │
│  └──────┬───────┘  └──────┬───────┘  └───────────────┘                       │
│         │                 │                                                  │
│         │         (thaw → download → decrypt → decompress → verify → move)   │
│         │                 │                                                  │
│         ▼                 ▼                                                  │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                          SQLite Database                              │   │
│  │  files │ backups │ backup_files │ hash_index │ restore_jobs │ logs   │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│                           OSS via rclone crypt                               │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. 数据模型扩展

### 2.1 新增 `restore_jobs` 表（003_add_restore_jobs.sql）

```sql
CREATE TABLE IF NOT EXISTS restore_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    paths       TEXT NOT NULL,              -- JSON array of restored paths
    pattern     TEXT,                       -- glob pattern if used
    backup_id   INTEGER,                    -- specific backup version
    output_dir  TEXT NOT NULL,
    expedited   BOOLEAN NOT NULL DEFAULT 0,
    conflict_strategy TEXT DEFAULT 'skip',  -- 'overwrite' | 'skip' | 'rename'
    total_files INTEGER NOT NULL DEFAULT 0,
    restored_files INTEGER NOT NULL DEFAULT 0,
    failed_files TEXT,                      -- JSON array of failed paths
    total_size  INTEGER NOT NULL DEFAULT 0,
    restored_size INTEGER NOT NULL DEFAULT 0,
    elapsed_ms  INTEGER,
    error_message TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    started_at  TEXT,
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_restore_jobs_status ON restore_jobs(status);
CREATE INDEX IF NOT EXISTS idx_restore_jobs_created_at ON restore_jobs(created_at);
```

### 2.2 新增 Go Model 类型

```go
// models/models.go 新增

// RestoreJobStatus represents the lifecycle state of a restore job.
type RestoreJobStatus string

const (
    RestoreJobStatusPending    RestoreJobStatus = "pending"
    RestoreJobStatusRunning    RestoreJobStatus = "running"
    RestoreJobStatusCompleted  RestoreJobStatus = "completed"
    RestoreJobStatusFailed     RestoreJobStatus = "failed"
    RestoreJobStatusCancelled  RestoreJobStatus = "cancelled"
)

// RestoreJobRecord tracks a single restore operation.
type RestoreJobRecord struct {
    ID                int64            `json:"id"`
    Status            RestoreJobStatus `json:"status"`
    Paths             []string         `json:"paths,omitempty"`
    Pattern           string           `json:"pattern,omitempty"`
    BackupID          *int64           `json:"backup_id,omitempty"`
    OutputDir         string           `json:"output_dir"`
    Expedited         bool             `json:"expedited"`
    ConflictStrategy  string           `json:"conflict_strategy"`
    TotalFiles        int              `json:"total_files"`
    RestoredFiles     int              `json:"restored_files"`
    FailedFiles       []string         `json:"failed_files,omitempty"`
    TotalSize         int64            `json:"total_size"`
    RestoredSize      int64            `json:"restored_size"`
    ElapsedMs         int64            `json:"elapsed_ms,omitempty"`
    ErrorMessage      string           `json:"error_message,omitempty"`
    CreatedAt         time.Time        `json:"created_at"`
    StartedAt         *time.Time       `json:"started_at,omitempty"`
    CompletedAt       *time.Time       `json:"completed_at,omitempty"`
}

// RestoreProgressEvent is sent via SSE for restore operations.
type RestoreProgressEvent struct {
    Type           string  `json:"type"`        // "phase", "progress", "file", "connected"
    JobID          int64   `json:"job_id"`
    Phase          string  `json:"phase,omitempty"`    // "preparing", "thawing", "downloading", "decrypting", "decompressing", "verifying", "moving", "completed", "failed", "cancelled"
    PhaseName      string  `json:"phase_name,omitempty"`
    Current        int     `json:"current,omitempty"`
    Total          int     `json:"total,omitempty"`
    Percent        float64 `json:"percent,omitempty"`
    Message        string  `json:"message,omitempty"`
    FilePath       string  `json:"file_path,omitempty"`
    FileSize       int64   `json:"file_size,omitempty"`
    RestoredSize   int64   `json:"restored_size,omitempty"`
    TotalSize      int64   `json:"total_size,omitempty"`
    Timestamp      time.Time `json:"timestamp"`
}

// RestoreListRequest is the query params for listing restorable files.
type RestoreListRequest struct {
    DirPath   string  `json:"dir_path,omitempty"`
    BackupID  *int64  `json:"backup_id,omitempty"`
    Search    string  `json:"search,omitempty"`
    Page      int     `json:"page"`
    PageSize  int     `json:"page_size"`
}

// RestorableFile represents a file that can be restored, with its backup metadata.
type RestorableFile struct {
    FileRecord
    BackupFileRecord
    BackupCount   int       `json:"backup_count"`     // how many backups contain this file
    LatestBackup  int64     `json:"latest_backup_id"` // most recent backup containing this file
    LatestBackupAt time.Time `json:"latest_backup_at"` // when that backup was created
}
```

## 3. API 契约

### 3.1 新增后端路由

在 `router.go` 的 `Setup()` 方法中新增以下路由：

| 方法 | 路径 | 功能 | Handler | 超时 |
|------|------|------|---------|------|
| POST | `/api/restore` | 创建恢复任务（异步） | handleRestore | 30s（仅创建任务） |
| GET | `/api/restore/files` | 列出可恢复文件（分页+搜索） | handleListRestorableFiles | 30s |
| GET | `/api/restore/progress/stream` | SSE 恢复进度流 | handleRestoreProgressStream | 无（SSE） |
| GET | `/api/restore/jobs` | 列出恢复任务历史 | handleListRestoreJobs | 30s |
| GET | `/api/restore/jobs/{id}` | 查看单个恢复任务详情 | handleGetRestoreJob | 30s |
| POST | `/api/restore/jobs/{id}/cancel` | 取消恢复任务 | handleCancelRestoreJob | 30s |
| GET | `/api/backups` | 列出备份会话（已有CLI，新增HTTP） | handleListBackups | 30s |

### 3.2 请求/响应详细定义

**POST `/api/restore`** — 创建恢复任务

请求体：
```json
{
  "paths": ["/data/docs/report.pdf", "/data/photos/"],
  "pattern": "*.pdf",
  "backup_id": 42,
  "output_dir": "/tmp/restore",
  "expedited": false,
  "conflict_strategy": "skip"
}
```

响应（202 Accepted）：
```json
{
  "success": true,
  "data": {
    "job_id": 7,
    "status": "pending",
    "total_files": 15,
    "total_size": 1073741824
  }
}
```

**GET `/api/restore/files`** — 列出可恢复文件

查询参数：`?dir_path=/data&backup_id=42&search=report&page=1&size=20`

响应：
```json
{
  "success": true,
  "data": [...],
  "total": 153,
  "page": 1,
  "size": 20
}
```

**GET `/api/restore/jobs`** — 恢复任务历史

查询参数：`?page=1&size=10&status=completed`

响应（PaginatedResponse<RestoreJobRecord>）：
```json
{
  "success": true,
  "data": [...],
  "total": 23,
  "page": 1,
  "size": 10
}
```

### 3.3 SSE 恢复进度事件格式

恢复进度使用独立的 SSE 端点 `/api/restore/progress/stream`，与备份的 `/api/backup/progress/stream` 完全隔离，避免事件混淆。

事件类型与数据结构：

| event type | 含义 | 字段 |
|-----------|------|------|
| `connected` | 连接建立 | job_id, message |
| `phase` | 阶段切换 | job_id, phase, phase_name, message |
| `progress` | 进度更新 | job_id, current, total, percent, restored_size, total_size |
| `file` | 单文件事件 | job_id, file_path, file_size, message |
| `log` | 日志消息 | job_id, level, message, detail |

## 4. 后端组件设计

### 4.1 RestoreJobManager（新增）

负责恢复任务的异步调度、状态管理和取消控制。

```go
// internal/backup/restore_job.go

type RestoreJobManager struct {
    db         *db.Database
    restorer   *Restorer
    progress   *RestoreProgressBroker
    
    mu          sync.Mutex
    activeJobID int64
    cancelFuncs map[int64]context.CancelFunc
}

func NewRestoreJobManager(db *db.Database, restorer *Restorer, progress *RestoreProgressBroker) *RestoreJobManager

// CreateJob 创建恢复任务，计算文件列表，返回 job_id
func (m *RestoreJobManager) CreateJob(req *models.RestoreRequest) (*models.RestoreJobRecord, error)

// StartJob 异步启动恢复任务
func (m *RestoreJobManager) StartJob(jobID int64) error

// CancelJob 取消指定任务
func (m *RestoreJobManager) CancelJob(jobID int64) error

// GetJob 获取任务详情
func (m *RestoreJobManager) GetJob(jobID int64) (*models.RestoreJobRecord, error)

// ListJobs 分页列出任务
func (m *RestoreJobManager) ListJobs(page, size int, status string) ([]*models.RestoreJobRecord, int64, error)
```

### 4.2 RestoreProgressBroker（新增）

与现有 `ProgressBroker` 模式一致，但独立维护恢复事件的订阅/发布。

```go
// internal/backup/restore_progress.go

type RestoreProgressBroker struct {
    mu         sync.Mutex
    clients    map[restoreProgressClient]struct{}
    history    []models.RestoreProgressEvent
    historyMax int
}

func NewRestoreProgressBroker() *RestoreProgressBroker
func (b *RestoreProgressBroker) Subscribe() (chan, []models.RestoreProgressEvent, func())
func (b *RestoreProgressBroker) Publish(event models.RestoreProgressEvent)
func (b *RestoreProgressBroker) PublishPhase(jobID int64, phase, message string)
func (b *RestoreProgressBroker) PublishProgress(jobID int64, current, total int, percent float64, restoredSize, totalSize int64)
func (b *RestoreProgressBroker) PublishFile(jobID int64, filePath string, fileSize int64, message string)
func (b *RestoreProgressBroker) PublishLog(jobID int64, level, message, detail string)
func (b *RestoreProgressBroker) ClearHistory()
```

### 4.3 RestoreJobRepository（新增）

```go
// internal/db/restore_job_repo.go

type RestoreJobRepository struct {
    db *sql.DB
}

func NewRestoreJobRepository(db *sql.DB) *RestoreJobRepository
func (r *RestoreJobRepository) Create(job *models.RestoreJobRecord) (int64, error)
func (r *RestoreJobRepository) GetByID(id int64) (*models.RestoreJobRecord, error)
func (r *RestoreJobRepository) UpdateStatus(id int64, status models.RestoreJobStatus, errorMsg string) error
func (r *RestoreJobRepository) UpdateProgress(id int64, restoredFiles int, restoredSize int64, failedFiles []string) error
func (r *RestoreJobRepository) UpdateCompleted(id int64, restoredFiles int, totalSize int64, elapsedMs int64, failedFiles []string) error
func (r *RestoreJobRepository) List(limit, offset int, status string) ([]*models.RestoreJobRecord, int64, error)
func (r *RestoreJobRepository) GetRunning() (*models.RestoreJobRecord, error)
func (r *RestoreJobRepository) CleanupStaleRunning() (int64, error)
```

### 4.4 对现有 Restorer 的增强

在 `internal/backup/restore.go` 的 `Restorer` 中新增：

1. **目录递归恢复支持**：`resolveFiles` 扩展支持 `IsDir` 检测，目录路径自动递归展开为所有子文件
2. **冲突处理策略**：`restoreFile` 中增加 `conflictStrategy` 参数（overwrite/skip/rename）
3. **进度回调注入**：`Restore` 方法接受 `onFileProgress` 回调，由 `RestoreJobManager` 注入并转发到 `RestoreProgressBroker`
4. **输出目录安全限制**：新增 `validateOutputDir` 函数，校验输出目录在配置的允许恢复根目录范围内

### 4.5 对现有 Engine 的修改

在 `NewEngine` 中增加 `RestoreJobManager` 依赖注入。在 `StartBackup` 中增加检查：如果当前有恢复任务在运行，则返回冲突错误（反之亦然）。确保备份和恢复互斥执行。

## 5. 前端设计

### 5.1 路由扩展

在 `App.tsx` 新增 `/restore` 路由：

```tsx
import { Restore } from '@/pages/Restore';

<Route path="/restore" element={<Restore />} />
```

### 5.2 导航扩展

在 `Sidebar.tsx` 的 `navItems` 数组中新增：

```tsx
import { RotateCcw } from 'lucide-react';

const navItems = [
  { to: '/', icon: LayoutDashboard, label: '全览' },
  { to: '/content', icon: FolderOpen, label: '内容选择' },
  { to: '/restore', icon: RotateCcw, label: '恢复' },  // 新增
  { to: '/strategy', icon: Settings, label: '策略设置' },
  { to: '/logs', icon: ScrollText, label: '日志' },
  { to: '/reconcile', icon: ShieldCheck, label: '系统对账' },
];
```

### 5.3 恢复页面（Restore.tsx）布局

页面分为左右两栏布局（参照 Reconcile.tsx 的双面板风格）：

```
┌────────────────────────────────────────────────────────────┐
│  恢复文件                                                    │
│  ┌──────────────────────────┬──────────────────────────┐   │
│  │   文件列表（左栏）        │   恢复操作（右栏）        │   │
│  │                          │                          │   │
│  │  [搜索框 ______________] │  已选择: 15 个文件        │   │
│  │                          │  总大小: 1.2 GB           │   │
│  │  [备份版本: 全部 ▼]      │                          │   │
│  │                          │  目标目录                 │   │
│  │  ☑ /data/docs/a.pdf      │  [恢复到原路径 ○]        │   │
│  │  ☑ /data/docs/b.pdf      │  [恢复到指定目录 ○]      │   │
│  │  ☐ /data/photos/         │  [/tmp/restore ______]   │   │
│  │     ☑ 001.jpg            │                          │   │
│  │     ☑ 002.jpg            │  冲突处理                 │   │
│  │  ☐ /data/movies/         │  [跳过已存在文件 ○]      │   │
│  │     ☐ movie.mkv          │  [覆盖 ○]                │   │
│  │                          │  [重命名 ○]              │   │
│  │  [分页 < 1/8 >]          │                          │   │
│  │  [全选] [按目录选择]     │  [☐ 加急解冻]            │   │
│  │                          │                          │   │
│  │                          │  [开始恢复]              │   │
│  │                          │  [一键全盘恢复]          │   │
│  └──────────────────────────┴──────────────────────────┘   │
│                                                              │
│  ─────────────────────────────────────────────────────────  │
│  恢复历史                                                    │
│  ┌────────────────────────────────────────────────────────┐│
│  │  ID │ 状态    │ 文件数 │ 大小     │ 目标目录 │ 时间    ││
│  │  7  │ completed│  15   │ 1.2 GB   │ /tmp/... │ 2h前   ││
│  │  6  │ failed   │  0    │ 0 B      │ /data/   │ 1d前   ││
│  └────────────────────────────────────────────────────────┘│
└────────────────────────────────────────────────────────────┘
```

### 5.4 前端 API 模块（api.ts 新增）

```typescript
// utils/api.ts 新增

export interface RestoreJobRecord {
  id: number;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
  paths: string[];
  pattern: string;
  backup_id: number | null;
  output_dir: string;
  expedited: boolean;
  conflict_strategy: string;
  total_files: number;
  restored_files: number;
  failed_files: string[];
  total_size: number;
  restored_size: number;
  elapsed_ms: number;
  error_message: string;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export interface RestoreRequest {
  paths: string[];
  pattern?: string;
  backup_id?: number;
  output_dir: string;
  expedited?: boolean;
  conflict_strategy?: 'overwrite' | 'skip' | 'rename';
}

export interface RestorePreview {
  job_id: number;
  status: string;
  total_files: number;
  total_size: number;
}

export interface RestorableFile {
  id: number;
  path: string;
  size: number;
  mod_time: string;
  hash: string;
  status: string;
  backup_count: number;
  latest_backup_id: number;
  latest_backup_at: string;
  storage_key: string;
  compress_type: string;
  original_size: number;
  stored_size: number;
}

export interface RestoreProgressEvent {
  type: 'phase' | 'progress' | 'file' | 'log' | 'connected';
  job_id: number;
  phase?: string;
  phase_name?: string;
  current?: number;
  total?: number;
  percent?: number;
  message?: string;
  file_path?: string;
  file_size?: number;
  restored_size?: number;
  total_size?: number;
  level?: 'debug' | 'info' | 'warn' | 'error';
  detail?: string;
  timestamp: string;
}

export const restoreApi = {
  trigger: (data: RestoreRequest) =>
    request<RestorePreview>('/restore', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  listFiles: (params?: { dir_path?: string; backup_id?: number; search?: string; page?: number; size?: number }) =>
    paginatedRequest<RestorableFile>('/restore/files', params),
  listJobs: (params?: { page?: number; size?: number; status?: string }) =>
    paginatedRequest<RestoreJobRecord>('/restore/jobs', params),
  getJob: (id: number) => request<RestoreJobRecord>(`/restore/jobs/${id}`),
  cancelJob: (id: number) =>
    request<{ status: string }>(`/restore/jobs/${id}/cancel`, { method: 'POST' }),
};

export function createRestoreProgressStream(
  onEvent: (event: RestoreProgressEvent) => void,
  onError?: (error: Event) => void
): () => void {
  const es = new EventSource(`${API_BASE}/restore/progress/stream`);
  // ... (与 createProgressStream 类似实现)
}
```

### 5.5 恢复进度展示组件

新增 `RestoreProgressPanel` 组件，参照 Dashboard 页面的进度展示区域：

- 进度条（整体恢复百分比）
- 当前阶段文字 + 中文翻译
- 当前文件路径
- 已恢复/总数
- 已恢复大小/总大小
- 实时速度估算
- 日志消息滚动区域

## 6. 关键流程

### 6.1 创建恢复任务流程

```
1. 前端 POST /api/restore { paths, output_dir, ... }
2. Router.handleRestore → 解析请求参数
3. RestoreJobManager.CreateJob:
   a. resolveFiles(paths) 展开目录递归
   b. 统计 total_files, total_size
   c. 写入 restore_jobs 表（status=pending）
   d. 返回 job_id
4. Router 返回 202 Accepted { job_id, total_files, total_size }
5. RestoreJobManager.StartJob(job_id)（异步 goroutine）:
   a. 更新 status=running, started_at
   b. 发布 SSE phase 事件
   c. 调用 Restorer.Restore 执行恢复
   d. 每恢复一个文件发布 SSE progress 事件
   e. 完成后更新 status=completed/failed
   f. 发布 SSE completed/failed 事件
6. 前端收到 202 后开始监听 SSE /api/restore/progress/stream
```

### 6.2 取消恢复任务流程

```
1. 前端 POST /api/restore/jobs/{id}/cancel
2. RestoreJobManager.CancelJob:
   a. 调用该 job 的 context.CancelFunc
   b. Restorer.Restore 中的 worker 检测到 ctx.Done() 后退出
   c. 更新 restore_jobs status=cancelled
   d. 发布 SSE cancelled 事件
```

### 6.3 冲突处理流程（restoreFile 增强）

```
restoreFile 中 moveFile 步骤之前：
1. 计算目标路径 outputPath
2. 检查 outputPath 是否已存在
3. 根据 conflict_strategy:
   - "skip": 跳过该文件，记录到 failed_files
   - "overwrite": 删除已存在文件，继续移动
   - "rename": 生成新文件名（追加 .restored_YYYYMMDD_HHMMSS），继续移动
4. 如果目标路径超出允许的恢复根目录范围，返回安全错误
```

## 7. 安全设计

### 7.1 输出目录安全限制

新增配置项 `server.restore_base_dirs`（字符串数组），定义允许的恢复根目录。默认值为备份配置中的目录列表。`validateOutputDir` 函数确保恢复目标目录以其中一个根目录为前缀。

```yaml
server:
  restore_base_dirs:
    - /data
    - /tmp/restore
```

如果配置未设置，则使用当前备份目录列表作为默认白名单。

### 7.2 路径遍历防护

`validateOutputDir` 使用 `filepath.Clean` 和 `strings.HasPrefix` 进行规范化后校验，拒绝包含 `..` 的任意路径穿越。

### 7.3 备份/恢复互斥

- `Engine.StartBackup` 检查：如果有恢复任务 `status=running`，返回 409 Conflict
- `RestoreJobManager.StartJob` 检查：如果有备份 `status=running`，返回 409 Conflict
- 通过数据库中的状态字段实现持久化互斥（进程重启后仍然有效）

## 8. 依赖关系与影响范围

### 8.1 新增文件清单

| 文件路径 | 用途 |
|---------|------|
| `db/migrations/003_add_restore_jobs.sql` | 数据库迁移 |
| `internal/db/restore_job_repo.go` | 恢复任务仓库 |
| `internal/backup/restore_job.go` | 恢复任务管理器 |
| `internal/backup/restore_progress.go` | 恢复进度 SSE Broker |
| `internal/api/restore_handler.go` | 恢复 API Handler（扩展） |
| `nas-backup-frontend/src/pages/Restore.tsx` | 恢复页面 |
| `nas-backup-frontend/src/utils/api.ts` | API 模块扩展 |
| `nas-backup-frontend/src/hooks/useRestoreProgress.ts` | 恢复进度 Hook |
| `docs/RESTORE_GUIDE.md` | 恢复操作文档 |

### 8.2 修改文件清单

| 文件路径 | 修改内容 |
|---------|---------|
| `models/models.go` | 新增 RestoreJobRecord, RestoreProgressEvent, RestorableFile 等类型 |
| `db/db.go` | 新增 RestoreJobRepo 字段 |
| `router.go` | 新增恢复路由注册 |
| `backup/restore.go` | 增强 resolveFiles（目录递归）、冲突处理、进度回调 |
| `backup/engine.go` | 增加恢复互斥检查 |
| `config/config.go` | 新增 RestoreBaseDirs 配置 |
| `main.go` | 注入 RestoreJobManager 和 RestoreProgressBroker |
| `App.tsx` | 新增 /restore 路由 |
| `Sidebar.tsx` | 新增恢复导航项 |
| `api.ts` | 新增 restoreApi, createRestoreProgressStream 和相关类型 |

### 8.3 零影响声明

以下模块无需修改：
- `crypto/`, `compress/`, `storage/` — 核心加密/压缩/存储层不受影响
- `scanner/`, `dedup/` — 扫描和去重逻辑不受影响
- `scheduler/` — 定时备份调度不受影响
- `logger/` — 日志模块不受影响
- 前端 `Dashboard.tsx`, `Content.tsx`, `Strategy.tsx`, `Logs.tsx`, `Reconcile.tsx` — 现有页面不受影响
- Docker 部署配置 — 无需新增端口或卷映射

## 9. 技术选型说明

| 决策点 | 选型 | 理由 |
|--------|------|------|
| 恢复任务异步化 | goroutine + DB 状态持久化 | 与备份引擎模式一致，HTTP 请求立即返回，恢复在后台执行 |
| 进度推送 | 独立 SSE Broker | 与备份 SSE 完全隔离，避免事件混淆；复用现有 SSE 基础设施 |
| 文件浏览 | 平铺列表 + 搜索 + 路径筛选 | 复用现有 DataTable 和 Pagination 组件，开发效率高 |
| 目录递归恢复 | 服务端展开 | 前端只需发送目录路径，服务端自动递归查询所有子文件 |
| 冲突处理 | overwrite/skip/rename 三策略 | 覆盖最常见场景，默认 skip 最安全 |
| 输出目录限制 | 白名单根目录校验 | 防止路径遍历攻击，默认使用备份目录作为白名单 |
| 数据库重建策略 | 文档指导用户备份 nas-backup.db | 本期先走文档化方案，OSS 元数据重建作为后续增强 |

## 10. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 恢复大文件时 SSE 连接断开 | 用户丢失进度 | 前端自动重连 SSE，服务端保留历史事件缓冲 |
| 恢复期间进程崩溃 | 恢复任务状态不一致 | startup 时 CleanupStaleRunning 自动清理异常任务 |
| 恢复到系统目录造成破坏 | 安全风险 | 白名单校验 + 默认输出目录限制 |
| 目录递归恢复文件过多导致内存溢出 | 服务端 OOM | 流式查询 + 分页，避免一次性加载全部文件 |
| ColdArchive 解冻费用不可控 | 费用风险 | UI 明确提示解冻费用，默认不加急 |
