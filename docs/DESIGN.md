# 技术设计文档（DESIGN）

> **变更主题**：恢复页面解耦本地 + OSS DB 单一权威源 + 剔除版本概念 + 跨环境协作
> **状态**：Architect 阶段，待舵手"架构确认OK"后进入 Atomize
> **日期**：2026-08-08
> **依据**：[docs/ALIGNMENT.md](file:///Users/jacobzhang/工作区/code/nasbkup_system/docs/ALIGNMENT.md) v2

---

## 1. 设计原则

1. **单一 DB 概念**：本地 `data/nas-backup.db` 是 OSS `meta/db/oss.db` 的工作副本，同一份数据的两个位置
2. **OSS 为权威源**：启动时从 OSS 拉取覆盖本地；备份完成后上传新 oss.db 到 OSS
3. **无版本**：删除 BackupType/BaseBackupID/version_keep_count/dbBackupKeepVersions，每次备份就是"一次备份"
4. **直接覆盖，无 merge**：拉取 OSS DB 直接覆盖本地，避免多分支风险
5. **本地专属表不同步**：`restore_jobs`（恢复作业记录）只存本地，不上传 OSS
6. **最小改动**：复用现有 `db_backup.go` 的加解密机制，重构而非重写

---

## 2. OSS 存储布局

```
oss-crypt:
├── meta/db/
│   ├── oss.db.enc              # 权威 DB（加密）
│   ├── oss.db.iv               # 权威 DB 的 IV
│   ├── oss.db.versionYYYYMMDDHHmm.bkup.enc   # 历史 .bkup（保留最近 5 个）
│   └── oss.db.versionYYYYMMDDHHmm.bkup.iv
└── data/                       # 备份文件对象（加密哈希名，不变）
    └── <hash>/<hash>.enc
```

- **oss.db 路径**：`meta/db/oss.db.enc` + `meta/db/oss.db.iv`（复用现有 `meta/db` 前缀）
- **.bkup 命名**：`oss.db.versionYYYYMMDDHHmm.bkup.enc` + `.bkup.iv`（时间戳精确到分钟）
- **.bkup 保留数**：5（可配置 `retention.db_bkup_keep_count`，默认 5）
- **.bkup 清理时机**：上传新 oss.db 后，列 `meta/db/` 下所有 `.bkup.enc`，按时间戳降序，删除第 6 个及之后

---

## 3. DB Schema 改造

### 3.1 同步到 OSS 的表（权威数据，跨环境共享）

| 表 | 说明 | 改动 |
|---|---|---|
| `files` | 文件索引 | 不变（保留 004 migration 加的 last_backup_* 列） |
| `backups` | 备份记录 | **删除 `type` 列、`base_backup_id` 列、`skipped_inc` 列**；保留 status（含 completed_with_errors） |
| `backup_files` | 文件-备份关联 | 不变 |
| `hash_index` | 去重索引 | 不变（保留 ref_count，orphaned_at） |
| `backup_logs` | 备份日志 | 不变 |
| `backup_directories` | 备份目录配置 | 不变（跨环境共享，让 B 机器看到 A 的配置） |
| `exclusion_rules` | 排除规则 | 不变 |
| `config_kv` | 配置键值 | 不变 |

### 3.2 仅本地的表（不上传 OSS）

| 表 | 说明 |
|---|---|
| `restore_jobs` | 恢复作业记录（本机操作日志，不同步） |

### 3.3 直接修改 001_init.sql（无历史数据，不考虑兼容）

舵手明确：无生产数据，无需 migration 兼容。**直接修改 `001_init.sql`** 的 `backups` 表定义，删除版本相关列。删除 `004_add_file_backup_status.sql` 里 `backups` 表重建为带版本列的旧逻辑（该 migration 本就是重建表，直接改成新 schema）。

**改造后的 `backups` 表定义**（在 001_init.sql 和 004 migration 里同步）：

```sql
CREATE TABLE IF NOT EXISTS backups (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'completed', 'completed_with_errors', 'failed', 'cancelled')),
    total_files     INTEGER NOT NULL DEFAULT 0,
    total_size      INTEGER NOT NULL DEFAULT 0,
    uploaded_size   INTEGER NOT NULL DEFAULT 0,
    skipped_dedup   INTEGER NOT NULL DEFAULT 0,
    failed_files    INTEGER NOT NULL DEFAULT 0,
    compress_saved  INTEGER NOT NULL DEFAULT 0,
    started_at      TEXT,
    completed_at    TEXT,
    error_message   TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT (datetime('now'))
);
```

**删除的列**：`type`、`base_backup_id`、`skipped_inc`、`idx_backups_type` 索引。

**migration 文件处理**：
- 001_init.sql：直接改 backups 表定义为新 schema
- 004_add_file_backup_status.sql：该 migration 重建 backups 表，直接改成新 schema（无 type/base_backup_id/skipped_inc）
- 不新增 005 migration
- 现有开发环境的本地 DB 直接删除重建（`rm data/nas-backup.db`，启动自动建新）

---

## 4. 代码改动设计

### 4.1 `internal/backup/db_backup.go`（重构）

**删除**：
- `dbBackupKeepVersions` 常量
- `BackupDatabase` 的多版本时间戳命名（`nas-backup-YYYYMMDD-HHMMSS.db.enc`）
- `pruneOldVersions` 的"保留 N 个版本"逻辑
- `ListVersions` 的多版本列举
- `Bootstrap` 的"指定 version 覆盖本地"逻辑

**新增/重命名**：

```go
const (
    ossDBPrefix       = "meta/db"
    ossDBEncKey       = "meta/db/oss.db.enc"
    ossDBIVKey        = "meta/db/oss.db.iv"
    dbBkupKeepCount   = 5  // 默认，可配置
)

// PullOSSDB 从 OSS 拉取 oss.db，解密覆盖本地 DB。
// 用于启动时和"刷新"按钮。
// 若 OSS 不存在 oss.db，返回 (false, nil) 表示首次部署。
func (s *DBBackupService) PullOSSDB(ctx context.Context) (exists bool, err error)

// PushOSSDB 上传本地 DB 到 OSS 作为新 oss.db。
// 流程：旧 oss.db 改名 .bkup → 上传新 oss.db.enc + .iv → 清理超量 .bkup
// 上传前做 PRAGMA integrity_check 校验完整性。
func (s *DBBackupService) PushOSSDB(ctx context.Context) error

// OSSDBExists 检查 OSS 里是否已有 oss.db.enc
func (s *DBBackupService) OSSDBExists(ctx context.Context) (bool, error)

// pruneBkups 清理超出保留数的 .bkup（保留最近 N 个）
func (s *DBBackupService) pruneBkups(ctx context.Context) error
```

**关键算法**（PushOSSDB）：
```
1. PRAGMA wal_checkpoint(TRUNCATE)  // WAL 落盘
2. PRAGMA integrity_check           // 完整性校验，失败则不上传
3. 加密本地 DB → tmp/oss.db.enc + tmp/oss.db.iv
4. 如果 OSS 已有 oss.db.enc：
   a. 下载旧 oss.db.enc + .iv → 重命名为 oss.db.versionYYYYMMDDHHmm.bkup.enc + .bkup.iv → 上传
   b. （或更简单：OSS 端 copy，但 rclone local 不支持 server-side copy，所以下载重传）
5. 上传新 oss.db.enc + .iv 到 OSS
6. pruneBkups：列 meta/db/*.bkup.enc，按时间戳降序，删第 6+ 个
```

### 4.2 `internal/backup/engine.go`（启动流程 + 备份流程改造）

**启动流程**（新增 `InitFromOSS` 方法，在 main.go 启动 HTTP 前调用）：

```go
// InitFromOSS 启动时从 OSS 初始化本地 DB。
// OSS 可达是服务前提，不可达直接 fatal 退出。
// 10 分钟退避重试，超过仍失败则 fatal。
func (e *Engine) InitFromOSS(ctx context.Context) error {
    // 1. 探测 OSS 可达性（rclone lsf oss-crypt:）
    // 2. 重试策略：每 10 分钟重试，最多 N 次（或无限重试直到成功）
    // 3. 检查 oss.db.enc 是否存在
    //    ├─ 存在：PullOSSDB 覆盖本地 → 跑 migrations → 完成
    //    └─ 不存在：初始化空本地 DB（建表 + migrations）→ 完成（等待首次备份）
    // 4. 任何步骤失败 → fatal log + os.Exit(1)
}
```

**备份流程改造**（`executeBackup`）：

```
现有流程：
  StartBackup → executeBackup → 扫描 → 去重 → 上传文件 → 更新 DB → 完成

改造后：
  StartBackup → executeBackup:
    ├─ 1. PullOSSDB（拉取最新 oss.db 覆盖本地，基于最新状态备份）
    ├─ 2. 扫描本地待备份目录
    ├─ 3. 去重（基于 hash_index）
    ├─ 4. 上传新文件到 OSS
    ├─ 5. 更新本地 DB：backups/backup_files/hash_index/files/backup_logs
    ├─ 6. PushOSSDB（上传新 oss.db，旧的重命名 .bkup）
    └─ 完成
```

**删除的代码**：
- `RunFullBackup` / `RunIncrementalBackup` 合并为单一 `RunBackup`
- `determineBackupType` 删除（不再区分全量/增量）
- `checkBootstrapRequired` 删除（启动时自动拉取，无需 bootstrap 守卫）
- `IsBootstrapRequired` 删除
- `StartBackup` 的 `backupType` 参数删除（或保留但忽略，API 兼容）

**storage_key 生成**（[engine.go#L1286](file:///Users/jacobzhang/工作区/code/nasbkup_system/nas-backup-backend/internal/backup/engine.go#L1286) `generateStorageKey`）：
- 当前：`generateStorageKey(backupType, hash)` → 路径含 `full`/`incremental` 前缀
- 改造：`generateStorageKey(hash)` → 路径只含 hash，如 `data/<hash前2位>/<hash>.enc`

### 4.3 `internal/backup/restore.go`（恢复页面改造）

**`ListRestorableFiles`**：不变（仍查本地 DB，但本地 DB = OSS DB 副本）

**`resolveFiles` / `RestoreWithOptions`**：
- 文件路径、storage_key 等来自本地 DB（= OSS DB 副本）
- 目标路径：恢复支持"原始路径"和"自定义目录"两种模式（选项 C）
- `RestoreWithOptions` 的归一化逻辑保留，但前端不再强制 `restore_to_original=true`

### 4.4 `internal/backup/reconcile.go`（解耦恢复页面）

- 恢复页面"更新"按钮不再调 reconcile
- reconcile 作为独立功能保留（可加 `/api/maintenance/reconcile` 路由或保留 `/api/reconcile` 但前端不挂恢复页面）
- `reconcileGatherAndCompare` 的 `storage.List` 容错已修复（见本次会话开头）

### 4.5 `internal/models/models.go`（删除版本类型）

```go
// 删除
type BackupType string
const { BackupTypeFull / BackupTypeIncremental / BackupTypeAuto }

// BackupRecord 删除字段
type BackupRecord struct {
    // Type           BackupType  // 删除
    // BaseBackupID   *int64      // 删除
    // SkippedByInc   int         // 删除
    ...其他保留
}

// RestoreRequest 不变（BackupID 仍用于"从哪次备份恢复"）
```

### 4.6 `internal/config/config.go`（简化 retention）

```go
type RetentionConfig struct {
    // VersionKeepCount  int  // 删除
    // FullResetInterval int  // 删除
    OrphanGraceDays   int  // 保留（orphan hash 清理）
    KeepDeletedDays   int  // 保留（deleted file 记录保留）
    DBBkupKeepCount   int  // 新增：oss.db .bkup 保留数，默认 5
}
```

### 4.7 `cmd/nas-backup/main.go`（启动流程）

```go
func main() {
    // 1. 加载 config
    // 2. 初始化 encryptor（master.key 必须存在）
    // 3. 初始化 storage（rclone.conf 必须存在）
    // 4. engine.InitFromOSS(ctx)  // 新增：从 OSS 初始化本地 DB，不可达则 fatal
    // 5. 启动 HTTP server
}
```

### 4.8 `cmd/restore-cli/main.go`（简化）

- 删除 `bootstrap` 子命令（启动时自动拉取，不再需要）
- 保留单文件恢复调试功能

### 4.9 `internal/api/router.go` + handler

**新增**：
- `POST /api/restore/refresh-oss-db`：触发 `PullOSSDB`（恢复页面"更新"按钮调用）

**修改**：
- `POST /api/backup/trigger`：request body 的 `type` 字段忽略（兼容旧前端）
- `POST /api/reconcile`：保留但前端恢复页面不再调用

### 4.10 前端

**`Restore.tsx`**：
- "更新"按钮 → 调 `POST /api/restore/refresh-oss-db`
- 目标路径：恢复支持选择自定义目录（恢复"恢复到"选择器），默认原始路径

**`Dashboard.tsx`**：
- 移除 bootstrap 相关提示
- 备份历史展示来自 OSS DB（已经是了，因为本地=OSS 副本）

---

## 5. 关键流程时序

### 5.1 启动流程

```
main()
  ├─ loadConfig()
  ├─ crypto.NewEncryptor(master.key)  ← 必须存在
  ├─ storage.NewStorageManager(rclone.conf)  ← 必须存在
  ├─ engine.InitFromOSS(ctx):
  │     ├─ 探测 OSS 可达（rclone lsf oss-crypt:，10分钟退避重试）
  │     ├─ OSSDBExists?
  │     │   ├─ yes: PullOSSDB → 解密覆盖本地 DB → runMigrations
  │     │   └─ no:  初始化空本地 DB → runMigrations
  │     └─ 失败 → fatal log + exit(1)
  └─ http.ListenAndServe()
```

### 5.2 备份流程

```
POST /api/backup/trigger
  ├─ StartBackup()
  │   └─ executeBackup(ctx):
  │       ├─ 1. PullOSSDB（拉取最新，覆盖本地）
  │       ├─ 2. scanner.Scan()（扫描本地目录）
  │       ├─ 3. dedup.Check()（去重）
  │       ├─ 4. storage.Upload()（上传新文件）
  │       ├─ 5. db.Update()（更新 backups/backup_files/hash_index/files/logs）
  │       └─ 6. PushOSSDB（上传新 oss.db，旧 .bkup，清理超量 .bkup）
  └─ 返回 backup_id
```

### 5.3 恢复流程

```
用户打开恢复页面
  └─ GET /api/restore/files → ListRestorableFiles → 查本地 DB（=OSS DB 副本）

用户点"更新"
  └─ POST /api/restore/refresh-oss-db → PullOSSDB → 覆盖本地 → 返回成功

用户选择文件 + 目标路径 → POST /api/restore
  └─ resolveFiles（查本地 DB）→ storage.Download → 解密 → 落地到目标路径
```

---

## 6. OSS 不可达处理

**舵手决策**：OSS 不可达 = 服务不可用。

**实现**：
- 启动时 `InitFromOSS` 探测 OSS 可达性，不可达则**在 10 分钟内退避重试**（指数退避：1s, 2s, 4s, 8s... 上限 60s）
- 10 分钟内任一次重试成功 → 继续启动
- 10 分钟后仍不可达 → `slog.Error` + `os.Exit(1)` 退出，日志明确提示"OSS 不可达，服务无法启动"
- 运行时备份/恢复操作若 OSS 不可达：返回错误给用户，不自动重试（避免后台静默重试消耗资源）

---

## 7. 跨环境协作

**场景**：绿联 NAS 备份 → Mac 配相同 master.key + oss → Mac 看到备份记录

**实现**：
- NAS 备份 → 上传 oss.db（含 NAS 的备份记录、文件路径如 `/var/lib/...`）
- Mac 启动 → PullOSSDB → 本地 DB = NAS 的 DB
- Mac 恢复页面 → 查本地 DB → 显示 NAS 的文件清单（路径 `/var/lib/...`）
- Mac 恢复时 → 路径不可写 → 走 `FallbackBaseDir` 或用户指定目录
- Mac 备份 → 先 PullOSSDB（=NAS 最新）→ 扫描 Mac 本地目录 → 上传 Mac 文件 → 更新 DB（含 NAS + Mac 记录）→ PushOSSDB
- NAS 下次启动 → PullOSSDB → 看到 Mac 的备份记录

**并发**：不处理（顺序操作场景）。若两台机器同时备份，最后 PushOSSDB 获胜，前一台的 DB 更新丢失（但文件已上传 OSS，下次 reconcile 可对账补回）。

---

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 启动时 OSS 不可达导致服务无法启动 | 10 分钟退避重试，日志明确提示；运维需保证 OSS 可达 |
| PullOSSDB 覆盖本地丢失本地 restore_jobs | PullOSSDB 覆盖前导出本地 restore_jobs 数据，覆盖后导回（方案 B） |
| PushOSSDB 上传损坏 DB | 上传前 PRAGMA integrity_check 校验，失败则不上传 |
| .bkup 机制占用 OSS 空间 | 保留 5 个，每个几十 MB，可接受 |
| 跨环境路径不可写 | FallbackBaseDir + 用户指定目录 |
| 多机器并发备份最后写入获胜 | 文档说明，不处理；文件已在 OSS，DB 可通过 reconcile 补 |

### PullOSSDB 保留本地 restore_jobs（方案 B）

PullOSSDB 流程：
```
1. 备份本地 restore_jobs 表数据到内存（SELECT * → []struct）
2. 下载 oss.db → 解密 → 覆盖本地 nas-backup.db 文件
3. 重新打开本地 DB → 把 restore_jobs 数据写回（INSERT）
```

oss.db 里 restore_jobs 表 schema 与本地一致（空表），覆盖后表结构在，数据从内存导回。

---

## 9. 验证策略

### 9.1 单元测试
- `db_backup_test.go`：重写，覆盖 PullOSSDB/PushOSSDB/pruneBkups/OSSDBExists
- `engine_test.go`：改造，删除全量/增量相关测试，新增 InitFromOSS 测试
- `storage_test.go`：新增 List 容错测试（已在本次会话修复）

### 9.2 E2E
- 灾难恢复场景：删除本地 DB → 启动 → 恢复页面显示 OSS 文件
- 跨环境模拟：两个 test-env（模拟两台机器）共用一个 OSS（local-cloud-storage）→ A 备份 → B 启动看到 A 的记录
- 备份流程：备份 → 验证 oss.db.enc + .bkup 存在 → 验证 .bkup 数量 ≤ 5

### 9.3 回归
- 现有 E2E（`verify-e2e.sh`）适配新流程
- 现有单测全通过

---

## 10. 实施顺序（Atomize 阶段拆分依据）

1. **DB 层**：migration 005 + models 删除版本 + config 简化
2. **db_backup.go 重构**：PullOSSDB/PushOSSDB/pruneBkups/OSSDBExists
3. **engine.go 改造**：InitFromOSS + executeBackup 流程 + 删除版本逻辑
4. **restore/reconcile 解耦**：恢复页面用新 refresh API + reconcile 独立
5. **API 路由**：新增 /api/restore/refresh-oss-db
6. **前端**：Restore.tsx 更新按钮 + 目标路径选择
7. **main.go / restore-cli**：启动流程 + 删除 bootstrap
8. **测试**：单测 + E2E 适配
9. **配置/文档**：config.yaml 更新 + AGENTS.md 更新

---

## 11. 请舵手确认

1. **OSS 存储布局**（第 2 节）：`meta/db/oss.db.enc` + `.bkup` 方案 OK？
2. **直接改 001_init.sql**（第 3.3 节）：无生产数据，直接改 schema 删版本列，不写 migration 兼容，OK？
3. **PullOSSDB 保留 restore_jobs**（第 8 节）：方案 B（覆盖前导出/覆盖后导回）？
4. **storage_key 去类型前缀**（第 4.2 节）：`data/<hash>/<hash>.enc`，无生产数据不考虑兼容旧 key，OK？
5. **OSS 不可达处理**（第 6 节）：启动时无限重试，运行时返回错误不重试，OK？

确认后进入 Atomize 阶段，输出 `docs/TASKS.md`（任务拆分 + 依赖 + 验收标准）。
