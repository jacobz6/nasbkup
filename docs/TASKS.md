# 任务拆分（TASKS）

> **依据**：[docs/ALIGNMENT.md](file:///Users/jacobzhang/工作区/code/nasbkup_system/docs/ALIGNMENT.md) v2 + [docs/DESIGN.md](file:///Users/jacobzhang/工作区/code/nasbkup_system/docs/DESIGN.md)
> **状态**：Atomize 阶段，待舵手确认后进入执行
> **原则**：小步重构，每步可编译可测试，频繁提交

---

## 任务依赖图

```
T1 (DB schema) ─┬─> T2 (models/config)
                └─> T3 (db_backup.go 重构)
                       │
T4 (engine.go) ────────┤
                       ├─> T6 (API 路由)
T5 (restore/reconcile) ┤      │
                       └──────┴─> T7 (前端)
                                   │
T8 (main.go/restore-cli) ──────────┤
                                   ├─> T9 (测试)
                                   └─> T10 (配置/文档)
```

---

## T1：DB Schema 去版本化

**文件**：
- `nas-backup-backend/internal/db/migrations/001_init.sql`
- `nas-backup-backend/internal/db/migrations/004_add_file_backup_status.sql`

**改动**：
1. `001_init.sql` 的 `backups` 表：删除 `type` 列、`base_backup_id` 列、`skipped_inc` 列、`idx_backups_type` 索引；status CHECK 加入 `completed_with_errors`（原 004 加的）
2. `004_add_file_backup_status.sql`：删除重建 `backups` 表的逻辑（已并入 001），只保留 `files` 表加 `last_backup_*` 列的部分
3. 删除 `data/nas-backup.db`（开发环境重建）

**验收**：
- `go build ./...` 通过
- 启动后端，DB 自动建表成功，`PRAGMA table_info(backups)` 不含 type/base_backup_id/skipped_inc
- 现有 DB 相关单测通过（可能需要适配 schema 变化）

**风险**：高（改 schema 影响面广，触发后续任务）

**模块卡点**：完成后触发硬闸门确认

---

## T2：models/config 删除版本类型

**依赖**：T1

**文件**：
- `nas-backup-backend/internal/models/models.go`
- `nas-backup-backend/internal/models/models_test.go`
- `nas-backup-backend/internal/config/config.go`
- `nas-backup-backend/internal/config/config_test.go`
- `nas-backup-backend/config.yaml.example`
- `nas-backup-backend/config.yaml`（测试配置）

**改动**：
1. `models.go`：删除 `BackupType` 类型及常量（`BackupTypeFull/Incremental/Auto`）；`BackupRecord` 删除 `Type`/`BaseBackupID`/`SkippedByInc` 字段
2. `config.go`：`RetentionConfig` 删除 `VersionKeepCount`/`FullResetInterval`，新增 `DBBkupKeepCount`（默认 5）
3. 配置文件示例同步更新

**验收**：
- `go build ./...` 通过
- `go test ./internal/models/ ./internal/config/` 通过
- 代码中无 `BackupType`/`VersionKeepCount`/`FullResetInterval` 残留引用

**风险**：中（触发 engine.go 等大量编译错误，需 T4 修复）

---

## T3：db_backup.go 重构（OSS DB 单一权威源）

**依赖**：T1, T2

**文件**：
- `nas-backup-backend/internal/backup/db_backup.go`
- `nas-backup-backend/internal/backup/db_backup_test.go`

**改动**：
1. 删除：`dbBackupKeepVersions`、`BackupDatabase`（多版本命名）、`pruneOldVersions`、`ListVersions`、`Bootstrap`、`ensureRestored`（bootstrap 专用解冻）
2. 新增常量：`ossDBPrefix="meta/db"`、`ossDBEncKey="meta/db/oss.db.enc"`、`ossDBIVKey="meta/db/oss.db.iv"`
3. 新增方法：
   - `OSSDBExists(ctx) (bool, error)`：检查 OSS 是否有 oss.db.enc
   - `PullOSSDB(ctx) (exists bool, err error)`：下载 oss.db → 解密覆盖本地 DB；覆盖前导出本地 restore_jobs 数据，覆盖后导回；若 OSS 无 oss.db 返回 (false, nil)
   - `PushOSSDB(ctx) error`：WAL checkpoint → integrity_check → 加密 → 旧 oss.db 改名 .bkup → 上传新 oss.db → pruneBkups
   - `pruneBkups(ctx) error`：列 meta/db/*.bkup.enc，按时间戳降序，删第 6+ 个
4. 复用现有 `copyFile`、`encryptor.EncryptFile/DecryptFile`、`storage.Upload/Download/Delete`

**验收**：
- `go build ./internal/backup/` 通过
- `go test ./internal/backup/ -run TestDBBackup` 通过（新增单测覆盖 Pull/Push/pruneBkups）
- 本地 E2E 环境：手动调用 PushOSSDB → OSS 出现 oss.db.enc；PullOSSDB → 本地 DB 被覆盖

**风险**：高（核心数据链路）

**模块卡点**：完成后触发硬闸门确认

---

## T4：engine.go 改造（启动流程 + 备份流程去版本）

**依赖**：T1, T2, T3

**文件**：
- `nas-backup-backend/internal/backup/engine.go`
- `nas-backup-backend/internal/backup/engine_test.go`

**改动**：
1. 删除：`RunFullBackup`/`RunIncrementalBackup`/`determineBackupType`/`checkBootstrapRequired`/`IsBootstrapRequired`
2. `StartBackup`：删除 `backupType` 参数（或保留忽略），合并为单一 `RunBackup`
3. `executeBackup`：开头加 `PullOSSDB`（拉取最新），结尾加 `PushOSSDB`（上传新 oss.db）
4. 新增 `InitFromOSS(ctx) error`：
   - 探测 OSS 可达（rclone lsf oss-crypt:），10 分钟内退避重试（1s,2s,4s...上限 60s），超时 `os.Exit(1)`
   - OSSDBExists? yes: PullOSSDB + runMigrations; no: 初始化空 DB + runMigrations
5. `generateStorageKey`：删除 `backupType` 参数，路径改为 `data/<hash前2位>/<hash>.enc`
6. 删除 `executeBackup` 内 `backupType == BackupTypeIncremental` 分支逻辑

**验收**：
- `go build ./internal/backup/` 通过
- `go test ./internal/backup/ -run TestEngine` 通过（适配新流程）
- 手动触发备份：先 PullOSSDB → 扫描 → 上传 → 更新 DB → PushOSSDB，OSS 出现新 oss.db + 旧 .bkup

**风险**：高（备份主流程）

**模块卡点**：完成后触发硬闸门确认

---

## T5：restore/reconcile 解耦

**依赖**：T2（不依赖 T3/T4，可并行）

**文件**：
- `nas-backup-backend/internal/backup/restore.go`（改动小）
- `nas-backup-backend/internal/backup/reconcile.go`（改动小，已修 List 容错）

**改动**：
1. `restore.go`：`RestoreWithOptions` 的归一化逻辑保留；移除任何"必须 restore_to_original"的隐含约束（前端 T7 会恢复自定义目录选择）
2. `reconcile.go`：保持独立功能，不从恢复页面调用（前端 T7 改）

**验收**：
- `go build ./internal/backup/` 通过
- `go test ./internal/backup/ -run TestRestore` 通过

**风险**：低

---

## T6：API 路由新增 refresh-oss-db

**依赖**：T3, T4

**文件**：
- `nas-backup-backend/internal/api/router.go`
- `nas-backup-backend/internal/api/restore_handler.go`

**改动**：
1. 新增路由 `POST /api/restore/refresh-oss-db`：调用 `engine.dbBackupSvc.PullOSSDB`，返回成功/失败
2. `POST /api/backup/trigger`：request body 的 `type` 字段忽略（兼容旧前端，或删除）
3. `POST /api/reconcile`：保留，前端不挂恢复页面

**验收**：
- `go build ./...` 通过
- `curl -X POST http://127.0.0.1:8080/api/restore/refresh-oss-db` 返回 success
- `go test ./internal/api/` 通过

**风险**：低

---

## T7：前端改造

**依赖**：T6

**文件**：
- `nas-backup-frontend/src/pages/Restore.tsx`
- `nas-backup-frontend/src/utils/api.ts`（可能新增 refreshOssDb API）

**改动**：
1. "更新"按钮：调用 `POST /api/restore/refresh-oss-db`（替代 `reconcileApi.run`）
2. 恢复表单：恢复支持选择自定义目标目录（恢复"恢复到"路径选择器），默认原始路径；移除"不可自定义目标目录"提示
3. 移除任何 bootstrap 相关 UI（如果有）

**验收**：
- `./node_modules/.bin/tsc -b --noEmit` 通过
- `./node_modules/.bin/tsc -b && ./node_modules/.bin/vite build` 通过
- 手动验证：点"更新"调新 API；选择自定义目录恢复成功

**风险**：中（UI 交互）

---

## T8：main.go / restore-cli 改造

**依赖**：T4

**文件**：
- `nas-backup-backend/cmd/nas-backup/main.go`
- `nas-backup-backend/cmd/restore-cli/main.go`

**改动**：
1. `main.go`：在 `engine := backup.NewEngine(...)` 后、`server.ListenAndServe()` 前调用 `engine.InitFromOSS(ctx)`；失败则进程退出
2. `restore-cli/main.go`：删除 `bootstrap` 子命令；保留单文件恢复调试功能

**验收**：
- `go build ./cmd/nas-backup/ ./cmd/restore-cli/` 通过
- 启动后端：自动从 OSS 拉取 DB（或初始化空 DB）
- OSS 不可达：10 分钟重试后进程退出，日志明确

**风险**：中（启动流程）

---

## T9：测试适配与新增

**依赖**：T1-T8 全部完成

**文件**：
- `nas-backup-backend/internal/backup/db_backup_test.go`（重写）
- `nas-backup-backend/internal/backup/engine_test.go`（适配）
- `nas-backup-backend/internal/api/integration_test.go`（适配）
- `scripts/verify_e2e.py`（适配新流程）
- `scripts/verify-e2e.sh`（适配）

**改动**：
1. `db_backup_test.go`：覆盖 PullOSSDB/PushOSSDB/OSSDBExists/pruneBkups，含 .bkup 保留数验证
2. `engine_test.go`：删除全量/增量测试，新增 InitFromOSS 测试（OSS 空 + OSS 有数据两种）
3. `integration_test.go`：适配 schema 变化，新增 `/api/restore/refresh-oss-db` 测试
4. E2E：新增灾难恢复场景（删本地 DB → 启动 → 恢复页面可用）+ 跨环境模拟（两 test-env 共用 OSS）

**验收**：
- `go test -count=1 -short -timeout 300s ./...` 全通过
- `bash scripts/verify-e2e.sh --skip-build` 通过，含灾难恢复 + 跨环境场景

**风险**：中

---

## T10：配置与文档更新

**依赖**：T1-T9

**文件**：
- `nas-backup-backend/config.yaml.example`
- `nas-backup-backend/config.yaml`（测试配置）
- `AGENTS.md`
- `docs/WIKI.md`（如需要）
- `docs/RESTORE_GUIDE.md`（如需要）

**改动**：
1. config 示例：retention 段删除 `version_keep_count`/`full_reset_interval`，加 `db_bkup_keep_count: 5`
2. AGENTS.md：更新启动流程描述（InitFromOSS）、删除 bootstrap 章节、更新备份流程（拉取-备份-上传）
3. 文档同步新架构

**验收**：
- 文档与代码一致
- config 示例可被代码正确解析

**风险**：低

---

## 执行顺序

```
阶段1：T1 → T2（DB 层 + models/config，打基础）
阶段2：T3 + T5（并行，db_backup 重构 + restore/reconcile 解耦）
阶段3：T4（engine 改造，依赖 T1/T2/T3）
阶段4：T6 + T8（并行，API 路由 + main.go）
阶段5：T7（前端，依赖 T6）
阶段6：T9（测试，依赖全部）
阶段7：T10（文档）
```

**模块卡点**：T1、T3、T4 完成后各触发一次硬闸门确认。

---

## 请舵手确认

确认任务拆分后，我进入执行态（核动力驴模式），按顺序推进，仅在硬闸门（T1/T3/T4）或真正阻塞时暂停。

请回复"全线放行"或指出调整。
