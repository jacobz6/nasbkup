# NAS Backup System - Code Wiki

> 本文档为 NAS 备份系统的完整代码百科，涵盖项目架构、模块职责、关键类与函数、依赖关系、API 详情、数据库模型、前端页面及运行方式。旨在帮助 AI 和开发者快速理解整个项目框架，避免优化过程中偏离项目主旨及核心。

---

## 目录

1. [项目概述](#1-项目概述)
2. [项目整体架构](#2-项目整体架构)
3. [后端架构详解](#3-后端架构详解)
   - 3.1 [入口与启动流程](#31-入口与启动流程)
   - 3.2 [配置系统 (config)](#32-配置系统-config)
   - 3.3 [数据模型 (models)](#33-数据模型-models)
   - 3.4 [数据库层 (db)](#34-数据库层-db)
   - 3.5 [API 路由与处理器 (api)](#35-api-路由与处理器-api)
   - 3.6 [备份引擎 (backup)](#36-备份引擎-backup)（含 SSE 进度推送 + 数据一致性对账）
   - 3.7 [文件扫描器 (scanner)](#37-文件扫描器-scanner)
   - 3.8 [去重模块 (dedup)](#38-去重模块-dedup)
   - 3.9 [压缩模块 (compress)](#39-压缩模块-compress)
   - 3.10 [加密模块 (crypto)](#310-加密模块-crypto)
   - 3.11 [存储管理 (storage)](#311-存储管理-storage)
   - 3.12 [调度器 (scheduler)](#312-调度器-scheduler)
   - 3.13 [日志系统 (logger)](#313-日志系统-logger)
4. [前端架构详解](#4-前端架构详解)
   - 4.1 [技术栈与构建配置](#41-技术栈与构建配置)
   - 4.2 [路由与页面结构](#42-路由与页面结构)
   - 4.3 [状态管理 (Zustand)](#43-状态管理-zustand)
   - 4.4 [API 客户端层](#44-api-客户端层)
   - 4.5 [页面详解](#45-页面详解)
   - 4.6 [组件详解](#46-组件详解)
   - 4.7 [Hooks](#47-hooks)
   - 4.8 [工具函数](#48-工具函数)
5. [数据库 Schema 详解](#5-数据库-schema-详解)
6. [API 接口完整参考](#6-api-接口完整参考)
7. [核心业务流程](#7-核心业务流程)
8. [依赖关系图](#8-依赖关系图)
9. [项目运行方式](#9-项目运行方式)
10. [辅助脚本](#10-辅助脚本)
11. [下云恢复指南 (restore-cli)](#11-下云恢复指南-restore-cli)
12. [前端页面名词解释（产品视角）](#12-前端页面名词解释产品视角)

---

## 1. 项目概述

**NAS Backup System** 是一个面向 NAS（网络附属存储）设备的自动化备份系统，核心功能是将本地 NAS 文件备份到阿里云 OSS 冷归档存储。系统采用 B/S 架构，后端为 Go HTTP 服务，前端为 React SPA。

### 核心设计理念

- **内容寻址去重**：基于 SHA-256 哈希的全局去重索引，相同内容的文件只存储一份
- **增量备份**：支持全量、增量和智能（auto）三种备份模式，增量备份仅处理变更文件，auto 模式基于时间间隔自动判断
- **端到端加密**：AES-256-GCM 加密，使用 HKDF 派生每文件数据加密密钥（DEK）
- **压缩优化**：zstd 压缩，智能跳过已压缩文件类型
- **冷归档友好**：支持 OSS ColdArchive 存储类，含解冻（thaw）流程
- **定时调度**：基于 cron 表达式的自动备份调度，自动判断全量/增量
- **实时进度反馈**：基于 SSE 的实时备份进度推送，含阶段、百分比、当前文件、实时日志
- **数据一致性保障**：三层数据一致性对账（OSS ↔ hash_index ↔ backup_files），支持 DryRun 审计和自动修复
- **崩溃恢复**：启动时自动清理残留状态，全量备份重建映射，锁内双重检查防止竞态

### 技术栈

| 层级 | 技术 |
|------|------|
| 后端语言 | Go 1.25 |
| 数据库 | SQLite (WAL 模式) |
| 云存储 | 阿里云 OSS (via rclone + OSS SDK) |
| 加密 | AES-256-GCM + HKDF |
| 压缩 | zstd (外部二进制) |
| 调度 | robfig/cron/v3 |
| 前端框架 | React 18 + TypeScript |
| 构建工具 | Vite 6 |
| UI 框架 | Tailwind CSS 3 |
| 状态管理 | Zustand 5 |
| 路由 | react-router-dom 7 |
| 图标 | lucide-react |

---

## 2. 项目整体架构

```
nasbkup_system/
├── nas-backup-backend/          # Go 后端服务
│   ├── cmd/nas-backup/          # 程序入口
│   │   └── main.go
│   ├── cmd/restore-cli/         # 下云恢复 CLI 工具
│   ├── internal/                # 内部包（不可被外部导入）
│   │   ├── api/                 # HTTP API 层（路由 + 处理器）
│   │   ├── backup/              # 备份引擎 + 恢复器 + 进度推送 + 对账
│   │   │   ├── engine.go        # 备份引擎核心
│   │   │   ├── progress.go      # SSE 进度推送 Broker
│   │   │   ├── reconcile.go     # 数据一致性对账修复
│   │   │   └── restore.go       # 文件恢复器
│   │   ├── compress/            # zstd 压缩/解压
│   │   ├── config/              # 配置加载与验证
│   │   ├── crypto/              # AES-256-GCM 加密/解密
│   │   ├── db/                  # SQLite 数据访问层
│   │   │   └── migrations/      # SQL 迁移脚本
│   │   ├── dedup/               # 内容去重
│   │   ├── logger/              # 日志系统
│   │   ├── models/              # 领域模型定义
│   │   ├── scanner/             # 文件扫描与变更检测
│   │   ├── scheduler/           # 定时任务调度
│   │   └── storage/             # OSS 存储管理（rclone）
│   ├── scripts/                 # 辅助脚本
│   ├── config.yaml.example      # 配置文件示例
│   ├── go.mod / go.sum
│   └── README.md
├── nas-backup-frontend/         # React 前端
│   ├── src/
│   │   ├── components/          # UI 组件
│   │   │   ├── layout/          # 布局组件
│   │   │   ├── shared/          # 共享组件
│   │   │   └── ui/              # 基础 UI 组件
│   │   ├── hooks/               # 自定义 Hooks
│   │   ├── pages/               # 页面组件
│   │   │   ├── Dashboard.tsx    # 全览页面（含进度条 + 实时日志）
│   │   │   ├── Content.tsx      # 内容选择
│   │   │   ├── Strategy.tsx     # 策略设置
│   │   │   ├── Logs.tsx         # 日志查看
│   │   │   └── Reconcile.tsx    # 系统对账
│   │   ├── store/               # Zustand 状态
│   │   ├── utils/               # 工具函数（api.ts 含 SSE 客户端）
│   │   ├── lib/                 # 通用库
│   │   ├── App.tsx              # 路由配置
│   │   ├── main.tsx             # 入口
│   │   └── index.css            # 全局样式
│   ├── package.json
│   ├── vite.config.ts
│   └── tailwind.config.js
├── docker/                      # Docker 部署配置（含 Nginx SSE 配置）
│   ├── nginx.conf
│   └── config.docker.yaml
├── DEPLOYMENT.md                # 生产部署指南
├── DEPLOYMENT_testenv.md        # 测试环境部署指南
└── nas_file_generator.py        # 测试数据生成器
```

### 架构分层

```
┌─────────────────────────────────────────────────────┐
│               Frontend (React SPA)                  │
│  Dashboard / Content / Strategy / Logs / Reconcile  │
│         (含 SSE EventSource 进度订阅)                │
└────────────────────────┬────────────────────────────┘
                         │ HTTP API (JSON) + SSE 实时流
┌────────────────────────▼────────────────────────────┐
│                API Layer (api/)                     │
│  Router → Handlers → JSON Response                  │
│  loggingMiddleware (4xx=WARN, 5xx=ERROR)            │
│  SSE 端点: /api/backup/progress/stream              │
└────────────────────────┬────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────┐
│              Business Logic Layer                   │
│  Engine / Restorer / Scheduler / ProgressBroker     │
│  Scanner / Dedup / Compress / Crypto / Reconciler   │
└──────────┬───────────────────────┬──────────────────┘
           │                       │
┌──────────▼──────┐        ┌───────▼──────────────┐
│   Database      │        │   Storage            │
│   (SQLite)      │        │   (rclone → OSS)     │
│   db/ repos     │        │   storage/           │
│  启动时清理      │        │  Health Check        │
│  残留 running   │        │                      │
└─────────────────┘        └──────────────────────┘
```

---

## 3. 后端架构详解

### 3.1 入口与启动流程

**文件**: `cmd/nas-backup/main.go`

启动流程按以下顺序执行：

1. **解析命令行参数**：`-config` 指定配置文件路径（默认 `config.yaml`）
2. **加载配置**：`config.Load()` 从 YAML 加载并验证
3. **初始化日志**：`logger.Init()` 设置日志级别、文件输出、轮转
4. **创建数据目录**：`cfg.EnsureDataDirs()` 确保所有必要目录存在
5. **打开数据库**：`db.Open()` 打开 SQLite 并运行迁移
6. **清理崩溃残留状态**：`db.BackupRepo.CleanupStaleRunning()` 将上一次进程崩溃遗留的 `running`/`pending` 备份记录标记为 failed，避免新进程无法启动备份
7. **初始化组件**：
   - `scanner.NewScanner(fileRepo, configRepo)` — 文件扫描器
   - `compress.NewCompressor(compressionConfig)` — 压缩器
   - `crypto.NewEncryptor(keyFilePath)` — 加密器
   - `storage.NewStorageManager(cfg)` — 存储管理器
   - `stor.EnsureRcloneConfig()` — 确保 rclone 配置存在
   - `dedup.NewDeduplicator(hashRepo, stor, concurrency)` — 去重器（注入 storage 用于 OSS 存在性校验，防止崩溃后引用丢失对象）
   - `backup.NewProgressBroker()` — SSE 进度推送 Broker
   - `backup.NewEngine(db, sc, dd, comp, enc, stor, cfg, pb)` — 备份引擎（注入 ProgressBroker）
   - `backup.NewRestorer(db, enc, comp, stor, cfg)` — 恢复器
   - `scheduler.NewScheduler(engine, db, cfg)` — 调度器
8. **启动调度器**（如果配置启用）：`sched.Start()`
9. **创建 HTTP 路由**：`api.NewRouter()` + `router.Setup()`（注册 SSE 端点、Reconcile、Storage Health 等路由）
10. **启动 HTTP 服务器**：监听 `host:port`。注意：SSE 端点通过 `http.NewResponseController` 动态禁用 `WriteTimeout` 以支持长连接
11. **优雅关闭**：捕获 SIGINT/SIGTERM，30 秒超时优雅关闭

### 3.2 配置系统 (config)

**文件**: `internal/config/config.go`

#### 配置结构体层级

```
AppConfig
├── ServerConfig          # HTTP 服务器配置
│   ├── Host              # 监听地址 (默认 "0.0.0.0")
│   ├── Port              # 监听端口 (默认 8080)
│   ├── ReadTimeout       # 读超时秒数 (默认 30)
│   └── WriteTimeout      # 写超时秒数 (默认 60, SSE端点动态禁用)
├── DatabaseConfig        # 数据库配置
│   └── Path              # SQLite 文件路径 (默认 "./data/nas-backup.db")
├── BackupConfig          # 备份配置
│   ├── Directories       # []DirectoryConfig — 备份目录列表
│   ├── Exclusions        # []ExclusionConfig — 排除规则列表
│   ├── SizeLimit         # SizeLimitConfig — 文件大小限制
│   ├── Schedule          # ScheduleConfig — 调度配置
│   ├── Compression       # CompressionConfig — 压缩配置
│   ├── Retention         # RetentionConfig — 保留策略
│   └── Encryption        # EncryptionConfig — 加密配置
├── StorageConfig         # OSS 批量操作并发配置
│   └── Concurrency       # 批量 OSS 操作 worker 数 (默认 8)
├── ReconcileConfig       # 数据一致性对账配置
│   ├── DryRun            # 默认 dry-run 模式 (默认 true, 不自动修复)
│   └── OSSListPrefix     # OSS 对象列表前缀 (默认 "data/")
├── OSSConfig             # 阿里云 OSS 配置
│   ├── Endpoint          # OSS 端点
│   ├── Bucket            # 存储桶名
│   ├── AccessKeyID       # AK
│   ├── AccessKeySecret   # SK
│   ├── StorageClass      # 存储类型 (ColdArchive/Archive)
│   └── Region            # 区域
├── RcloneConfig          # rclone 配置
│   ├── BinaryPath        # rclone 二进制路径
│   ├── ConfigPath        # rclone 配置文件路径
│   └── RemoteName        # 远程名称 (默认 "oss-crypt")
└── LoggingConfig         # 日志配置
    ├── Level             # 日志级别
    ├── FilePath          # 日志文件路径
    ├── MaxSize           # 最大文件大小 MB
    └── MaxFiles          # 最大文件数
```

#### 关键函数

| 函数 | 签名 | 说明 |
|------|------|------|
| `DefaultConfig` | `() *AppConfig` | 返回含合理默认值的配置 |
| `Load` | `(path string) (*AppConfig, error)` | 从 YAML 文件加载配置，文件不存在则返回默认配置 |
| `Validate` | `(c *AppConfig) error` | 验证配置一致性和正确性 |
| `EnsureDataDirs` | `(c *AppConfig) error` | 创建所有必要数据目录 |
| `ToModelsScheduleConfig` | `(c *AppConfig) models.ScheduleConfig` | 转换为 models 层调度配置 |
| `ToModelsCompressionConfig` | `(c *AppConfig) models.CompressionConfig` | 转换为 models 层压缩配置 |
| `ToModelsRetentionConfig` | `(c *AppConfig) models.RetentionConfig` | 转换为 models 层保留配置 |
| `ToModelsUploadConfig` | `(c *AppConfig) models.UploadConfig` | 转换为 models 层上传配置 |
| `ToModelsEncryptionConfig` | `(c *AppConfig) models.EncryptionConfig` | 转换为 models 层加密配置 |
| `Now` | `(c *AppConfig) time.Time` | 返回配置时区的当前时间 |

#### 默认排除规则

| 模式 | 类型 | 说明 |
|------|------|------|
| `*.tmp` | extension | 临时文件 |
| `*.log` | extension | 日志文件 |
| `node_modules` | directory | Node.js 依赖 |
| `.git` | directory | Git 仓库 |
| `__pycache__` | directory | Python 缓存 |
| `.DS_Store` | pattern | macOS 系统文件 |
| `Thumbs.db` | pattern | Windows 缩略图缓存 |

#### 默认跳过压缩的文件类型

`.mp4`, `.mkv`, `.mov`, `.avi`, `.wmv`, `.jpg`, `.jpeg`, `.png`, `.webp`, `.gif`, `.mp3`, `.flac`, `.aac`, `.ogg`, `.zip`, `.7z`, `.gz`, `.rar`, `.bz2`, `.xz`, `.docx`, `.xlsx`, `.pptx`, `.pdf`

### 3.3 数据模型 (models)

**文件**: `internal/models/models.go`

所有跨层数据类型定义，作为数据库、业务逻辑和 API 之间的契约。

#### 文件追踪

| 类型 | 字段 | 说明 |
|------|------|------|
| `FileStatus` | `active` / `deleted` / `modified` | 文件生命周期状态 |
| `FileRecord` | ID, Path, Size, ModTime, Hash, Status, BackupID, CreatedAt, UpdatedAt | 文件索引记录 |

#### 备份会话

| 类型 | 字段 | 说明 |
|------|------|------|
| `BackupType` | `full` / `incremental` / `auto` | 备份类型（auto 由系统自动判断全量/增量） |
| `BackupStatus` | `pending` / `running` / `completed` / `failed` / `cancelled` | 备份状态 |
| `BackupRecord` | ID, Type, Status, BaseBackupID, TotalFiles, TotalSize, UploadedSize, SkippedByDedup, SkippedByInc, CompressSaved, StartedAt, CompletedAt, ErrorMessage, CreatedAt | 备份会话记录 |

#### 备份-文件关联

| 类型 | 字段 | 说明 |
|------|------|------|
| `BackupFileRecord` | BackupID, FileID, StorageKey, EncryptedIV, AuthTag, CompressType, OriginalSize, StoredSize | 每文件加密和存储元数据 |

#### 哈希索引（去重）

| 类型 | 字段 | 说明 |
|------|------|------|
| `HashIndexRecord` | ID, Hash, FileSize, StorageKey, RefCount, CreatedAt | 内容哈希到物理存储位置的映射 |

#### 日志

| 类型 | 字段 | 说明 |
|------|------|------|
| `LogLevel` | `debug` / `info` / `warn` / `error` | 日志级别 |
| `LogRecord` | ID, BackupID, Level, Message, Detail, CreatedAt | 备份操作日志 |
| `LogFilter` | BackupID, Level, Search, StartTime, EndTime, Page, PageSize | 日志查询过滤 |
| `LogListResult` | Items, Total, Page, PageSize | 分页日志结果 |

#### 配置

| 类型 | 字段 | 说明 |
|------|------|------|
| `ConfigRecord` | Key, Value, UpdatedAt | KV 配置条目 |
| `BackupDirectory` | ID, Path, Recursive, Enabled, Description | 备份目录 |
| `ExclusionRule` | ID, Pattern, RuleType, Enabled | 排除规则 |
| `FileSizeLimit` | MaxFileSize, MinFileSize | 文件大小限制 |
| `ContentConfig` | Directories, Exclusions, SizeLimit | 内容选择配置 |

#### 策略配置

| 类型 | 字段 | 说明 |
|------|------|------|
| `ScheduleConfig` | Enabled, CronExpr, Timezone | 调度配置 |
| `CompressionConfig` | Enabled, Algorithm, Level, SkipTypes | 压缩配置 |
| `UploadConfig` | StorageClass, MaxConcurrency, ChunkSizeMB, RetryCount, RetryDelaySec | 上传配置 |
| `RetentionConfig` | VersionKeepCount, OrphanGraceDays, FullResetInterval, KeepDeletedDays | 保留策略 |
| `EncryptionConfig` | Algorithm, KeyFilePath | 加密配置 |
| `StrategyConfig` | Schedule, Compression, Upload, Retention, Encryption | 完整策略配置 |

#### API 请求/响应

| 类型 | 字段 | 说明 |
|------|------|------|
| `OSSInfo` | StorageClass, Endpoint, Bucket, RemoteName, Region | OSS 配置信息（仪表板显示） |
| `DashboardStats` | TotalFiles, TotalSize, OSSStorageUsed, OSSQuotaBytes, BackupCount, UniqueHashCount, NeedsReconcile, OSSInfo, LastBackupTime, LastBackupStatus, NextBackupTime, ActiveBackupRunning | 仪表板统计 |
| `BackupTriggerRequest` | Type | 触发备份请求（支持 full/incremental/auto） |
| `RestoreRequest` | Paths, Pattern, BackupID, OutputDir, Expedited | 恢复请求 |
| `RestoreResult` | TotalFiles, RestoredFiles, FailedFiles, TotalSize, ElapsedMs | 恢复结果 |
| `FSEntry` | Name, Path, IsDir, Size, ModTime, InBackup, PartialBackup, HasUpdate, WillBackup | 文件系统条目（PartialBackup 表示目录部分纳入备份） |
| `FSBrowseResult` | Path, ParentPath, Entries | 文件浏览结果 |
| `APIResponse` | Success, Data, Error | 标准 API 响应 |
| `PaginatedResponse` | Success, Data, Total, Page, Size | 分页 API 响应 |

#### SSE 进度事件

| 类型 | 字段 | 说明 |
|------|------|------|
| `BackupPhase` | `scanning`/`hashing`/`deduplicating`/`uploading`/`finalizing`/`completed`/`failed`/`cancelled` | 备份阶段 |
| `ProgressEvent` | Type, BackupID, Phase, PhaseName, Current, Total, Percent, Message, Detail, Level, FilePath, FileSize, Timestamp | SSE 进度事件 |

ProgressEvent.Type 取值：
- `connected` — 连接建立
- `phase` — 阶段切换
- `progress` — 进度百分比更新
- `file` — 当前处理文件
- `log` — 实时日志条目

#### 数据一致性对账报告

| 类型 | 字段 | 说明 |
|------|------|------|
| `RefCountMismatch` | Hash, StoredInDB, ActualActive | ref_count 不匹配项 |
| `BackupStatusFix` | BackupID, From, To, Reason | 备份状态修正建议 |
| `ReconcileReport` | StartedAt, FinishedAt, Duration, DryRun, OSSOnlyOrphans, DanglingHashIndexesRefZero, DanglingHashIndexesRefNonZero, OrphanBackupFiles, BackupFilesMissingHashIndexButInOSS, RefCountMismatches, FailedBackupsWithFiles, CompletedBackupsNoFiles, AppliedFixes, SkippedFixes, Errors | 对账报告 |

### 3.4 数据库层 (db)

**文件**: `internal/db/`

#### Database 结构体

```go
type Database struct {
    db         *sql.DB
    FileRepo   *FileRepository
    BackupRepo *BackupRepository
    HashRepo   *HashRepository
    LogRepo    *LogRepository
    ConfigRepo *ConfigRepository
}
```

#### 关键函数

| 函数 | 签名 | 说明 |
|------|------|------|
| `Open` | `(dbPath string) (*Database, error)` | 打开 SQLite 数据库，运行迁移，初始化所有 Repository |
| `Close` | `(d *Database) error` | 关闭数据库连接 |
| `DB` | `(d *Database) *sql.DB` | 返回原始 *sql.DB |
| `Now` | `() string` | 返回 UTC 时间 RFC3339 格式字符串 |

SQLite 连接参数：`?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1`

SQLite 性能优化 PRAGMA：
- `journal_mode=WAL`
- `synchronous=NORMAL`
- `cache_size=-64000` (64MB)
- `temp_store=MEMORY`
- `mmap_size=268435456` (256MB)

#### FileRepository

| 方法 | 签名 | 说明 |
|------|------|------|
| `Upsert` | `(path, size, modTime, hash, inode) (int64, error)` | 插入或更新文件记录，ON CONFLICT(path) 更新 |
| `GetByPath` | `(path) (*FileRecord, error)` | 按路径查询，未找到返回 nil |
| `GetByHash` | `(hash) ([]*FileRecord, error)` | 按哈希查询所有活跃文件 |
| `GetByID` | `(id) (*FileRecord, error)` | 按主键查询 |
| `ListByStatus` | `(status, limit, offset) ([]*FileRecord, error)` | 按状态分页查询（limit=0 返回所有） |
| `ListActiveByDirectory` | `(dirPath) ([]*FileRecord, error)` | 查询目录下所有活跃文件（LIKE dirPath/%） |
| `ListActiveByBackup` | `(backupID, dirPath) ([]*FileRecord, error)` | 查询指定备份中某目录下的文件 |
| `ListAllPaths` | `() ([]string, error)` | 列出所有文件路径 |
| `CountActiveByHash` | `() (map[string]int, error)` | 按 hash 统计活跃文件数（用于对账 ref_count 校验） |
| `MarkDeleted` | `(path) error` | 标记文件为已删除 |
| `MarkDeletedBatch` | `(paths) error` | 批量标记删除（事务） |
| `UpdateHash` | `(id, hash) error` | 更新文件哈希 |
| `CountByStatus` | `(status) (int64, error)` | 按状态计数 |
| `TotalSizeByStatus` | `(status) (int64, error)` | 按状态统计总大小 |

#### BackupRepository

| 方法 | 签名 | 说明 |
|------|------|------|
| `Create` | `(backupType, baseBackupID) (int64, error)` | 创建备份记录（状态 pending） |
| `UpdateStatus` | `(id, status, errorMsg) error` | 更新状态，running 设置 started_at，completed/failed/cancelled 设置 completed_at |
| `UpdateStats` | `(id, totalFiles, totalSize, uploadedSize, skippedDedup, skippedInc, compressSaved) error` | 更新备份统计 |
| `GetByID` | `(id) (*BackupRecord, error)` | 按主键查询 |
| `GetRunning` | `() (*BackupRecord, error)` | 获取当前 running 状态的备份记录 |
| `CleanupStaleRunning` | `() (int64, error)` | 启动时清理崩溃残留的 running/pending 记录，标记为 failed |
| `List` | `(limit, offset) ([]*BackupRecord, error)` | 分页查询，按 created_at DESC（返回总数用于分页） |
| `GetLatestCompleted` | `() (*BackupRecord, error)` | 获取最近完成的备份 |
| `GetLatestFull` | `() (*BackupRecord, error)` | 获取最近完成的全量备份 |
| `GetIncrementalsSinceFull` | `(fullBackupID) ([]*BackupRecord, error)` | 获取基于指定全量的增量备份 |
| `CountByStatus` | `(status) (int64, error)` | 按状态计数 |
| `Count` | `() (int64, error)` | 统计备份总数 |
| `ListFailedBackupsWithFiles` | `() ([]*BackupRecord, error)` | 列出有 backup_files 但状态为 failed 的备份（对账用） |
| `ListCompletedBackupsWithoutFiles` | `() ([]*BackupRecord, error)` | 列出状态为 completed 但无 backup_files 的备份（对账用） |
| `AddBackupFile` | `(bf) error` | 添加单条备份-文件关联 |
| `AddBackupFilesBatch` | `(bfs) error` | 批量添加备份-文件关联（事务） |
| `GetBackupFiles` | `(backupID) ([]*BackupFileRecord, error)` | 获取备份的所有文件关联 |
| `GetFileRestoreInfo` | `(fileID) (*BackupFileRecord, error)` | 获取文件最新恢复信息 |
| `GetBackupFileByFileID` | `(fileID) (*BackupFileRecord, error)` | 按文件 ID 获取备份文件记录 |
| `ListAllBackupFileStorageKeys` | `() (map[string]int, error)` | 获取所有 backup_files 的 storage_key 及引用计数（对账用） |
| `DeleteBackupFilesByStorageKey` | `(storageKey) (int64, error)` | 按 storage_key 删除孤立的 backup_files 记录 |
| `IsRunning` | `() (bool, error)` | 检查是否有运行中的备份（内存+DB双重检查） |

#### HashRepository

| 方法 | 签名 | 说明 |
|------|------|------|
| `GetByHash` | `(hash) (*HashIndexRecord, error)` | 按哈希查询 |
| `Upsert` | `(hash, fileSize, storageKey) (int64, error)` | 插入或递增 ref_count |
| `IncrementRef` | `(hash) error` | 递增引用计数 |
| `DecrementRef` | `(hash) (int, error)` | 递减引用计数（事务内，不低于0），返回新值 |
| `SetRefCount` | `(hash, count) error` | 直接设置引用计数（对账修复用） |
| `HasRefCountMismatches` | `() (bool, error)` | 快速检查是否存在 ref_count 不匹配（轻量级，不列 OSS） |
| `ListAll` | `() ([]*HashIndexRecord, error)` | 列出所有哈希索引记录（对账用） |
| `GetOrphans` | `() ([]*HashIndexRecord, error)` | 获取 ref_count=0 的孤儿记录 |
| `GetOrphansOlderThan` | `(days) ([]*HashIndexRecord, error)` | 获取超过指定天数的孤儿记录 |
| `DeleteByHash` | `(hash) error` | 按哈希删除 |
| `DeleteByStorageKey` | `(storageKey) error` | 按 storage_key 删除悬空索引 |
| `DeleteBatch` | `(hashes) error` | 批量删除（事务） |
| `CountUnique` | `() (int64, error)` | 统计唯一哈希数 |
| `TotalDedupSaved` | `() (int64, error)` | 计算去重节省的总字节数 |
| `GetAllStorageKeys` | `() ([]string, error)` | 获取所有存储键 |

#### LogRepository

| 方法 | 签名 | 说明 |
|------|------|------|
| `Insert` | `(backupID, level, message, detail) error` | 插入日志 |
| `List` | `(filter) ([]*LogRecord, int64, error)` | 过滤分页查询，返回记录和总数 |
| `GetByID` | `(id) (*LogRecord, error)` | 按主键查询 |
| `GetByBackupID` | `(backupID) ([]*LogRecord, error)` | 按备份 ID 查询 |
| `PurgeOlderThan` | `(days) (int64, error)` | 清理超过指定天数的日志 |
| `CountByLevel` | `(level) (int64, error)` | 按级别计数 |

#### ConfigRepository

| 方法 | 签名 | 说明 |
|------|------|------|
| `Get` | `(key) (string, error)` | 获取配置值，不存在返回空字符串 |
| `Set` | `(key, value) error` | Upsert 配置键值对 |
| `GetAll` | `() (map[string]string, error)` | 获取所有配置 |
| `ListDirectories` | `() ([]*BackupDirectory, error)` | 列出所有备份目录 |
| `AddDirectory` | `(path, recursive, enabled, description) (int64, error)` | 添加备份目录 |
| `UpdateDirectory` | `(id, path, recursive, enabled, description) error` | 更新备份目录 |
| `DeleteDirectory` | `(id) error` | 删除备份目录 |
| `GetEnabledDirectories` | `() ([]*BackupDirectory, error)` | 获取启用的备份目录 |
| `ListExclusionRules` | `() ([]*ExclusionRule, error)` | 列出所有排除规则 |
| `AddExclusionRule` | `(pattern, ruleType, enabled) (int64, error)` | 添加排除规则 |
| `UpdateExclusionRule` | `(id, pattern, ruleType, enabled) error` | 更新排除规则 |
| `DeleteExclusionRule` | `(id) error` | 删除排除规则 |
| `GetEnabledExclusionRules` | `() ([]*ExclusionRule, error)` | 获取启用的排除规则 |

### 3.5 API 路由与处理器 (api)

**文件**: `internal/api/`

#### Router 结构体

```go
type Router struct {
    engine    *backup.Engine
    restorer  *backup.Restorer
    scheduler *scheduler.Scheduler
    db        *db.Database
    config    *config.AppConfig
    mux       *http.ServeMux
}
```

#### 辅助函数与中间件

| 函数 | 签名 | 说明 |
|------|------|------|
| `jsonResponse` | `(w, data, status)` | 写入成功 JSON 响应 |
| `jsonPaginatedResponse` | `(w, data, total, page, size)` | 写入分页 JSON 响应 |
| `jsonError` | `(w, message, status)` | 写入错误 JSON 响应（4xx=WARN 日志，5xx=ERROR 日志） |
| `parsePagination` | `(req) (page, size)` | 从查询参数提取分页，默认 page=1, size=20，size 硬上限 200 |
| `parseStringSlice` | `(s) []string` | 逗号分隔字符串转切片 |
| `formatStringSlice` | `(parts) string` | 切片转逗号分隔字符串 |
| `corsMiddleware` | `(next) http.Handler` | CORS 中间件，允许所有来源 |
| `loggingMiddleware` | `(next) http.Handler` | HTTP 请求日志中间件：记录方法/路径/状态码/耗时，4xx→WARN，5xx→ERROR |

#### 处理器详解

**backup_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleBackupTrigger` | `POST /api/backup/trigger` | 触发备份，body: `{type: "full"/"incremental"/"auto"}`（auto 由系统自动判断），返回 `{backup_id, status}`（202 Accepted） |
| `handleBackupCancel` | `POST /api/backup/cancel?backup_id=X` | 取消备份，backup_id 可选（不传则自动查找内存中的，再查 DB 残留记录） |
| `handleBackupStatus` | `GET /api/backup/status` | 获取当前备份状态（OR 逻辑：内存 running 或 DB running），返回 `{is_running, running_backup}` |
| `handleBackupProgressStream` | `GET /api/backup/progress/stream` | **SSE 实时进度流**：15 秒心跳保活，连接时回放历史事件，推送 phase/progress/file/log 事件。Nginx 需配置 `proxy_buffering off` 和 1 小时超时 |

**dashboard_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleDashboardStats` | `GET /api/dashboard/stats` | 获取仪表板统计数据 |
| `handleDashboardHistory` | `GET /api/dashboard/history?page=&size=` | 获取备份历史（分页） |

**content_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleListDirectories` | `GET /api/content/directories` | 列出所有备份目录 |
| `handleAddDirectory` | `POST /api/content/directories` | 添加备份目录，body: BackupDirectory |
| `handleUpdateDirectory` | `PATCH /api/content/directories/{id}` | 更新备份目录（PATCH 语义：只更新提供的字段，启用/禁用切换只需传 `{enabled: true/false}`） |
| `handleDeleteDirectory` | `DELETE /api/content/directories/{id}` | 删除备份目录 |
| `handleListExclusions` | `GET /api/content/exclusions` | 列出所有排除规则 |
| `handleAddExclusion` | `POST /api/content/exclusions` | 添加排除规则，body: ExclusionRule（必须包含 pattern 字段） |
| `handleUpdateExclusion` | `PUT /api/content/exclusions/{id}` | 更新排除规则 |
| `handleDeleteExclusion` | `DELETE /api/content/exclusions/{id}` | 删除排除规则 |

**fs_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleFSBrowse` | `GET /api/fs/browse?path=` | 浏览文件系统，标记备份状态 |

辅助方法：
- `isPathInBackup(path, isDir, backupDirs)` — 路径是否在备份范围内
- `willPathBeBackedUp(path, isDir, backupDirs, exclusions)` — 路径是否将被备份
- `isPathExcluded(path, exclusions)` — 路径是否被排除
- `fileHasUpdate(path, info)` — 文件是否有更新

**strategy_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleGetSchedule` | `GET /api/strategy/schedule` | 获取调度配置 |
| `handleUpdateSchedule` | `PUT /api/strategy/schedule` | 更新调度配置，同时更新运行中的调度器 |
| `handleGetCompression` | `GET /api/strategy/compression` | 获取压缩配置 |
| `handleUpdateCompression` | `PUT /api/strategy/compression` | 更新压缩配置 |
| `handleGetUpload` | `GET /api/strategy/upload` | 获取上传配置 |
| `handleUpdateUpload` | `PUT /api/strategy/upload` | 更新上传配置 |
| `handleGetRetention` | `GET /api/strategy/retention` | 获取保留策略 |
| `handleUpdateRetention` | `PUT /api/strategy/retention` | 更新保留策略 |
| `handleGetEncryption` | `GET /api/strategy/encryption` | 获取加密配置 |
| `handleUpdateEncryption` | `PUT /api/strategy/encryption` | 更新加密配置 |

策略配置的读取优先级：**数据库 config_kv > 配置文件默认值**

**log_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleListLogs` | `GET /api/logs?backup_id=&level=&search=&start_time=&end_time=&page=&page_size=` | 过滤分页查询日志 |
| `handleGetLog` | `GET /api/logs/{id}` | 获取单条日志 |

**restore_handler.go**

| 处理器 | 请求 | 说明 |
|--------|------|------|
| `handleRestore` | `POST /api/restore` | 恢复文件，body: RestoreRequest（使用独立 4h 超时 context，客户端断连不中断） |
| `handleGarbageCollection` | `POST /api/gc` | 异步触发垃圾回收 |
| `handleReconcile` | `POST /api/reconcile?dry_run=true/false` | **数据一致性对账**：同步检查 OSS ↔ hash_index ↔ backup_files ↔ 备份状态。dry_run=true 仅报告不修复，备份运行中返回 409 Conflict |
| `handleStorageHealth` | `GET /api/storage/health` | **OSS 连通性健康检查**：ping OSS 验证配置/凭证/网络，返回延迟 ms，不可用时返回 503 |

### 3.6 备份引擎 (backup)

**文件**: `internal/backup/engine.go`, `restore.go`, `progress.go`, `reconcile.go`

#### Engine 结构体

```go
type Engine struct {
    db         *db.Database
    scanner    *scanner.Scanner
    dedup      *dedup.Deduplicator
    compressor *compress.Compressor
    encryptor  *crypto.Encryptor
    storage    *storage.StorageManager
    config     *config.AppConfig
    logger     *slog.Logger
    progress   *ProgressBroker       // SSE 进度推送
    mu         sync.Mutex
    runningBackupID int64
    cancelFuncs    map[int64]context.CancelFunc
}
```

#### 核心方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewEngine` | `(db, sc, dd, comp, enc, stor, cfg, pb) *Engine` | 创建引擎（注入 ProgressBroker） |
| `ProgressBroker` | `() *ProgressBroker` | 获取进度 Broker 供 SSE 订阅 |
| `RunFullBackup` | `(ctx) error` | 同步执行全量备份（锁内双重检查内存+DB状态） |
| `RunIncrementalBackup` | `(ctx) error` | 同步执行增量备份（锁内双重检查内存+DB状态） |
| `determineBackupType` | `() (BackupType, *int64, error)` | 自动判断全量/增量（基于 full_reset_interval） |
| `StartBackup` | `(backupType) (int64, error)` | 异步启动备份（支持 type=auto），锁内完成状态检查和记录创建防止竞态 |
| `Cancel` | `(backupID) error` | 取消运行中的备份 |
| `RunningBackupID` | `() (int64, bool)` | 获取当前运行中的备份 ID |
| `NeedsReconcile` | `() bool` | 轻量级一致性检查（不列 OSS，检查 ref_count 漂移和状态错误） |
| `Reconcile` | `(ctx, dryRun) (*ReconcileReport, error)` | 执行完整数据一致性对账 |
| `RunGarbageCollection` | `(ctx) error` | 执行垃圾回收 |

#### 并发安全与崩溃恢复

- **互斥锁保护**：`StartBackup`/`RunFullBackup`/`RunIncrementalBackup` 在 `mu.Lock()` 内同时检查内存 `runningBackupID` 和数据库 `IsRunning()`，双重检查防止并发竞态
- **启动时清理**：main 启动时调用 `CleanupStaleRunning()` 将残留 running/pending 标记为 failed
- **取消兜底**：`handleBackupCancel` 若内存中找不到运行备份，会检查 DB 残留记录并清理
- **全量备份重建映射**：全量备份将 Unchanged 文件重写为 Modified 处理，确保崩溃后重建 hash_index ↔ OSS 映射
- **同批次去重元数据**：使用 `pendingByHash` map 暂存本批次上传文件的加密/压缩元数据，供同批次去重文件复用

#### 备份执行流程 (`executeBackup`) 与进度加权

```
Phase 1: 更新状态为 running
         → 推送 phase=scanning 事件
Phase 2: 扫描目录 (scanner.ScanWithProgress)
         → 进度 0-5%（对数曲线估算，扫描中每 500 文件记日志）
Phase 3: 计算哈希 (scanner.ComputeHashes)
         → 进度 5-30%，逐文件推送 file + log 事件（仅SSE，不写DB）
Phase 4: 按变更类型分类
         → 全量: Unchanged → Modified（重建映射）
         → 增量: Unchanged 跳过 (skippedInc++)
Phase 5: 去重 (dedup.Deduplicate)
         → 进度 30-35%
Phase 6: 处理需上传文件 (compress → encrypt → upload → verify)
         → 处理去重跳过文件（同批次从 pendingByHash 取元数据，跨批次查 DB）
         → 进度 35-95%，逐文件推送详细日志
Phase 7: (已合并到 Phase 6)
Phase 8: 批量写入 backup_files 关联
Phase 9: 标记已删除文件并递减引用计数（事务内安全递减）
         → 进度 95-100%
Phase 10: 更新备份统计
结束: 推送 completed/failed/cancelled 事件
     → 30 秒后清空 ProgressBroker 历史缓冲
```

#### 单文件处理流程 (`processAndUploadFile`)

```
1. Upsert 文件记录获取 fileID
2. 判断是否需要压缩 (ShouldCompress)
3. 如需压缩: zstd 压缩到临时文件（推送日志）
4. AES-256-GCM 加密到临时文件（推送日志）
5. Stat 加密文件获取 storedSize
6. 生成存储键: data/{YYYYMMDD}-{type}/{hash_prefix}/{hash}.enc
7. 通过 rclone 上传到 OSS（推送日志）
8. 验证上传 (Exists 检查，推送日志)
9. Upsert 哈希索引记录（推送日志）
```

#### Restorer 结构体

```go
type Restorer struct {
    db         *db.Database
    encryptor  *crypto.Encryptor
    compressor *compress.Compressor
    storage    *storage.StorageManager
    config     *config.AppConfig
}
```

#### 恢复方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewRestorer` | `(db, enc, comp, stor, cfg) *Restorer` | 创建恢复器 |
| `Restore` | `(ctx, req) (*RestoreResult, error)` | 执行恢复 |
| `ListRestorableFiles` | `(dirPath, backupID) ([]*FileRecord, error)` | 列出可恢复文件 |
| `GetFileInfo` | `(path) (*FileRecord, *BackupFileRecord, error)` | 获取文件信息 |

#### 单文件恢复流程 (`restoreFile`)

```
1. 检查对象是否需要解冻 (ColdArchive)
   - 如需解冻: 发起 RestoreObject 请求
   - 轮询等待解冻完成 (最长 30 分钟，每 30 秒检查)
2. 下载加密文件
3. AES-256-GCM 解密
4. 如有压缩: zstd 解压
5. SHA-256 哈希验证
6. 移动到输出目录
```

#### ProgressBroker - SSE 进度推送 (`progress.go`)

```go
type ProgressBroker struct {
    clients    map[chan models.ProgressEvent]struct{}
    history    []models.ProgressEvent
    historyMax int
    mu         sync.RWMutex
    logger     *slog.Logger
}
```

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewProgressBroker` | `(logger) *ProgressBroker` | 创建 Broker，默认保留 200 条历史 |
| `Subscribe` | `() (<-chan event, history, unsub func)` | 订阅事件：返回只读 channel、历史事件回放、取消订阅函数 |
| `Publish` | `(event)` | 推送事件给所有订阅者（非阻塞发送，channel 满则丢弃并警告） |
| `ClearHistory` | `()` | 清空历史缓冲（备份完成 30 秒后调用） |

**SSE 事件格式**：
```
event: progress
data: {"type":"progress","phase":"uploading","current":150,"total":200,"percent":75,"message":"上传中...","timestamp":"..."}

: heartbeat (每 15 秒)
```

**前端订阅**：使用 `EventSource` 连接 `/api/backup/progress/stream`，自动重连。

#### Reconciler - 数据一致性对账 (`reconcile.go`)

检查和修复三层数据的一致性问题：OSS 对象存储 ↔ hash_index（引用计数） ↔ backup_files（备份-文件关联）。

| 方法 | 签名 | 说明 |
|------|------|------|
| `Reconcile` | `(ctx, dryRun) (*ReconcileReport, error)` | 执行完整对账 |

**检查项（6 类不一致）**：

| 问题类型 | 说明 | DryRun 修复行为 |
|----------|------|-----------------|
| OSSOnlyOrphans | OSS 有对象但 hash_index 无记录（不自动删除，需要人工确认） | 不自动修复，记录告警 |
| DanglingHashIndexesRefZero | hash_index 有记录但 ref_count=0 且 OSS 无对应对象 | 自动删除 hash_index 记录 |
| DanglingHashIndexesRefNonZero | hash_index 有记录且 ref_count>0 但 OSS 无对应对象 | 不自动修复，记录错误 |
| OrphanBackupFiles | backup_files 有记录但 hash_index 无对应条目 | 自动删除 orphan backup_files 记录 |
| BackupFilesMissingHashIndexButInOSS | backup_files 指向的 hash_index 不存在但 OSS 有对象 | 自动重建 hash_index 记录（ref_count=1） |
| RefCountMismatches | hash_index.ref_count 与实际活跃文件数不一致 | 自动修正 ref_count |
| FailedBackupsWithFiles | 备份状态为 failed 但有 backup_files 记录 | 自动修正为 completed 或清理孤立文件 |
| CompletedBackupsNoFiles | 备份状态为 completed 但无 backup_files 记录 | 自动修正为 failed |

**关键约束**：
- 备份运行中（锁被持有）时拒绝执行，返回 409 Conflict
- DryRun=true 只检查不修改数据库/OSS
- 修复操作在事务中执行
- OSS 上的孤儿对象不会自动删除（避免误删），仅记录在报告中

**并发控制**：`Reconcile` 方法内部会 `engine.mu.Lock()` 获取备份锁，确保与备份任务互斥。

**轻量级检查**：`NeedsReconcile()` 不列 OSS，仅检查 DB 层 ref_count 漂移和备份状态错误，用于仪表板快速提示。

### 3.7 文件扫描器 (scanner)

**文件**: `internal/scanner/scanner.go`

#### 核心类型

```go
type ChangeType int  // Added, Modified, Deleted, Unchanged, Renamed

type FileChange struct {
    Path       string
    ChangeType ChangeType
    Size       int64
    ModTime    time.Time
    OldHash    string
    NewHash    string
    Inode      uint64
}

type ScanResult struct {
    Changes      []FileChange
    TotalScanned int
    TotalActive  int
    Errors       []string
}

type Scanner struct {
    fileRepo   *db.FileRepository
    configRepo *db.ConfigRepository
}
```

#### 扫描流程 (`Scan`)

```
1. 获取启用的备份目录
2. 获取启用的排除规则
3. 获取文件大小限制 (config_kv)
4. 预加载所有活跃文件记录
5. 遍历每个目录 (walkDirectory)
   - 解析符号链接
   - 循环检测 (dev+inode)
   - 跳过符号链接文件，跟随符号链接目录
   - 处理每个文件 (processFile)
6. 计算哈希 (ComputeHashes)
7. 检测删除（DB 中有但磁盘上无）
```

#### 排除规则匹配逻辑 (`shouldExclude`)

| 规则类型 | 匹配方式 |
|----------|----------|
| `extension` | 文件扩展名匹配（忽略大小写） |
| `directory` | 路径组件匹配（filepath.Match） |
| `pattern` | 文件名 glob 匹配（filepath.Match） |
| `size_exceed` | 在大小检查阶段处理 |

#### 哈希计算 (`ComputeHashes`)

- 对 Added 和 Modified 文件计算 SHA-256
- 如果新哈希与旧哈希相同，降级为 Unchanged（mtime-only 变更的误报）

### 3.8 去重模块 (dedup)

**文件**: `internal/dedup/dedup.go`

#### 核心类型

```go
type DedupFileEntry struct {
    scanner.FileChange
    StorageKey string
    IsNew      bool
}

type DedupSkippedEntry struct {
    Path              string
    Hash              string
    ExistingStorageKey string
    Reason            string
}

type DedupResult struct {
    ToUpload   []DedupFileEntry
    Skipped    []DedupSkippedEntry
    TotalSaved int64
}

type Deduplicator struct {
    hashRepo *db.HashRepository
}
```

#### 去重逻辑 (`Deduplicate`)

```
对每个 Added/Modified 文件:
  1. 如果哈希为空，加入上传列表
  2. 查询 hash_index 表
  3. 如果哈希已存在:
     - 递增 ref_count
     - 加入 Skipped 列表
     - 累加节省字节数
  4. 如果哈希不存在:
     - 加入上传列表
```

### 3.9 压缩模块 (compress)

**文件**: `internal/compress/compress.go`

```go
type Compressor struct {
    enabled         bool
    algorithm       string
    level           int
    skipTypes       map[string]bool
    zstdBin         string
    compressTimeout time.Duration  // 默认 30 分钟
}
```

#### 关键方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewCompressor` | `(cfg) *Compressor` | 从配置创建，定位 zstd 二进制 |
| `ShouldCompress` | `(filePath) bool` | 根据扩展名判断是否压缩 |
| `Compress` | `(inputPath, outputPath) (originalSize, compressedSize, error)` | zstd 压缩，带超时 |
| `Decompress` | `(inputPath, outputPath) error` | zstd 解压 |
| `FindZstdBinary` | `() string` | 查找 zstd 二进制（PATH → 常见路径） |
| `SetTimeout` | `(timeout)` | 设置压缩超时 |

压缩命令：`zstd -{level} -f -o outputPath inputPath`
解压命令：`zstd -d -f -o outputPath inputPath`

### 3.10 加密模块 (crypto)

**文件**: `internal/crypto/crypto.go`

```go
type Encryptor struct {
    masterKeyPath string
    masterKey     []byte
    chunkSize     int  // 默认 256KB
}
```

#### 加密方案

- **算法**: AES-256-GCM
- **密钥派生**: HKDF-SHA256，从主密钥 + 随机 salt 派生每文件 DEK
- **HKDF Info**: `"nas-backup-dek-v1"`
- **流式加密**: 256KB 分块，每块独立 nonce
- **密钥文件**: 32 字节随机密钥，0600 权限

#### 加密文件格式

```
salt (32 bytes) || chunk1: nonce(12) + ciphertext+tag || chunk2: nonce(12) + ciphertext+tag || ...
```

#### 关键方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewEncryptor` | `(keyFilePath) (*Encryptor, error)` | 创建加密器，加载或生成主密钥 |
| `EncryptFile` | `(inputPath, outputPath) (iv string, err error)` | 流式加密，返回首块 nonce 的 base64 |
| `DecryptFile` | `(inputPath, outputPath, ivBase64) error` | 流式解密，验证首块 nonce |
| `GenerateMasterKey` | `() ([]byte, error)` | 生成 32 字节随机密钥 |
| `SaveMasterKey` | `(path, key) error` | 保存密钥文件（0600） |
| `LoadMasterKey` | `(path) ([]byte, error)` | 加载密钥文件 |
| `SetChunkSize` | `(size)` | 设置分块大小（最小 1KB） |

### 3.11 存储管理 (storage)

**文件**: `internal/storage/storage.go`

```go
type StorageManager struct {
    rcloneBin    string
    rcloneBinCfg string
    rcloneConf   string
    remoteName   string
    storageClass string
    ossEndpoint  string
    ossBucket    string
    ossAKID      string
    ossAKSecret  string
}
```

#### 双层远程配置

- **[oss]**: 原始 S3 兼容远程（指向阿里云 OSS）
- **[oss-crypt]**: crypt 远程（包装 oss 远程，提供传输层加密）

#### 关键方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewStorageManager` | `(cfg) (*StorageManager, error)` | 创建存储管理器，定位 rclone |
| `EnsureRcloneConfig` | `() error` | 确保 rclone 配置文件存在，不存在则自动生成 |
| `Upload` | `(localPath, remoteKey) error` | 上传文件到 OSS（rclone copyto，3 次重试） |
| `Download` | `(remoteKey, localPath) error` | 从 OSS 下载文件（rclone copyto，3 次重试） |
| `Delete` | `(remoteKey) error` | 删除 OSS 对象（rclone delete，3 次重试） |
| `DeleteBatch` | `(remoteKeys) error` | 批量删除 OSS 对象 |
| `Exists` | `(remoteKey) (bool, error)` | 检查对象是否存在（rclone lsl） |
| `RestoreObject` | `(remoteKey, expedited) error` | 发起解冻请求（OSS SDK） |
| `CheckRestored` | `(remoteKey) (bool, error)` | 检查对象是否已解冻（检查 X-Oss-Restore 头） |
| `GetStorageUsage` | `() (int64, error)` | 获取存储使用量（rclone size） |
| `Ping` | `(ctx) (latencyMs int64, err error)` | OSS 连通性健康检查：向临时文件 `._health_check` 写入 1 字节并删除，测量延迟 |
| `FindRcloneBinary` | `() string` | 查找 rclone 二进制 |

#### 重试机制

- 默认 3 次重试
- 指数退避：2s → 4s → 8s

### 3.12 调度器 (scheduler)

**文件**: `internal/scheduler/scheduler.go`

```go
type Scheduler struct {
    cron   *cron.Cron
    engine *backup.Engine
    db     *db.Database
    config *config.AppConfig
    mu     sync.Mutex
    jobID  cron.EntryID
}
```

#### 关键方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `NewScheduler` | `(engine, db, cfg) *Scheduler` | 创建调度器 |
| `Start` | `() error` | 解析 cron 表达式，注册任务，启动调度 |
| `Stop` | `()` | 优雅停止，等待运行中的任务完成 |
| `UpdateSchedule` | `(cronExpr) error` | 动态更新 cron 表达式 |
| `NextRun` | `() time.Time` | 获取下次运行时间 |
| `IsEnabled` | `() bool` | 调度器是否运行中 |

#### 自动备份类型判断 (`isFullResetNeeded`)

```
如果 full_reset_interval > 0:
  - 获取最近完成的全量备份
  - 如果不存在或未完成 → 需要全量
  - 如果完成时间超过 interval 个月前 → 需要全量
  - 否则 → 增量
```

### 3.13 日志系统 (logger)

**文件**: `internal/logger/logger.go`

```go
type Logger struct {
    mu        sync.Mutex
    level     Level
    logger    *log.Logger
    file      *os.File
    maxSizeMB int
    maxFiles  int
    filePath  string
}
```

- 双输出：stdout + 文件
- 日志轮转：超过 maxSizeMB 时轮转，保留 maxFiles 个历史文件
- 全局单例：`Debug()`, `Info()`, `Warn()`, `Error()` 直接调用

---

## 4. 前端架构详解

### 4.1 技术栈与构建配置

| 项目 | 版本/配置 |
|------|-----------|
| React | 18.3 |
| TypeScript | 5.8 |
| Vite | 6.3 |
| Tailwind CSS | 3.4 |
| Zustand | 5.0 |
| react-router-dom | 7.3 |
| lucide-react | 0.511 |
| clsx + tailwind-merge | 用于类名合并 |

Vite 配置要点：
- 路径别名 `@` → `src/`
- 开发代理 `/api` → `http://localhost:8080/api`

### 4.2 路由与页面结构

```
BrowserRouter
└── AppLayout (侧边栏 + 主内容区 + Toast)
    ├── /           → Dashboard (全览)
    ├── /content    → Content (内容选择)
    ├── /strategy   → Strategy (策略设置)
    ├── /logs       → Logs (日志)
    └── /reconcile  → Reconcile (数据一致性对账)
```

### 4.3 状态管理 (Zustand)

**文件**: `src/store/useAppStore.ts`

```typescript
interface AppState {
  sidebarCollapsed: boolean;    // 侧边栏是否折叠
  toggleSidebar: () => void;    // 切换侧边栏
  toasts: Toast[];              // Toast 通知列表
  addToast: (toast) => void;    // 添加 Toast
  removeToast: (id) => void;    // 移除 Toast
}

interface Toast {
  id: string;
  type: 'success' | 'error' | 'info' | 'warning';
  message: string;
}
```

### 4.4 API 客户端层

**文件**: `src/utils/api.ts`

#### 基础请求函数

| 函数 | 说明 |
|------|------|
| `request<T>(endpoint, options)` | 通用请求，返回 `APIResponse<T>` |
| `paginatedRequest<T>(endpoint, params)` | 分页请求，返回 `PaginatedResponse<T>` |

API_BASE = `/api`（开发环境代理到后端）

#### API 对象

| 对象 | 方法 | 对应后端接口 |
|------|------|-------------|
| `dashboardApi` | `getStats()` | GET /dashboard/stats |
| | `getHistory(page, size)` | GET /dashboard/history |
| `backupApi` | `trigger(type)` | POST /backup/trigger（type 支持 full/incremental/auto） |
| | `cancel(backupId?)` | POST /backup/cancel |
| | `getStatus()` | GET /backup/status |
| | `getProgressStreamUrl()` | 返回 SSE 端点 URL `/api/backup/progress/stream` |
| `fsApi` | `browse(path)` | GET /fs/browse |
| `directoryApi` | `list()` | GET /content/directories |
| | `create(data)` | POST /content/directories |
| | `update(id, data)` | PATCH /content/directories/{id}（PATCH 语义） |
| | `delete(id)` | DELETE /content/directories/{id} |
| `exclusionApi` | `list()` | GET /content/exclusions |
| | `create(data)` | POST /content/exclusions |
| | `update(id, data)` | PUT /content/exclusions/{id} |
| | `delete(id)` | DELETE /content/exclusions/{id} |
| `strategyApi` | `getSchedule()` | GET /strategy/schedule |
| | `updateSchedule(data)` | PUT /strategy/schedule |
| | `getCompression()` | GET /strategy/compression |
| | `updateCompression(data)` | PUT /strategy/compression |
| | `getUpload()` | GET /strategy/upload |
| | `updateUpload(data)` | PUT /strategy/upload |
| | `getRetention()` | GET /strategy/retention |
| | `updateRetention(data)` | PUT /strategy/retention |
| | `getEncryption()` | GET /strategy/encryption |
| | `updateEncryption(data)` | PUT /strategy/encryption |
| `logApi` | `list(params)` | GET /logs |
| | `get(id)` | GET /logs/{id} |
| `gcApi` | `trigger()` | POST /gc |
| `reconcileApi` | `run(dryRun?)` | POST /api/reconcile?dry_run=true/false |
| `storageApi` | `health()` | GET /api/storage/health |

#### TypeScript 类型定义

前端定义了与后端 models 对应的 TypeScript 接口：`DashboardStats`（含 `ossInfo`/`ossQuotaBytes`/`backupCount`/`uniqueHashCount`/`needsReconcile`），`BackupRecord`，`BackupStatus`，`BackupType`（新增 `auto`），`BackupDirectory`，`ExclusionRule`，`ScheduleConfig`，`CompressionConfig`，`UploadConfig`（含 `concurrency`），`RetentionConfig`（含 `deletedRetentionDays`），`EncryptionConfig`，`LogRecord`，`LogQueryParams`，`FSEntry`（含 `partialBackup`），`FSBrowseResult`，`ProgressEvent`，`BackupPhase`，`ReconcileReport`，`RefCountMismatch`，`StorageHealthResult`。

### 4.5 页面详解

#### Dashboard 页面 (`src/pages/Dashboard.tsx`)

**功能**: 系统全览，备份操作入口

**布局**:
1. **状态横幅**: 显示备份运行状态（运行中/空闲，含进度条和阶段名）、上次备份时间、下次备份时间
   - 运行中时：展示当前阶段（scanning→hashing→uploading→finalizing）、百分比进度条、实时日志尾行（SSE）
   - idle 时：显示 SSE 连接状态指示
2. **对账告警横幅**: `stats.needsReconcile` 为 true 时显示黄色警告，提示用户前往对账页
3. **仪表盘图表** (3 列): OSS 存储使用率（含配额/已用/总节省）、去重节省、压缩节省
4. **统计卡片** (4 列): 备份总数、唯一哈希数、活跃文件数、OSS 使用量
5. **OSS 信息卡片**: 显示存储类型、Endpoint、Bucket、Region
6. **操作按钮**: 一键备份（auto 类型）、全量备份、取消备份、垃圾回收、数据对账
7. **备份历史表格**: ID、类型（full/inc/auto）、状态（彩色徽章）、文件数、大小、上传量、去重跳过、开始/完成时间
8. **分页组件**

**数据刷新**:
- idle 状态：每 10 秒轮询
- 备份运行中：**优先使用 SSE 实时推送**，不依赖轮询；断开时降级为 2 秒轮询
- SSE 进度流通过 `useBackupProgress` Hook 订阅，自动重连

**交互**:
- "一键备份"按钮调用 `trigger('auto')`，系统自动判断全量/增量
- 取消备份和垃圾回收前弹出确认对话框
- OSS 信息可一键复制

#### Content 页面 (`src/pages/Content.tsx`)

**功能**: 文件浏览与备份内容管理

**布局**:
1. **文件浏览器** (FileBrowser 组件):
   - 面包屑导航
   - 文件列表表格（名称、大小、备份状态标签：已备份/部分备份/未备份/有更新）
   - 右侧详情面板（文件信息、备份状态、操作按钮）
   - 操作: 设为备份目录、禁用/启用备份、移除备份目录、进入目录
2. **排除规则表格**: 模式、类型（扩展名/目录/模式/大小超限）、启用状态、操作
3. **添加/编辑规则面板** (SlidePanel): 模式输入、类型选择、启用开关

**交互**:
- 单击文件/目录显示详情
- 双击目录进入
- 目录可直接设为备份目标或移除
- 排除规则支持增删改和启用/禁用
- 目录状态徽章：完全纳入备份（绿色）/部分纳入（黄色，子目录有不同设置）/未备份（灰色）

#### Strategy 页面 (`src/pages/Strategy.tsx`)

**功能**: 备份策略配置

**布局** (5 个配置卡片):
1. **调度配置**: 启用开关、Cron 表达式、时区选择（含常见预设：每小时/每天/每周/每月/自定义）
2. **压缩配置**: 启用开关、算法（只读 zstd）、压缩级别滑块 (1-22)、跳过类型标签管理
3. **上传配置**: 存储类型选择（标准/低频/归档/冷归档）、OSS 并发数（1-10）、上传分块大小、重试次数、重试延迟
4. **保留策略**: 版本保留数、孤儿数据清理天数、全量备份间隔(月)、已删除文件保留天数
5. **加密配置**: 算法（只读 AES-256-GCM）、密钥文件路径

每个卡片有独立的保存按钮。配置变更后提示用户重新加载或应用。

#### Logs 页面 (`src/pages/Logs.tsx`)

**功能**: 日志查看与筛选

**布局**:
1. **筛选栏**: 级别下拉、搜索框、备份 ID、时间范围、搜索/重置按钮
2. **日志表格**: 级别（彩色标签）、备份 ID、消息、时间、详情展开按钮
3. **分页组件**

**交互**:
- 点击搜索应用筛选条件
- 点击详情按钮展开/折叠日志详情
- 级别颜色: DEBUG=灰, INFO=蓝, WARN=黄, ERROR=红

#### Reconcile 页面 (`src/pages/Reconcile.tsx`)

**功能**: 数据一致性对账（诊断与修复）

**布局**:
1. **操作栏**: "执行对账检查（Dry Run）"按钮、"执行修复"按钮（红色）、健康状态指示器
2. **上次检查结果摘要**: 执行时间、耗时、DryRun 标识、发现问题总数/已修复数/错误数
3. **问题分类卡片**（按问题类型分块）:
   - OSS 孤儿对象（仅告警，不自动删除）
   - 悬空哈希索引（ref=0，无OSS对象）
   - ref_count 不匹配项
   - 孤立 backup_files 记录
   - 备份状态异常
   - 每个分类显示数量和详情列表（hash/路径/原因）
4. **实时日志面板**: 对账执行过程中的实时输出
5. **存储健康检查**: OSS 连通性状态、延迟 ms

**交互**:
- Dry Run 按钮：只读检查，不修改数据
- 修复按钮：执行对账并自动修复可修复项，需二次确认
- 执行中按钮置灰，显示加载状态
- 对账完成后各问题卡片展开显示详情，已修复项标记为绿色

### 4.6 组件详解

#### 布局组件

**AppLayout** (`src/components/layout/AppLayout.tsx`)
- 左侧 Sidebar + 右侧主内容区
- 主内容区根据 sidebarCollapsed 调整左边距 (ml-16 / ml-56)
- 右上角 Toast 通知区域（4 秒自动消失）

**Sidebar** (`src/components/layout/Sidebar.tsx`)
- 5 个导航项: 全览(/)、内容选择(/content)、策略设置(/strategy)、日志(/logs)、数据对账(/reconcile)
- 底部折叠/展开按钮
- 活跃路由高亮

#### 共享组件

**EmptyState** (`src/components/shared/EmptyState.tsx`)
- 空数据占位提示

**LoadingSkeleton** (`src/components/shared/LoadingSkeleton.tsx`)
- 骨架屏加载占位，支持行数和卡片模式

#### UI 组件

| 组件 | 文件 | Props | 说明 |
|------|------|-------|------|
| `ConfirmDialog` | `ui/ConfirmDialog.tsx` | open, onClose, onConfirm, title, message | 确认对话框 |
| `DataTable` | `ui/DataTable.tsx` | columns, data, rowKey | 通用数据表格，支持自定义列渲染 |
| `GaugeChart` | `ui/GaugeChart.tsx` | value, max, label, color | 仪表盘图表（SVG 圆弧） |
| `Pagination` | `ui/Pagination.tsx` | page, size, total, onChange | 分页导航 |
| `SlidePanel` | `ui/SlidePanel.tsx` | open, onClose, title | 右侧滑出面板 |
| `StatCard` | `ui/StatCard.tsx` | icon, label, value, iconColor | 统计卡片 |
| `StatusBadge` | `ui/StatusBadge.tsx` | status, pulse | 状态徽章（带脉冲动画） |

### 4.7 Hooks

**useApi** (`src/hooks/useApi.ts`)

```typescript
function useApi<T>(): {
  data: T | null;
  loading: boolean;
  error: string | null;
  execute: (apiCall: () => Promise<APIResponse<T>>) => Promise<T>;
  reset: () => void;
  setData: Dispatch<SetStateAction<T | null>>;
}
```

通用 API 请求 Hook，管理 loading/error/data 状态。

**usePolling** (`src/hooks/usePolling.ts`)

```typescript
function usePolling<T>(
  fetchFn: () => Promise<T>,
  interval?: number,     // 默认 3000ms
  enabled?: boolean      // 默认 true
): { start: () => void; stop: () => void }
```

轮询 Hook，enabled 变化时自动启动/停止。

**useBackupProgress** (`src/hooks/useBackupProgress.ts`)

```typescript
function useBackupProgress(onEvent?: (event: ProgressEvent) => void): {
  events: ProgressEvent[];      // 历史事件列表
  connected: boolean;           // SSE 连接状态
  currentPhase: BackupPhase | null;
  percent: number;              // 0-100
  currentFile: string | null;
  latestMessage: string | null;
}
```

SSE 实时进度订阅 Hook：
- 自动管理 EventSource 生命周期（连接/重连/断开）
- 回放历史事件，接收实时 phase/progress/file/log 事件
- 备份完成后 30 秒自动清空缓冲
- 连接断开时自动重连（浏览器原生 EventSource 行为）

**useConfirm** (`src/hooks/useConfirm.ts`)

全局确认对话框 Hook，返回 `{ confirm, ConfirmDialog }`，`confirm(message): Promise<boolean>` 用于异步确认操作。

### 4.8 工具函数

**format.ts** (`src/utils/format.ts`)

| 函数 | 说明 |
|------|------|
| `formatFileSize(bytes)` | 字节数格式化 (B/KB/MB/GB/TB) |
| `formatDateTime(dateStr)` | 日期时间格式化 (YYYY-MM-DD HH:mm:ss) |
| `formatRelativeTime(dateStr)` | 相对时间 (刚刚/X分钟前/X小时前/X天前) |
| `formatDuration(ms)` | 时长格式化 (Xms/Xs/Xm Xs) |
| `formatPercent(value, total)` | 百分比格式化 |

**constants.ts** (`src/utils/constants.ts`)

| 常量 | 说明 |
|------|------|
| `BACKUP_STATUS_MAP` | 备份状态中文映射 + 颜色 |
| `BACKUP_TYPE_MAP` | 备份类型中文映射 + 颜色（含 auto→智能） |
| `LOG_LEVEL_MAP` | 日志级别映射 + 颜色 + 背景 |
| `EXCLUSION_TYPE_MAP` | 排除规则类型中文映射 |
| `STORAGE_CLASS_MAP` | 存储类型中文映射（标准/低频/归档/冷归档） |
| `TIMEZONE_OPTIONS` | 时区选项列表（含 Asia/Shanghai 等） |
| `BACKUP_PHASE_MAP` | 备份阶段中文映射（扫描/计算哈希/去重/上传/完成等） |

**lib/utils.ts** (`src/lib/utils.ts`)

`cn()` 函数：合并 clsx + tailwind-merge，用于条件类名。

---

## 5. 数据库 Schema 详解

**文件**: `internal/db/migrations/001_init.sql`

### 表结构

#### files — 文件追踪

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| path | TEXT | NOT NULL UNIQUE | 文件路径 |
| size | INTEGER | NOT NULL DEFAULT 0 | 文件大小 |
| mod_time | TEXT | NOT NULL | 修改时间 |
| hash | TEXT | NOT NULL DEFAULT '' | SHA-256 哈希 |
| status | TEXT | NOT NULL DEFAULT 'active' CHECK(active/deleted) | 状态 |
| backup_id | INTEGER | | 关联备份 ID |
| inode | INTEGER | NOT NULL DEFAULT 0 | inode 号 |
| created_at | TEXT | NOT NULL DEFAULT datetime | 创建时间 |
| updated_at | TEXT | NOT NULL DEFAULT datetime | 更新时间 |

索引: `idx_files_path`, `idx_files_hash`, `idx_files_status`, `idx_files_inode`

#### backups — 备份会话

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| type | TEXT | NOT NULL CHECK(full/incremental/auto) | 备份类型（auto 由系统判断） |
| status | TEXT | NOT NULL DEFAULT 'pending' CHECK(pending/running/completed/failed/cancelled) | 状态 |
| base_backup_id | INTEGER | | 基础全量备份 ID |
| total_files | INTEGER | NOT NULL DEFAULT 0 | 总文件数 |
| total_size | INTEGER | NOT NULL DEFAULT 0 | 总大小 |
| uploaded_size | INTEGER | NOT NULL DEFAULT 0 | 上传大小 |
| skipped_dedup | INTEGER | NOT NULL DEFAULT 0 | 去重跳过数 |
| skipped_inc | INTEGER | NOT NULL DEFAULT 0 | 增量跳过数 |
| compress_saved | INTEGER | NOT NULL DEFAULT 0 | 压缩节省 |
| started_at | TEXT | | 开始时间 |
| completed_at | TEXT | | 完成时间 |
| error_message | TEXT | NOT NULL DEFAULT '' | 错误信息 |
| created_at | TEXT | NOT NULL DEFAULT datetime | 创建时间 |

索引: `idx_backups_status`, `idx_backups_type`, `idx_backups_created`

#### backup_files — 备份-文件关联

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| backup_id | INTEGER | FK → backups(id) CASCADE | 备份 ID |
| file_id | INTEGER | FK → files(id) CASCADE | 文件 ID |
| storage_key | TEXT | NOT NULL | OSS 存储键 |
| encrypted_iv | TEXT | NOT NULL DEFAULT '' | 加密 IV |
| auth_tag | TEXT | NOT NULL DEFAULT '' | 认证标签 |
| compress_type | TEXT | NOT NULL DEFAULT 'none' CHECK(zstd/none) | 压缩类型 |
| original_size | INTEGER | NOT NULL DEFAULT 0 | 原始大小 |
| stored_size | INTEGER | NOT NULL DEFAULT 0 | 存储大小 |

主键: `(backup_id, file_id)`

索引: `idx_backup_files_backup`, `idx_backup_files_storage`

#### hash_index — 全局哈希索引（去重）

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| hash | TEXT | NOT NULL UNIQUE | SHA-256 哈希 |
| file_size | INTEGER | NOT NULL DEFAULT 0 | 文件大小 |
| storage_key | TEXT | NOT NULL DEFAULT '' | OSS 存储键 |
| ref_count | INTEGER | NOT NULL DEFAULT 0 | 引用计数 |
| created_at | TEXT | NOT NULL DEFAULT datetime | 创建时间 |

索引: `idx_hash_index_hash`

#### backup_logs — 备份日志

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| backup_id | INTEGER | FK → backups(id) SET NULL | 关联备份 ID |
| level | TEXT | NOT NULL DEFAULT 'info' CHECK(debug/info/warn/error) | 级别 |
| message | TEXT | NOT NULL | 消息 |
| detail | TEXT | NOT NULL DEFAULT '' | 详情 |
| created_at | TEXT | NOT NULL DEFAULT datetime | 创建时间 |

索引: `idx_backup_logs_backup`, `idx_backup_logs_level`, `idx_backup_logs_created`

#### config_kv — 运行时配置

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| key | TEXT | PK | 配置键 |
| value | TEXT | NOT NULL DEFAULT '' | 配置值 |
| updated_at | TEXT | NOT NULL DEFAULT datetime | 更新时间 |

#### backup_directories — 备份目录

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| path | TEXT | NOT NULL UNIQUE | 目录路径 |
| recursive | INTEGER | NOT NULL DEFAULT 1 | 是否递归 |
| enabled | INTEGER | NOT NULL DEFAULT 1 | 是否启用 |
| description | TEXT | NOT NULL DEFAULT '' | 描述 |

#### exclusion_rules — 排除规则

| 列 | 类型 | 约束 | 说明 |
|----|------|------|------|
| id | INTEGER | PK AUTOINCREMENT | 主键 |
| pattern | TEXT | NOT NULL UNIQUE | 匹配模式 |
| rule_type | TEXT | NOT NULL DEFAULT 'pattern' CHECK(extension/directory/pattern/size_exceed) | 规则类型 |
| enabled | INTEGER | NOT NULL DEFAULT 1 | 是否启用 |

---

## 6. API 接口完整参考

### Dashboard

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/dashboard/stats` | - | `APIResponse<DashboardStats>` | 仪表板统计 |
| GET | `/api/dashboard/history?page=&size=` | Query: page, size | `PaginatedResponse<BackupRecord>` | 备份历史 |

### Backup

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| POST | `/api/backup/trigger` | Body: `{type: "full"/"incremental"/"auto"}` | `APIResponse<{backup_id, status}>` (202) | 触发备份 |
| POST | `/api/backup/cancel?backup_id=` | Query: backup_id (可选) | `APIResponse<{status}>` | 取消备份 |
| GET | `/api/backup/status` | - | `APIResponse<{is_running, running_backup}>` | 备份状态（内存+DB双重检查） |
| GET | `/api/backup/progress/stream` | SSE 连接 | `text/event-stream` | **SSE 实时进度流**（phase/progress/file/log/heartbeat） |

### Content - File System

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/fs/browse?path=` | Query: path | `APIResponse<FSBrowseResult>` | 浏览文件系统 |

### Content - Directories

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/content/directories` | - | `APIResponse<BackupDirectory[]>` | 列出目录 |
| POST | `/api/content/directories` | Body: BackupDirectory | `APIResponse<BackupDirectory>` | 添加目录 |
| PATCH | `/api/content/directories/{id}` | Body: 部分字段 | `APIResponse<BackupDirectory>` | 更新目录（部分更新） |
| DELETE | `/api/content/directories/{id}` | - | `APIResponse<{status}>` | 删除目录 |

### Content - Exclusions

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/content/exclusions` | - | `APIResponse<ExclusionRule[]>` | 列出规则 |
| POST | `/api/content/exclusions` | Body: ExclusionRule | `APIResponse<ExclusionRule>` | 添加规则 |
| PUT | `/api/content/exclusions/{id}` | Body: ExclusionRule | `APIResponse<ExclusionRule>` | 更新规则 |
| DELETE | `/api/content/exclusions/{id}` | - | `APIResponse<{status}>` | 删除规则 |

### Strategy

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/strategy/schedule` | - | `APIResponse<ScheduleConfig>` | 获取调度配置 |
| PUT | `/api/strategy/schedule` | Body: ScheduleConfig | `APIResponse<ScheduleConfig>` | 更新调度配置 |
| GET | `/api/strategy/compression` | - | `APIResponse<CompressionConfig>` | 获取压缩配置 |
| PUT | `/api/strategy/compression` | Body: CompressionConfig | `APIResponse<CompressionConfig>` | 更新压缩配置 |
| GET | `/api/strategy/upload` | - | `APIResponse<UploadConfig>` | 获取上传配置 |
| PUT | `/api/strategy/upload` | Body: UploadConfig | `APIResponse<UploadConfig>` | 更新上传配置 |
| GET | `/api/strategy/retention` | - | `APIResponse<RetentionConfig>` | 获取保留策略 |
| PUT | `/api/strategy/retention` | Body: RetentionConfig | `APIResponse<RetentionConfig>` | 更新保留策略 |
| GET | `/api/strategy/encryption` | - | `APIResponse<EncryptionConfig>` | 获取加密配置 |
| PUT | `/api/strategy/encryption` | Body: EncryptionConfig | `APIResponse<EncryptionConfig>` | 更新加密配置 |

### Logs

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| GET | `/api/logs?backup_id=&level=&search=&start_time=&end_time=&page=&page_size=` | Query: 过滤参数 | `PaginatedResponse<LogRecord>` | 日志列表 |
| GET | `/api/logs/{id}` | - | `APIResponse<LogRecord>` | 日志详情 |

### Restore & GC & Maintenance

| 方法 | 路径 | 请求 | 响应 | 说明 |
|------|------|------|------|------|
| POST | `/api/restore` | Body: RestoreRequest | `APIResponse<RestoreResult>` | 恢复文件（4h 独立超时） |
| POST | `/api/gc` | - | `APIResponse<{status}>` | 触发垃圾回收（异步） |
| POST | `/api/reconcile?dry_run=` | Query: dry_run (默认 true) | `APIResponse<ReconcileReport>` | **数据一致性对账**（备份运行中返回 409） |
| GET | `/api/storage/health` | - | `APIResponse<{latency_ms, ok}>` (200/503) | OSS 连通性健康检查 |

---

## 7. 核心业务流程

### 7.0 应用启动与崩溃恢复流程

```
main() 启动:
  1. 加载配置 (config.Load)
  2. 初始化日志 (logger.Init)
  3. 打开数据库 (db.Open, 执行 migrations)
  4. 清理崩溃残留: BackupRepo.CleanupStaleRunning()
     → 将所有 status=pending/running 的记录标记为 failed
  5. 初始化各模块 (scanner/dedup/compress/encryptor/storage)
  6. 创建 ProgressBroker (SSE 进度中心)
  7. 创建 Engine (注入 ProgressBroker)
  8. 初始化调度器 (scheduler.New)
  9. 创建 API Router (注入 engine 等)
  10. 启动 HTTP 服务器
  11. 启动调度器 (scheduler.Start)
  12. 等待 SIGINT/SIGTERM → 优雅关闭
```

### 7.1 全量备份流程（含 SSE 进度推送）

```
用户触发 → POST /api/backup/trigger {type: "full"}
  → 锁内双重检查（内存 runningBackupID + DB IsRunning）
  → Engine.StartBackup(full)
    → 创建 BackupRecord (status=pending)
    → goroutine 异步执行 executeBackup:
      Phase 1: status → running，推送 phase=scanning 事件
      Phase 2: Scanner.ScanWithProgress() 扫描所有启用目录
               → 进度 0-5%（对数曲线估算），每 500 文件推送日志
      Phase 3: Scanner.ComputeHashes() 计算哈希
               → 进度 5-30%，逐文件推送 file + log 事件（仅 SSE）
               → 新哈希==旧哈希时降级为 Unchanged
      Phase 4: 分类变更
               → 全量备份：Unchanged 也按 Modified 处理（重建映射）
               → 用 pendingByHash 暂存本批次文件元数据
      Phase 5: Dedup.Deduplicate() 去重 → 进度 30-35%
      Phase 6: 逐文件处理 (compress → encrypt → upload → verify):
               → 进度 35-95%，逐文件推送详细日志
               → 本批次去重：直接从 pendingByHash 获取元数据，不上传
               → 跨批次去重：查 HashIndex 获取元数据
               → 新文件：完整处理流程
      Phase 7: (已合并到 Phase 6)
      Phase 8: 批量写入 backup_files（事务）
      Phase 9: 标记删除文件 + 事务内递减引用计数 → 进度 95-100%
      Phase 10: 更新备份统计
      结束: 推送 completed/failed/cancelled 事件
           → 30 秒后 ClearHistory()
```

### 7.2 增量备份流程

与全量备份的区别：
- `determineBackupType()` 判断：超过 full_reset_interval 则自动升级为全量
- `base_backup_id` 指向最近的全量备份
- Phase 4 中 Unchanged 文件被跳过（`skippedInc++`），不重建映射
- 只处理 Added 和 Modified 文件
- Deleted 文件仍标记删除（引用计数递减）

### 7.3 定时调度流程

```
Scheduler.Start() → 注册 cron 任务
  → 每次触发:
    → determineBackupType():
      full_reset_interval == 0 → 永远增量
      最近全量不存在/失败/超过间隔 → full
      否则 → incremental
    → 根据判断结果调用 RunFullBackup / RunIncrementalBackup
```

### 7.4 文件恢复流程

```
POST /api/restore {paths/pattern, output_dir, backup_id?, expedited?}
  → 使用 context.Background() + 4h 超时（独立于 HTTP 请求）
  → Restorer.Restore()
    → resolveFiles(): 按路径或模式查找文件
    → 逐文件:
      → resolveBackupFile(): 查找备份文件记录
      → restoreFile():
        1. 检查解冻状态 (CheckRestored)
        2. 如需解冻: RestoreObject + 轮询等待（最多30分钟）
        3. 下载 (rclone copyto, 3次重试)
        4. 解密 (AES-256-GCM 流式)
        5. 解压 (zstd, 如有压缩)
        6. SHA-256 哈希验证
        7. 移动到输出目录
```

### 7.5 垃圾回收流程

```
POST /api/gc
  → 异步执行 Engine.RunGarbageCollection()
    → 获取超过 orphan_grace_days 的孤儿哈希记录 (ref_count=0)
    → 逐个删除 OSS 对象
    → 批量删除数据库哈希记录
```

### 7.6 数据一致性对账流程

```
POST /api/reconcile?dry_run=true
  → 获取 engine.mu 锁（与备份互斥）
  → 备份运行中 → 409 Conflict
  → Reconciler.Reconcile(ctx, dryRun):
    1. 查询 hash_index 所有记录
    2. 查询 OSS 所有对象（rclone lsjson，分批）
    3. 查询 backup_files 所有 storage_key
    4. 按 hash 统计 files 表活跃文件数（CountActiveByHash）
    5. 检查各层不一致:
       a. OSS 有但 hash_index 无 → OSSOnlyOrphans（仅告警）
       b. hash_index 有但 OSS 无 → DanglingHashIndexes
          - ref=0: 删除 hash_index（修复）
          - ref>0: 错误（不修复）
       c. ref_count 与实际活跃文件数不符 → SetRefCount（修复）
       d. backup_files 指向的 hash_index 不存在:
          - OSS 有对象 → 重建 hash_index（修复）
          - OSS 无对象 → 删除 backup_files（修复）
       e. failed 备份有 backup_files → 修正为 completed（修复）
       f. completed 备份无 backup_files → 修正为 failed（修复）
    6. dryRun=true 时跳过所有修复操作
    7. 返回 ReconcileReport
  → 释放锁
```

### 7.7 SSE 进度推送流程

```
前端 EventSource 连接 → GET /api/backup/progress/stream
  → handleBackupProgressStream:
    1. 设置 Content-Type: text/event-stream
    2. 禁用写超时 (SetWriteDeadline(0))
    3. ProgressBroker.Subscribe() → 获取 channel + 历史事件
    4. 回放历史事件（按 Timestamp 排序）
    5. 循环:
       - 收到事件 → data: JSON\n\n 写入
       - 15 秒无事件 → : heartbeat\n\n 保活
       - 客户端断开 → unsub() 清理 channel
```

---

## 8. 依赖关系图

### 后端包依赖

```
main → config, logger, db, scanner, dedup, compress, crypto, storage, backup, scheduler, api

api → backup, config, db, models, scheduler

backup → compress, config, crypto, db, dedup, models, scanner, storage

scanner → db, models

dedup → db, scanner, models

compress → models

crypto → golang.org/x/crypto/hkdf

storage → config, aliyun-oss-go-sdk

scheduler → backup, config, db

db → models

logger → (标准库)
```

### Go 外部依赖

| 依赖 | 版本 | 用途 |
|------|------|------|
| `github.com/mattn/go-sqlite3` | v1.14.22 | SQLite 驱动 |
| `github.com/robfig/cron/v3` | v3.0.1 | Cron 调度 |
| `github.com/aliyun/aliyun-oss-go-sdk` | v2.2.9 | 阿里云 OSS SDK |
| `golang.org/x/crypto` | v0.17.0 | HKDF 密钥派生 |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML 解析 |

### 前端依赖

| 依赖 | 版本 | 用途 |
|------|------|------|
| `react` | 18.3 | UI 框架 |
| `react-dom` | 18.3 | DOM 渲染 |
| `react-router-dom` | 7.3 | 路由 |
| `zustand` | 5.0 | 状态管理 |
| `lucide-react` | 0.511 | 图标库 |
| `clsx` | 2.1 | 类名工具 |
| `tailwind-merge` | 3.0 | Tailwind 类名合并 |

---

## 9. 项目运行方式

### 后端

```bash
# 编译
cd nas-backup-backend
go build -o nas-backup ./cmd/nas-backup

# 准备配置
cp config.yaml.example config.yaml
# 编辑 config.yaml，配置 OSS 凭证、备份目录等

# 安装依赖工具
# zstd: brew install zstd / apt install zstd
# rclone: brew install rclone / apt install rclone

# 运行
./nas-backup -config config.yaml
```

默认监听 `0.0.0.0:8080`。

### 前端

```bash
cd nas-backup-frontend
npm install
npm run dev      # 开发模式 (http://localhost:5173)
npm run build    # 生产构建
npm run preview  # 预览构建结果
```

开发模式下，`/api` 请求自动代理到 `http://localhost:8080`。

### 生产部署

1. 后端编译为二进制，配置 systemd 服务
2. 前端构建为静态文件，由 Nginx 反向代理
3. Nginx 配置：`/` → 前端静态文件，`/api` → 后端服务
4. **SSE 特别配置**：`/api/backup/progress/stream` 需要：
   - `proxy_buffering off;` — 禁用缓冲，实时推送
   - `proxy_read_timeout 3600s;` — 1 小时长连接超时
   - `proxy_set_header Connection '';` — 不发送 Connection: close
   - `chunked_transfer_encoding on;`
   - `tcp_nopush off;`
   - `add_header Cache-Control 'no-cache';`
   - `add_header X-Accel-Buffering no;`（告诉 Nginx 不要缓冲）

详见 [DEPLOYMENT.md](file:///Users/jacobzhang/工作区/code/nasbkup_system/DEPLOYMENT.md)

---

## 10. 辅助脚本

### backup.sh

**文件**: `nas-backup-backend/scripts/backup.sh`

CLI 触发备份的便捷脚本，支持：
- `./backup.sh full` — 全量备份
- `./backup.sh incremental` — 增量备份
- `./backup.sh -c /path/to/config` — 指定配置文件

### setup-rclone.sh

**文件**: `nas-backup-backend/scripts/setup-rclone.sh`

配置 rclone 远程存储：
- 创建原始 OSS 远程 `[oss]`
- 创建加密远程 `[oss-crypt]`
- 交互式输入 OSS 凭证

### nas_file_generator.py

**文件**: `nas_file_generator.py`

测试数据生成器，用于生成模拟 NAS 文件系统：
- 按文件数量生成：`python nas_file_generator.py --count 1000 --output /tmp/nas_test`
- 按总大小生成：`python nas_file_generator.py --size 10GB --output /tmp/nas_test`
- 模拟不同文件类型（文档、图片、视频、代码等）
- 模拟目录结构

---

## 附录：关键设计决策

1. **SQLite 而非 PostgreSQL**：单机部署场景，零依赖，WAL 模式提供足够并发
2. **rclone 而非纯 SDK 上传**：利用 rclone 的成熟传输逻辑（断点续传、加密远程、多线程）
3. **内容寻址存储**：存储键基于内容哈希，天然支持去重
4. **HKDF 密钥派生**：每文件独立 DEK，salt 随机生成，前向安全性
5. **流式加密**：256KB 分块，避免大文件占用过多内存
6. **配置双层存储**：YAML 文件提供默认值，数据库 config_kv 支持运行时修改
7. **引用计数去重**：hash_index.ref_count 跟踪内容引用，支持垃圾回收
8. **冷归档解冻**：恢复时自动检测并触发解冻，支持标准/加急两种模式
9. **SSE 而非 WebSocket**：备份进度是单向推送（服务器→客户端），SSE 更轻量、自动重连、HTTP 友好，无需额外协议升级
10. **锁内双重检查防竞态**：StartBackup 在互斥锁内同时检查内存状态和数据库状态，防止并发触发多个备份
11. **崩溃恢复设计**：启动时 CleanupStaleRunning 清理残留记录；全量备份重处理 Unchanged 文件以重建映射；取消兜底检查 DB 残留
12. **对账分层安全**：DryRun 模式先审计再修复；OSS 孤儿对象不自动删除（避免误删）；DB 层不一致自动修复；修复操作均在事务内
13. **同批次去重元数据缓存**：使用 pendingByHash map 避免同批次重复文件重复查询 hash_index，减少 DB 往返
14. **进度加权分段估算**：扫描阶段使用对数曲线估算进度（文件数未知前无法精确），哈希/上传阶段按已处理文件比例计算，上传阶段占主要权重（35-95%）
15. **恢复独立超时 context**：使用 context.Background() + 4h 超时而非 HTTP 请求 context，避免客户端断连中断大文件恢复/解冻等待
16. **PATCH 而非 PUT 目录更新**：启用/禁用切换只需传 enabled 字段，无需发送完整对象，语义更准确

---

## 11. 下云恢复指南 (restore-cli)

> 本章描述将云端加密对象还原为本地原始文件的完整流程。下云恢复与上云备份构成完整闭环：**上传 (压缩→加密→上云) ↔ 下云 (解冻→下载→解密→解压→哈希校验)**。

### 11.1 工具与能力概览

| 工具 | 路径 | 适用场景 |
|------|------|---------|
| **restore-cli** | `nas-backup-backend/cmd/restore-cli/main.go` | 推荐：命令行下云恢复、闭环验证、ColdArchive 解冻场景（不受 HTTP 超时限制） |
| **HTTP API** | `POST /api/restore` | 程序化集成、小文件快速恢复（4h 超时） |

**restore-cli 复用与生产 API 完全相同的 `Restorer`**，因此任何在 CLI 验证通过的恢复路径在生产 API 中行为一致。

#### 支持的命令

| 命令 | 说明 |
|------|------|
| `backups` | 列出最近 20 个备份会话（ID / 类型 / 状态 / 文件数 / 大小 / 完成时间） |
| `list [dir-path]` | 列出可恢复文件，可按目录过滤，可指定 `--backup-id` 只列该备份中的文件 |
| `info <path>` | 显示某路径的文件记录 + 备份元数据（含闭环无损检查、存储压缩比） |
| `verify <path>` | 单文件闭环验证：下载→解密→解压→SHA-256 哈希校验，落在临时目录，自动清理 |
| `verify-dir <dir> [--limit N]` | 批量抽检目录下文件，N=0 表示全量 |
| `restore <path> -o <outdir>` | 恢复单文件到 outdir |
| `restore-dir <dir> -o <outdir> [--limit N]` | 恢复目录下所有文件到 outdir |

#### 通用 flags

| Flag | 默认值 | 说明 |
|------|--------|------|
| `-config` | `config.yaml` | 配置文件路径 |
| `--backup-id N` | 0（最近完成） | 指定备份会话 ID |
| `--expedited` | false | ColdArchive 加急解冻（1-10 分钟，需 OSS 白名单） |
| `-o <dir>` | — | 恢复输出目录（restore/restore-dir 必填） |
| `--limit N` | 0（全部） | verify-dir/restore-dir 最多处理的文件数 |

### 11.2 前置准备

1. **配置就绪**：`config.yaml` 中 OSS `endpoint` / `bucket` / AK 已配置，`encryption.key_file_path` 指向 `master.key`。
   > ⚠️ **密钥一致性**：恢复用的 `master.key` 必须与备份时使用的是同一把，否则 AES-256-GCM 解密必败（报 `cipher: message authentication failed`）。建议对主密钥做异地备份。
2. **数据库可用**：`data/nas-backup.db` 存在且包含已 `completed` 的备份记录。
3. **依赖工具**：`zstd` 和 `rclone` 已安装并在 PATH 中。
4. **网络**：恢复机能访问 OSS endpoint。
5. **输出目录**：恢复目标目录已创建且进程有写权限。

### 11.3 构建 restore-cli

```bash
cd nas-backup-backend
go build -o restore-cli ./cmd/restore-cli
```

构建产物为 `nas-backup-backend/restore-cli` 二进制。

### 11.4 闭环验证 SOP（验证备份可恢复，不污染正式目录）

**步骤 1：确认可用备份**

```bash
./restore-cli -config config.yaml backups
```
输出示例：
```
ID     TYPE         STATUS       FILES      SIZE         COMPLETED_AT
--------------------------------------------------------------------------------
5      full         completed    1200       2.3 GB       2026-07-04 21:00:00
4      incremental  completed    35         120 MB       2026-07-04 09:00:00
```

**步骤 2：列出指定备份中的可恢复文件**

```bash
# 列出备份 #5 中 /data/docs 下的文件
./restore-cli -config config.yaml --backup-id 5 list /data/docs

# 列出备份 #5 中的所有可恢复文件
./restore-cli -config config.yaml --backup-id 5 list
```
> 注意：`--backup-id` 真正生效，只返回该备份会话内的文件（通过 `backup_files` JOIN `files` 查询）。

**步骤 3：查看文件备份元数据**

```bash
./restore-cli -config config.yaml info /data/docs/report.pdf
```
输出包含 `Lossless: true/false`（原始大小是否等于备份记录 OriginalSize）和 `StorageRatio`（存储压缩比）。

**步骤 4：单文件闭环验证（关键）**

```bash
./restore-cli -config config.yaml --backup-id 5 verify /data/docs/report.pdf
```
执行完整闭环：下载 → AES-256-GCM 解密 → zstd 解压 → SHA-256 哈希校验。文件落在临时目录，命令结束自动清理。

预期输出：
```
Verifying "/data/docs/report.pdf" ...
  ✓ VERIFIED — hash matched, size=1048576 bytes (1.0 MB)
  Elapsed: 3.2s
```
出现 `✓ VERIFIED` 即证明该文件从云端加密对象可完整无损还原。

**步骤 5：批量抽检目录**

```bash
# 抽样 20 个文件
./restore-cli -config config.yaml --backup-id 5 verify-dir /data/docs --limit 20

# 全量验证
./restore-cli -config config.yaml --backup-id 5 verify-dir /data/docs --limit 0
```
查看 `Verify Summary`，`Failed: 0` 表示闭环完整。

### 11.5 正式下云恢复 SOP

**单文件恢复**

```bash
./restore-cli -config config.yaml --backup-id 5 \
  restore /data/docs/report.pdf -o /restore
```
结果路径：`/restore/docs/report.pdf`（保留父目录名，剥离祖父目录）。

**目录恢复**

```bash
./restore-cli -config config.yaml --backup-id 5 \
  restore-dir /data/docs -o /restore
```
结果：目录结构按公共前缀保留在 `/restore` 下。例：`/data/docs/a.txt` 和 `/data/docs/sub/b.txt` 恢复为 `/restore/docs/a.txt` 和 `/restore/docs/sub/b.txt`。

**全备份恢复**

```bash
./restore-cli -config config.yaml --backup-id 5 \
  restore-dir / -o /restore
```

查看 `Restore Summary` 确认 `Failed: 0`：
```
── Restore Summary ──────────────────────────
  Total:    1200
  Restored: 1200  ✓
  Failed:   0  ✗
  Size:     2.3 GB
  Elapsed:  8m42s
```

### 11.6 ColdArchive/Archive 归档存储恢复要点

归档对象需先「解冻」才能下载，restore-cli 自动处理，但需注意耗时：

| 解冻方式 | 耗时 | 命令 flag | 适用 |
|---------|------|----------|------|
| Expedited（加急） | 1–10 分钟 | `--expedited` | 需 OSS 白名单开通，紧急恢复 |
| Standard（标准） | 1–10 小时 | 默认 | 无需开通，但**会超过 maxThawWait** |

⚠️ **重要限制**：当前 `maxThawWait = 30 分钟`（见 [restore.go:25](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go#L24-L28)）。Standard 解冻需 1–10 小时会超过此上限报错 `object not restored after 30m0s`。

**建议**：
- ColdArchive 恢复一律加 `--expedited`
- 或将常需恢复的数据存为 Standard/IA 存储类别
- 若必须用 Standard 解冻，需手动调整 `maxThawWait` 常量并重新编译

解冻轮询机制（[restore.go:255-272](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go#L250-L276)）：
- 每 30 秒轮询 OSS 检查 `X-Oss-Restore` 头
- 使用 `select` + `time.After`，支持上下文取消即时响应
- 解冻恢复窗口为 7 天（OSS RestoreConfiguration.Days=7）

### 11.7 HTTP API 恢复方式

适合程序化集成或前端调用：

```bash
curl -X POST http://localhost:8080/api/restore \
  -H 'Content-Type: application/json' \
  -d '{
    "paths": ["/data/docs/report.pdf"],
    "backup_id": 5,
    "output_dir": "/restore",
    "expedited": false
  }'
```

响应：
```json
{
  "success": true,
  "data": {
    "total_files": 1,
    "restored_files": 1,
    "failed_files": [],
    "total_size": 1048576,
    "elapsed_ms": 3200
  }
}
```

> **HTTP 与 CLI 差异**：API 使用独立 `context.Background()` + 4h 超时（见 [restore_handler.go:21](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/api/restore_handler.go#L15-L21)），客户端断连不中断恢复。但 ColdArchive 解冻仍建议用 restore-cli（更适合长耗时操作）。

### 11.8 故障排查

| 现象 | 根因 / 处理 |
|------|------------|
| `decrypt chunk 0: cipher: message authentication failed` | 主密钥不匹配（master.key 非备份时所用）；或密文在云端被篡改/损坏 |
| `object not restored after 30m0s` | ColdArchive 标准解冻超时 → 改用 `--expedited`，或调大 `maxThawWait` |
| `no backup file record found` | 该文件不在指定 backupID 的备份中 → 用 `list --backup-id N` 核对 |
| `hash verification failed` | 云端对象被篡改/损坏，或 compressType 元数据异常 |
| `rename ...: permission denied` | 输出目录权限不足（修复后不再静默覆盖已存在文件） |
| `restore failed: context deadline exceeded` | 4h 总超时（API）或文件数过多 → 拆分批次或用 CLI |
| `rclone: command not found` | rclone 未安装或不在 PATH |

### 11.9 闭环完整性自检清单

每次重大恢复前建议完成以下检查：

- [ ] `info <path>` 输出 `Lossless: true`（原始大小 = 备份记录 OriginalSize）
- [ ] `verify <path>` 输出 `✓ VERIFIED — hash matched`（证明 AES-256-GCM 解密 + zstd 解压无损）
- [ ] 恢复后文件 SHA-256 与 DB 中 `files.hash` 一致（用 `sha256sum` 对比）
- [ ] 恢复目录结构与源一致（多文件保留公共前缀下相对结构，单文件保留父目录名）
- [ ] `Restore Summary` 中 `Failed: 0`
- [ ] 恢复后抽样文件可正常打开/解析（无损坏）

### 11.10 恢复流程源码索引

| 阶段 | 源码位置 |
|------|---------|
| 入口（CLI） | [cmd/restore-cli/main.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/cmd/restore-cli/main.go) |
| 入口（API） | [internal/api/restore_handler.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/api/restore_handler.go) |
| Restorer 主流程 | [internal/backup/restore.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go) — `Restore()`, `restoreFile()` |
| 解冻轮询 | [restore.go:255-272](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go#L250-L276) |
| 解密（分块 GCM） | [internal/crypto/crypto.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/crypto/crypto.go) — `DecryptFile()` |
| 解压（zstd） | [internal/compress/compress.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/compress/compress.go) — `Decompress()` |
| 哈希校验 | [restore.go:sha256File](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go) |
| 落盘（moveFile） | [restore.go:moveFile](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/restore.go) |
| 文件列表查询 | [internal/db/file_repo.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/db/file_repo.go) — `ListActiveByBackup()`, `ListActiveByDirectory()` |
| 备份文件元数据 | [internal/db/backup_repo.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/db/backup_repo.go) — `GetBackupFileByFileID()`, `GetFileRestoreInfo()` |
| OSS 解冻 | [internal/storage/storage.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/storage/storage.go) — `RestoreObject()`, `CheckRestored()` |

---

## 12. 前端页面名词解释（产品视角）

> 本章从产品/用户视角解释前端四个页面（全览 / 内容选择 / 策略设置 / 日志）中出现的关键名词、指标和状态，帮助用户理解每个数字和标签背后的准确含义及计算口径。所有解释均与后端 [dashboard_handler.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/api/dashboard_handler.go)、[models.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/models/models.go) 中的实际逻辑对齐。

### 12.1 全览页（Dashboard）

#### 12.1.1 状态横幅

| 名词 | 含义 |
|------|------|
| **备份运行中** | 当前有一个备份会话处于 `running` 状态。判断逻辑为「数据库中有 running 记录 **或** 引擎内存中有运行中的 backupID」（OR 逻辑），避免进程崩溃后状态不一致导致误判为空闲。 |
| **系统空闲** | 数据库与内存中均无运行中的备份。 |
| **上次备份时间** | 最近一次状态为 `completed` 的备份会话的 `completed_at` 时间，以相对时间展示（如「3 分钟前」）。 |
| **下次备份时间** | 调度器启用时，根据 cron 表达式计算的下一次触发时间。调度器未启用时不显示。 |
| **上次备份状态** | 最近一次完成备份的最终状态徽章（completed / failed / cancelled）。 |

#### 12.1.2 进度阶段（备份运行时显示）

进度条按阶段加权显示，反映备份引擎的执行流水线：

| 阶段 | 范围 | 含义 |
|------|------|------|
| **扫描文件**（scanning） | 0% | 遍历所有启用的备份目录，识别新增/修改/删除/未变更文件。 |
| **计算哈希**（hashing） | 0–25% | 对新增和修改文件计算 SHA-256 内容哈希，用于去重和完整性校验。 |
| **去重分析**（deduplicating） | 25–30% | 查询全局哈希索引，判断每个文件内容是否已存在云端。 |
| **上传文件**（uploading） | 30–95% | 对需要上传的文件依次执行「压缩 → 加密 → 上传 OSS → 完整性校验」。 |
| **完成收尾**（finalizing） | 95–100% | 写入备份-文件关联、标记删除文件、递减孤儿引用计数、更新统计。 |
| **completed / failed / cancelled** | 终态 | 备份会话结束（成功 / 失败 / 被取消）。 |

> 进度百分比、当前处理文件、文件计数（current / total）通过 SSE 实时推送，前端无需轮询。

#### 12.1.3 仪表盘图表（3 列）

三个圆弧仪表盘共享同一个分母「**总文件大小**」（活跃文件原始字节数），便于横向对比各项优化效果：

| 名词 | 计算公式 | 产品含义 |
|------|---------|----------|
| **OSS 存储使用** | `SUM(stored_size)` over `DISTINCT storage_key` in `backup_files` | 当前云端 OSS 实际占用的字节数（已压缩 + 已加密 + 已去重）。这是用户每月 OSS 账单的依据。 |
| **OSS 存储使用百分比** | `OSS 存储使用 / 总文件大小 × 100%` | 云端实际占用占原始数据大小的比例。该值越低，说明去重 + 压缩节省效果越好；通常远低于 100%。 |
| **去重节省** | `SUM(file_size × (ref_count − 1))` over `hash_index WHERE ref_count > 1` | 通过内容寻址去重避免重复上传的字节数。同一内容被 N 个文件引用时，节省 = 文件大小 × (N−1)。 |
| **去重节省百分比** | `去重节省 / 总文件大小 × 100%` | 重复内容在原始数据中的占比，反映数据集的重复度。 |
| **压缩节省** | 各备份会话中 `SUM(original_size − stored_size)` 的累计值（每文件仅统计首次上传时） | 通过 zstd 压缩减少的字节数。仅统计被压缩的文件，未压缩文件（视频、图片、已压缩归档等）不计入。 |
| **压缩节省百分比** | `压缩节省 / 总文件大小 × 100%` | 压缩带来的整体缩减比例。 |

> ⚠️ **已知限制**：`saved_by_compress` 字段目前在 [dashboard_handler.go](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/api/dashboard_handler.go) 中未赋值，前端图表当前显示 0。压缩节省实际数据已记录在每个备份会话的 `compress_saved` 列中，待后端聚合后即可显示。

#### 12.1.4 统计卡片（4 列）

| 名词 | 字段 | 计算口径 | 产品含义 |
|------|------|---------|----------|
| **活跃文件** | `total_files` | `COUNT(*) FROM files WHERE status = 'active'` | 当前被系统追踪的文件总数（未标记删除）。代表 NAS 上纳入备份范围的有效文件数量。 |
| **已备份文件** | `backed_up_files` | `COUNT(DISTINCT file_id) FROM backup_files` | 至少有过一次成功上传到 OSS 的不同文件数。同一文件多次备份只计一次。 |
| **总文件大小** | `total_size` | `SUM(size) FROM files WHERE status = 'active'` | 所有活跃文件的原始大小总和（未压缩、未去重）。是仪表盘三个图表的共同分母。 |
| **已备份大小** | `backed_up_size` | 当前实现 = `OSSStorageUsed`（云端实际占用） | 字段名暗示「已备份文件原始大小总和」，但当前后端实现复用了 `OSSStorageUsed`（云端实际占用）。因此该值通常**小于**「活跃文件中已备份部分的原始大小」。 |

> 说明：若用户期望「已备份大小」展示为「已备份文件原始大小总和」，需在后端单独查询 `SUM(size) FROM files WHERE id IN (SELECT DISTINCT file_id FROM backup_files) AND status = 'active'`。

#### 12.1.5 备份历史表格列

| 列名 | 字段 | 含义 |
|------|------|------|
| **ID** | `id` | 备份会话主键，可用于日志页过滤。 |
| **类型** | `type` | `full`（全量）/ `incremental`（增量）。 |
| **状态** | `status` | 备份会话状态徽章。 |
| **文件数** | `total_files` | 本次会话处理的文件数（含新增、修改、未变更、去重跳过，不含已删除）。 |
| **大小** | `total_size` | 本次会话处理文件的原始大小总和。 |
| **上传量** | `uploaded_size` | 本次会话实际写入 OSS 的字节数（压缩 + 加密后），不含去重跳过的文件。 |
| **去重跳过** | `skipped_dedup` | 因内容已存在于云端而被跳过上传的文件数。 |
| **开始时间** | `started_at` | 会话进入 `running` 状态的时间。 |
| **完成时间** | `completed_at` | 会话进入终态（completed/failed/cancelled）的时间。 |

#### 12.1.6 操作按钮

| 按钮 | 行为 |
|------|------|
| **立即备份** | 触发一次备份会话。默认类型由调度策略决定（全量周期到则全量，否则增量）。 |
| **取消备份** | 取消当前运行中的备份，会话状态置为 `cancelled`，已上传文件保留。需二次确认。 |
| **垃圾回收** | 清理云端 ref_count=0 且超过孤儿宽限期的对象，释放 OSS 存储空间。需二次确认。 |

### 12.2 内容选择页（Content）

#### 12.2.1 文件浏览器状态标签

| 标签 | 字段 | 含义 |
|------|------|------|
| **已纳入** | `in_backup = true, partial_backup = false` | 该目录完整位于某个启用的备份目录范围内（递归）。 |
| **部分纳入** | `in_backup = true, partial_backup = true` | 该目录在备份范围内，但存在启用的排除规则会跳过其中部分文件/子目录。 |
| **未纳入** | `in_backup = false` | 该目录不在任何启用的备份目录范围内。 |

#### 12.2.2 文件详情面板字段

| 字段 | 含义 |
|------|------|
| **在备份范围** | 同上「已纳入 / 部分 / 否」。 |
| **有更新** | 该文件自上次备份以来 mtime 或 size 发生变化（`has_update = true`），下次备份会重新上传。 |
| **将会备份** | 综合判断：该路径既在备份范围内、又未被排除规则命中，且（若是文件）有更新或尚未备份过。表示「下次备份会处理该文件」。 |

#### 12.2.3 排除规则类型

| 类型 | 标签 | 匹配方式 |
|------|------|---------|
| `extension` | 扩展名 | 按文件扩展名匹配（忽略大小写），如 `*.tmp`。 |
| `directory` | 目录 | 按路径组件匹配，命中后整个目录树跳过，如 `node_modules`。 |
| `pattern` | 模式 | 文件名 glob 匹配（filepath.Match），如 `.DS_Store`。 |
| `size_exceed` | 大小超限 | 超过 `MaxFileSize` 上限的文件跳过备份。 |

### 12.3 策略设置页（Strategy）

#### 12.3.1 调度配置

| 名词 | 含义 |
|------|------|
| **启用定时任务** | 是否启用 cron 自动备份。关闭后只能手动触发。 |
| **Cron 表达式** | 标准 5 段式 cron：`分 时 日 月 周`。如 `0 3 1 * *` 表示每月 1 日 03:00 触发。 |
| **时区** | cron 表达式所使用的时区。建议与 NAS 实际时区一致。 |

#### 12.3.2 压缩配置

| 名词 | 含义 |
|------|------|
| **启用压缩** | 是否对可压缩文件执行 zstd 压缩。关闭后所有文件原样上传（仍会加密）。 |
| **算法** | 压缩算法，当前固定为 `zstd`（只读）。 |
| **压缩级别** | zstd 级别 1–22。级别越高压缩率越好但越慢；推荐 3–9。 |
| **跳过类型** | 不进行压缩的文件扩展名列表（视频、图片、已压缩归档等），避免无效压缩浪费 CPU。 |

#### 12.3.3 上传配置

| 名词 | 含义 |
|------|------|
| **存储类型** | OSS 存储类别：`ColdArchive`（冷归档，最便宜，恢复需解冻）/ `Archive`（归档）。 |
| **并发数** | 同时上传的文件数。增大可加速但占用更多带宽和内存。 |
| **分块大小(MB)** | rclone 分块上传的单块大小，影响大文件传输效率和内存占用。 |
| **重试次数** | 单文件上传失败后的重试次数（指数退避 2s → 4s → 8s）。 |
| **重试延迟(秒)** | 首次重试前的等待秒数。 |

#### 12.3.4 保留策略

| 名词 | 含义 |
|------|------|
| **版本保留数** | 每个文件保留的历史版本数。`1` = 仅保留最新版本（仍受引用计数保护）。 |
| **孤儿数据清理天数** | `ref_count=0` 的孤儿对象在 OSS 上保留的天数，超过后由垃圾回收删除。 |
| **全量备份间隔(月)** | 强制触发全量备份的周期。超过该周期未做全量时，下次调度自动转为全量。 |
| **已删除文件保留天数** | NAS 上被删除的文件在数据库中保持 `deleted` 状态的天数，用于误删恢复窗口。 |

#### 12.3.5 加密配置

| 名词 | 含义 |
|------|------|
| **加密算法** | 固定为 `AES-256-GCM`（只读），每文件通过 HKDF 派生独立 DEK。 |
| **密钥文件路径** | 主密钥文件（32 字节）路径。**务必异地备份**，丢失后云端数据无法解密。 |

### 12.4 日志页（Logs）

#### 12.4.1 日志级别

| 级别 | 颜色 | 含义 |
|------|------|------|
| **DEBUG** | 灰色 | 调试细节，生产环境通常不记录。 |
| **INFO** | 蓝色 | 正常流程信息（阶段开始/结束、文件处理进度等）。 |
| **WARN** | 黄色 | 非致命异常（如单个文件跳过、重试成功、HTTP 4xx 客户端错误）。 |
| **ERROR** | 红色 | 严重错误（如上传失败、哈希校验失败、HTTP 5xx 服务端错误）。 |

#### 12.4.2 筛选条件

| 字段 | 含义 |
|------|------|
| **级别** | 按日志级别过滤。 |
| **搜索** | 对 `message` 和 `detail` 字段做模糊匹配。 |
| **备份 ID** | 仅显示指定备份会话的日志。 |
| **时间范围** | 按 `created_at` 过滤起止时间。 |

### 12.5 备份类型与状态（跨页面通用）

#### 12.5.1 备份类型

| 类型 | 含义 |
|------|------|
| **全量**（full） | 扫描所有启用目录，处理全部活跃文件（含未变更），刷新所有引用计数。首次备份必须为全量。 |
| **增量**（incremental） | 仅处理新增和修改文件，跳过未变更文件（`skipped_inc` 计数）。必须基于一个已完成的全量备份。 |

#### 12.5.2 备份状态

| 状态 | 含义 |
|------|------|
| **等待中**（pending） | 会话已创建但尚未开始执行（短暂中间态）。 |
| **运行中**（running） | 引擎正在执行备份流水线。 |
| **已完成**（completed） | 备份成功结束，所有阶段通过。 |
| **失败**（failed） | 备份因错误终止，`error_message` 字段记录原因。 |
| **已取消**（cancelled） | 用户主动取消，已上传内容保留但会话不再继续。 |

### 12.6 指标关系速查

```
总文件大小（活跃文件原始大小）
├── 已上传到 OSS 的部分（原始大小）
│   ├── 去重节省（重复内容未上传） ──→ 不计入 OSS
│   └── 实际上传部分
│       ├── 压缩节省（zstd 缩减）   ──→ 不计入 OSS
│       └── OSS 存储使用（加密后实际占用）──→ 计入 OSS 账单
└── 未上传部分（新增/未变更/排除）
```

**关键不等式**（通常情况）：

```
OSS 存储使用  ≤  已上传部分原始大小  ≤  总文件大小
压缩节省 + OSS 存储使用  ≈  已上传部分原始大小（去重后）
去重节省 + 已上传部分原始大小  ≈  总文件大小 × 重复率
```
