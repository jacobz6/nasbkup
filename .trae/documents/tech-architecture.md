## 1. 架构设计

```mermaid
flowchart TB
    subgraph "前端 (React + Vite)"
        A["React SPA"] --> B["React Router"]
        B --> C["全览页面<br/>(SSE 进度订阅)"]
        B --> D["内容选择页面"]
        B --> E["策略设置页面"]
        B --> F["日志页面"]
        B --> R["数据对账页面"]
    end
    subgraph "API 层"
        G["API Client (fetch)"]
        SSE["SSE EventSource<br/>(/api/backup/progress/stream)"]
    end
    subgraph "后端 (Go HTTP Server :8080)"
        H["/api/dashboard/*"]
        I["/api/backup/*<br/>(含 SSE 进度流)"]
        J["/api/content/*"]
        K["/api/strategy/*"]
        L["/api/logs/*"]
        M["/api/restore"]
        N["/api/gc"]
        RC["/api/reconcile"]
        SH["/api/storage/health"]
        PB["ProgressBroker<br/>(SSE 推送中心)"]
        REC["Reconciler<br/>(数据一致性对账)"]
    end
    subgraph "数据层"
        O["SQLite 数据库"]
        P["阿里云 OSS"]
    end
    C --> G
    C --> SSE
    D --> G
    E --> G
    F --> G
    R --> G
    G --> H
    G --> I
    G --> J
    G --> K
    G --> L
    G --> M
    G --> N
    G --> RC
    G --> SH
    SSE --> I
    I --> PB
    RC --> REC
    H --> O
    I --> O
    J --> O
    K --> O
    L --> O
    RC --> O
    I --> P
    M --> P
    REC --> P
    SH --> P
```

## 2. 技术说明

- **前端框架**：React 18 + TypeScript + Vite
- **初始化工具**：vite-init (react-ts 模板)
- **样式方案**：Tailwind CSS 3
- **状态管理**：Zustand
- **路由**：React Router DOM v7
- **图标**：Lucide React
- **HTTP 请求**：原生 fetch API（封装为统一 API Client）
- **实时通信**：Server-Sent Events (SSE) 用于备份进度推送，自动重连
- **后端**：Go HTTP 服务（端口 8080）
- **数据库**：SQLite (WAL 模式)
- **云存储**：阿里云 OSS（通过 rclone + OSS SDK）

## 3. 路由定义

| 路由 | 用途 |
|------|------|
| `/` | 全览页面 - 备份状态仪表盘（含 SSE 实时进度） |
| `/content` | 内容选择页面 - 目录与排除规则管理 |
| `/strategy` | 策略设置页面 - 调度/压缩/上传/保留/加密配置 |
| `/logs` | 日志页面 - 日志查看与过滤 |
| `/reconcile` | 数据对账页面 - 一致性检查与修复 |

## 4. API 定义

### 4.1 统一响应类型

```typescript
interface APIResponse<T> {
  success: boolean;
  data?: T;
  error?: string;
}

interface PaginatedResponse<T> {
  success: boolean;
  data: T[];
  total: number;
  page: number;
  size: number;
}
```

### 4.2 仪表盘 API

```typescript
interface OSSInfo {
  storage_class: string;
  endpoint: string;
  bucket: string;
  remote_name: string;
  region: string;
}

interface DashboardStats {
  total_files: number;
  total_size: number;
  oss_storage_used: number;
  oss_quota_bytes: number;
  backup_count: number;
  unique_hash_count: number;
  needs_reconcile: boolean;
  oss_info: OSSInfo;
  last_backup_time: string | null;
  last_backup_status: string | null;
  next_backup_time: string | null;
  active_backup_running: boolean;
}

interface BackupRecord {
  id: number;
  type: "full" | "incremental" | "auto";
  status: "pending" | "running" | "completed" | "failed" | "cancelled";
  base_backup_id: number | null;
  total_files: number;
  total_size: number;
  uploaded_size: number;
  skipped_dedup: number;
  skipped_inc: number;
  compress_saved: number;
  started_at: string | null;
  completed_at: string | null;
  error_message: string | null;
  created_at: string;
}

// GET /api/dashboard/stats → APIResponse<DashboardStats>
// GET /api/dashboard/history?page=1&size=20 → PaginatedResponse<BackupRecord>
```

### 4.3 备份操作 API（含 SSE）

```typescript
interface BackupTriggerRequest {
  type: "full" | "incremental" | "auto";
}

interface BackupStatus {
  is_running: boolean;
  running_backup: BackupRecord | null;
}

type BackupPhase = "scanning" | "hashing" | "deduplicating" | "uploading" | "finalizing" | "completed" | "failed" | "cancelled";

interface ProgressEvent {
  type: "connected" | "phase" | "progress" | "file" | "log";
  backup_id: number;
  phase: BackupPhase;
  phase_name: string;
  current: number;
  total: number;
  percent: number;
  message: string;
  detail: string;
  level: "debug" | "info" | "warn" | "error";
  file_path: string;
  file_size: number;
  timestamp: string;
}

// POST /api/backup/trigger → APIResponse<{backup_id: number, status: string}> (202 Accepted)
// POST /api/backup/cancel?backup_id=xxx → APIResponse<{status: string}>
// GET /api/backup/status → APIResponse<BackupStatus>
// GET /api/backup/progress/stream → text/event-stream (SSE)
```

### 4.4 内容管理 API

```typescript
interface BackupDirectory {
  id: number;
  path: string;
  recursive: boolean;
  enabled: boolean;
  description: string;
}

interface ExclusionRule {
  id: number;
  pattern: string;
  rule_type: "extension" | "directory" | "pattern" | "size_exceed";
  enabled: boolean;
}

interface FSEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mod_time: string;
  in_backup: boolean;
  partial_backup: boolean;
  has_update: boolean;
  will_backup: boolean;
}

interface FSBrowseResult {
  path: string;
  parent_path: string;
  entries: FSEntry[];
}

// GET /api/fs/browse?path= → APIResponse<FSBrowseResult>
// GET /api/content/directories → APIResponse<BackupDirectory[]>
// POST /api/content/directories → APIResponse<BackupDirectory>
// PATCH /api/content/directories/:id → APIResponse<BackupDirectory>（部分更新）
// DELETE /api/content/directories/:id → APIResponse<{status: string}>
// GET /api/content/exclusions → APIResponse<ExclusionRule[]>
// POST /api/content/exclusions → APIResponse<ExclusionRule>
// PUT /api/content/exclusions/:id → APIResponse<ExclusionRule>
// DELETE /api/content/exclusions/:id → APIResponse<{status: string}>
```

### 4.5 策略配置 API

```typescript
interface ScheduleConfig {
  enabled: boolean;
  cron_expr: string;
  timezone: string;
}

interface CompressionConfig {
  enabled: boolean;
  algorithm: string;
  level: number;
  skip_types: string[];
}

interface UploadConfig {
  storage_class: "Standard" | "IA" | "Archive" | "ColdArchive";
  concurrency: number;
  chunk_size_mb: number;
  retry_count: number;
  retry_delay_sec: number;
}

interface RetentionConfig {
  version_keep_count: number;
  orphan_grace_days: number;
  full_reset_interval: number;
  deleted_retention_days: number;
}

interface EncryptionConfig {
  algorithm: string;
  key_file_path: string;
}

// GET/PUT /api/strategy/schedule → APIResponse<ScheduleConfig>
// GET/PUT /api/strategy/compression → APIResponse<CompressionConfig>
// GET/PUT /api/strategy/upload → APIResponse<UploadConfig>
// GET/PUT /api/strategy/retention → APIResponse<RetentionConfig>
// GET/PUT /api/strategy/encryption → APIResponse<EncryptionConfig>
```

### 4.6 日志 API

```typescript
interface LogRecord {
  id: number;
  backup_id: number | null;
  level: "debug" | "info" | "warn" | "error";
  message: string;
  detail: string;
  created_at: string;
}

interface LogQueryParams {
  backup_id?: number;
  level?: string;
  search?: string;
  start_time?: string;
  end_time?: string;
  page?: number;
  page_size?: number;
}

// GET /api/logs?... → PaginatedResponse<LogRecord>
// GET /api/logs/:id → APIResponse<LogRecord>
```

### 4.7 恢复、GC、维护 API

```typescript
interface RestoreRequest {
  paths?: string[];
  pattern?: string;
  backup_id?: number;
  output_dir: string;
  expedited?: boolean;
}

interface RestoreResult {
  total_files: number;
  restored_files: number;
  failed_files: string[];
  total_size: number;
  elapsed_ms: number;
}

interface RefCountMismatch {
  hash: string;
  stored_in_db: number;
  actual_active: number;
}

interface ReconcileReport {
  started_at: string;
  finished_at: string;
  duration_ms: number;
  dry_run: boolean;
  oss_only_orphans: string[];
  dangling_hash_indexes_ref_zero: string[];
  dangling_hash_indexes_ref_non_zero: string[];
  orphan_backup_files: string[];
  backup_files_missing_hash_index_but_in_oss: string[];
  ref_count_mismatches: RefCountMismatch[];
  failed_backups_with_files: number[];
  completed_backups_no_files: number[];
  applied_fixes: string[];
  skipped_fixes: string[];
  errors: string[];
}

interface StorageHealthResult {
  ok: boolean;
  latency_ms: number;
}

// POST /api/restore → APIResponse<RestoreResult>（4h 独立超时）
// POST /api/gc → APIResponse<{status: string}>（异步）
// POST /api/reconcile?dry_run=true → APIResponse<ReconcileReport>（备份运行中返回 409）
// GET /api/storage/health → APIResponse<StorageHealthResult> (200/503)
```

## 5. 后端架构图

```mermaid
flowchart LR
    A["API Handlers<br/>(含 logging/cors 中间件)"] --> B["业务服务层"]
    B --> C["数据仓库层<br/>(Repository 模式)"]
    C --> D["SQLite 数据库"]
    B --> E["备份引擎"]
    E --> PB["ProgressBroker<br/>(SSE 推送)"]
    E --> F["扫描器"]
    E --> G["去重器"]
    E --> H["压缩器"]
    E --> I["加密器"]
    E --> J["存储管理器"]
    E --> REC["Reconciler<br/>(一致性对账)"]
    J --> K["阿里云 OSS"]
    B --> L["调度器"]
    API["SSE Handler"] --> PB
```

**关键特性**：
- **并发安全**：互斥锁 + DB 双重检查防止并发备份
- **崩溃恢复**：启动时 CleanupStaleRunning 清理残留状态
- **全量重建映射**：全量备份重处理 Unchanged 文件修复 hash_index
- **同批次去重**：pendingByHash 缓存减少 DB 查询

## 6. 数据模型

### 6.1 数据模型定义

```mermaid
erDiagram
    "backups" {
        INTEGER id PK
        TEXT type "full/incremental/auto"
        TEXT status "pending/running/completed/failed/cancelled"
        INTEGER base_backup_id
        INTEGER total_files
        INTEGER total_size
        INTEGER uploaded_size
        INTEGER skipped_dedup
        INTEGER skipped_inc
        INTEGER compress_saved
        TEXT started_at
        TEXT completed_at
        TEXT error_message
        TEXT created_at
    }
    "files" {
        INTEGER id PK
        TEXT path UK
        INTEGER size
        TEXT mod_time
        TEXT hash
        TEXT status "active/deleted"
        INTEGER backup_id
        INTEGER inode
        TEXT created_at
        TEXT updated_at
    }
    "backup_files" {
        INTEGER backup_id FK
        INTEGER file_id FK
        TEXT storage_key
        TEXT encrypted_iv
        TEXT auth_tag
        TEXT compress_type "zstd/none"
        INTEGER original_size
        INTEGER stored_size
    }
    "hash_index" {
        INTEGER id PK
        TEXT hash UK
        INTEGER file_size
        TEXT storage_key
        INTEGER ref_count
        TEXT created_at
    }
    "backup_logs" {
        INTEGER id PK
        INTEGER backup_id FK
        TEXT level
        TEXT message
        TEXT detail
        TEXT created_at
    }
    "config_kv" {
        TEXT key PK
        TEXT value
        TEXT updated_at
    }
    "backup_directories" {
        INTEGER id PK
        TEXT path UK
        INTEGER recursive
        INTEGER enabled
        TEXT description
    }
    "exclusion_rules" {
        INTEGER id PK
        TEXT pattern UK
        TEXT rule_type
        INTEGER enabled
    }
    "backups" ||--o{ "backup_files" : "has"
    "files" ||--o{ "backup_files" : "has"
    "backups" ||--o{ "backup_logs" : "has"
```

### 6.2 前端项目结构

```
src/
├── components/
│   ├── layout/
│   │   ├── Sidebar.tsx          # 左侧导航栏（5 个导航项，含对账）
│   │   └── AppLayout.tsx        # 应用布局框架
│   ├── ui/
│   │   ├── StatusBadge.tsx      # 状态标签
│   │   ├── GaugeChart.tsx       # 环形仪表图
│   │   ├── StatCard.tsx         # 统计卡片
│   │   ├── DataTable.tsx        # 通用数据表格
│   │   ├── SlidePanel.tsx       # 右侧滑出面板
│   │   ├── ConfirmDialog.tsx    # 确认对话框
│   │   └── Pagination.tsx       # 分页器
│   └── shared/
│       ├── LoadingSkeleton.tsx  # 加载骨架屏
│       └── EmptyState.tsx       # 空状态
├── pages/
│   ├── Dashboard.tsx            # 全览页面（SSE 进度条 + 对账告警）
│   ├── Content.tsx              # 内容选择页面（PartialBackup 状态）
│   ├── Strategy.tsx             # 策略设置页面
│   ├── Logs.tsx                 # 日志页面
│   └── Reconcile.tsx            # 数据对账页面（DryRun/修复）
├── hooks/
│   ├── useApi.ts                # API 请求 Hook
│   ├── usePolling.ts            # 轮询 Hook
│   ├── useBackupProgress.ts     # SSE 进度订阅 Hook
│   └── useConfirm.ts            # 全局确认对话框 Hook
├── utils/
│   ├── api.ts                   # API Client 封装
│   ├── format.ts                # 格式化工具（文件大小、时间等）
│   └── constants.ts             # 常量定义（含 BACKUP_PHASE_MAP）
├── store/
│   └── useAppStore.ts           # Zustand 全局状态
├── lib/
│   └── utils.ts                 # cn() 类名合并工具
├── App.tsx                      # 根组件 + 路由
├── main.tsx                     # 入口
└── index.css                    # 全局样式 + Tailwind
```
