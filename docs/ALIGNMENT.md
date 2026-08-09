# 需求对齐文档（ALIGNMENT v2）

> **变更主题**：恢复页面数据源解耦本地 + 剔除版本概念 + 跨环境协作 + OSS DB 单一权威源
> **状态**：Align v2，待舵手确认理解正确后进入 Architect
> **日期**：2026-08-08
> **v2 变更**：纳入舵手补充的 6 条决策（取消版本、OSS DB 单一文件+.bkup、启动先查 OSS、备份拉取-合并-上传、跨环境协作、测试不影响生产）

---

## 1. 背景与问题

### 1.1 当前实现的问题
- 恢复页面查本地运行 DB → 本地 DB 丢失则恢复页面空白
- 存在大量版本概念（全量/增量备份、文件版本、DB 快照 10 版本保留、retention version_keep_count）→ 复杂度高
- OSS DB 快照按时间戳多版本（`meta/db/nas-backup-YYYYMMDD-HHMMSS.db.enc`）→ 跨环境协作时谁是最新的？哪个是我这台机器的？
- reconcile 因本地路径（测试环境 rclone local）报错，阻断恢复页面

### 1.2 舵手的核心诉求
> "恢复肯定是恢复 OSS 里已经有的备份的数据及记录。和本地有啥关系？根本就不应该和本地产生任何联系。"
> "取消一切版本分支的范畴，剔除版本及版本保留相关的代码，降低复杂度。"
> "只需要 master.key + oss 桶地址 + ak/sk 就能获取所有已备份数据。"
> "绿联 NAS 备份一次，Mac 配相同 master.key + oss + ak/sk 就能看到 NAS 备份的文件和历史。"

---

## 2. 架构决策（舵手已明确）

### 决策 1：OSS DB 为唯一权威数据源

- OSS 里只有一个权威 DB：`oss.db.enc` + `oss.db.iv`（路径待 Architect 定，暂定 `meta/db/oss.db.enc`）
- 每次上传新 DB 前，把旧的改名备份：`oss.db.enc` → `oss.db.versionYYYYMMDDHHmm.bkup.enc`
- **不再**保留 10 个时间戳版本（删除 `dbBackupKeepVersions` 机制）
- .bkup 数量：默认保留最近 N 个（N 可配置，建议 3-5），超出删最老的。这不是"版本"，是"DB 容灾快照"

### 决策 2：本地 DB = OSS DB 的工作副本

- **只有一个 DB 概念**：本地 `data/nas-backup.db` 是 OSS `oss.db` 的本地工作副本
- 启动时：从 OSS 拉取 `oss.db` 覆盖本地（保证本地=OSS 最新）
- 运行时：读写本地 DB
- 备份后：把更新后的本地 DB 上传到 OSS 作为新 `oss.db`
- **不再有"本地运行 DB"和"OSS 备份 DB"两套数据** —— 它们是同一份数据的两个位置

### 决策 3：启动流程（先查 OSS）

```
服务启动
  ├─ 加载 master.key + rclone.conf（必须存在，否则报错退出）
  ├─ 检查 OSS meta/db/oss.db.enc 是否存在
  │   ├─ 存在（OSS 有数据）
  │   │   ├─ 下载 + 解密 → 覆盖本地 data/nas-backup.db
  │   │   └─ 恢复页面立即可用，展示 OSS 里已备份的文件清单
  │   └─ 不存在（OSS 空目录，首次部署）
  │       ├─ 初始化空本地 DB（建表）
  │       └─ 等待第一次备份，备份完成后上传 DB 到 OSS
  └─ 启动 HTTP 服务
```

### 决策 4：备份流程（拉取-备份-合并-上传）

```
每次备份
  ├─ 1. 从 OSS 拉取最新 oss.db → 解密 → 作为本次备份的工作 DB
  │      （防止多机器协作时基于过期状态；单机时也是最新，无副作用）
  ├─ 2. 扫描本地待备份目录
  ├─ 3. 去重（基于 hash_index，已存在则跳过上传）
  ├─ 4. 上传新文件到 OSS（data/ 前缀下）
  ├─ 5. 更新工作 DB：files / backup_files / hash_index / backups / logs
  ├─ 6. 校验 DB 完整性（PRAGMA integrity_check）
  ├─ 7. 上传工作 DB 到 OSS：
  │      ├─ 先把当前 oss.db 改名为 oss.db.versionYYYYMMDDHHmm.bkup
  │      └─ 上传新 oss.db
  └─ 8. 清理超出保留数的 .bkup
```

### 决策 5：剔除所有版本概念

| 删除 | 说明 |
|---|---|
| `BackupType`（full/incremental/auto） | 每次备份就是"一次备份"，不分类型 |
| `BaseBackupID` | 不再有"基于哪个全量的增量" |
| `version_keep_count` retention 配置 | 不保留"N 个版本" |
| `dbBackupKeepVersions` | OSS DB 不再保留 10 版本，改为单一 oss.db + .bkup |
| 文件多版本 | 一个文件一个 storage_key（去重），无版本历史 |

**保留**：
- `BackupRecord`（每次备份一条记录，含统计、状态、日志关联）—— 这是"备份历史"，不是"版本"
- `BackupFileRecord`（记录文件在哪次备份中）—— 用于"从哪次备份恢复"
- `hash_index` 的 `ref_count`（去重引用计数，不是版本）
- `logs` 表（每次备份的详细日志）

### 决策 6：跨环境协作

- 多台机器共用一个 OSS 桶时，看到的是**同一份** oss.db
- 机器 A 备份 → 上传新 oss.db → 机器 B 下次启动/备份前拉取 → B 看到 A 的备份记录
- 文件路径在 DB 里是**备份时的原始绝对路径**（如绿联 NAS 上的 `/var/lib/...`）
- 恢复到不同机器时：路径可能不可写 → 走 `FallbackBaseDir` 或用户指定目录
- **并发冲突不特殊处理**：最后写入获胜（用户场景是顺序操作，非并发）

### 决策 7：测试不影响生产

- 代码层面：一套代码同时支持本地测试（rclone local）和真实 OSS
- 配置层面：测试配置（当前 config.yaml 的 local rclone）和生产配置（真实 OSS AK/SK）隔离
- 部署时切换配置即可，不切换代码

---

## 3. 恢复页面改造（基于决策 1-7）

### 3.1 数据源
- 恢复页面文件清单：查本地 `data/nas-backup.db`（它 = OSS oss.db 的副本，启动时已拉取）
- 恢复作业历史（restore_jobs）：仍存本地 DB（本机操作记录，不同步 OSS）

### 3.2 "更新/刷新"按钮
- 改为"从 OSS 重新拉取最新 oss.db"
- 点击 → 下载 oss.db → 解密覆盖本地 → 刷新页面清单

### 3.3 触发恢复
- 文件路径、storage_key、backup_id 等来自本地 DB（= OSS DB 副本）
- 目标路径：支持原始路径 + 自定义目录（选项 C）

### 3.4 reconcile 的归属
- 恢复页面不再调 reconcile
- reconcile 作为独立功能保留（对账 OSS 对象 vs DB 记录），可移到设置/维护页面

---

## 4. 之前歧义点的最终结论

| 歧义 | 结论 |
|---|---|
| 1. OSS DB 数据源 | 读 OSS 单一 `oss.db`（非多版本时间戳） |
| 2. 恢复作业记录 | 写本地 DB（不同步 OSS） |
| 3. 缓存策略 | **不需要单独缓存** —— 本地 DB 本身就是 OSS DB 副本，启动时拉取，手动刷新时重新拉取 |
| 4. 目标路径 | 原始路径 + 自定义目录（选项 C） |
| 5. 更新按钮 | 改为"从 OSS 重新拉取 oss.db" |
| 6. bootstrap | **废弃** —— 启动时自动拉取 oss.db 覆盖本地，不需要单独 bootstrap 操作 |
| 7. 多机器共享 OSS | 支持，同一份 oss.db，顺序协作 |

---

## 5. 影响范围预估

### 5.1 后端
- `db_backup.go`：重写。单一 oss.db + .bkup 机制，删除多版本保留
- `engine.go`：启动流程改造（先查 OSS 拉取 DB）；备份流程改造（拉取-备份-上传）
- `restore.go`：恢复页面数据源已经是本地 DB（=OSS 副本），改动较小
- `reconcile.go`：从恢复页面解耦，保留为独立功能
- `models.go`：删除 `BackupType`/`BaseBackupID`/版本相关字段
- `config.go`：删除 `version_keep_count` 等 retention 版本配置
- DB schema migration：删除版本相关列/约束

### 5.2 前端
- `Restore.tsx`：更新按钮改为"从 OSS 拉取"；目标路径支持自定义目录
- `Dashboard.tsx`/`Content.tsx`：全览页面展示来自 OSS DB 的备份历史（跨环境）
- 移除版本相关 UI（如果有）

### 5.3 配置
- `config.yaml`：retention 段简化（删除版本保留，保留 .bkup 数量）

---

## 6. 验收标准

1. **灾难恢复**：删除本地 `data/nas-backup.db` + 代码，只保留 `master.key` + `rclone.conf`，重新部署 → 启动后恢复页面显示 OSS 里所有已备份文件
2. **跨环境**：机器 A 备份 → 机器 B 配相同 master.key+oss → B 启动后看到 A 的备份历史和文件清单
3. **无版本概念**：代码中不再有 BackupType/BaseBackupID/version_keep_count/dbBackupKeepVersions
4. **OSS DB 单一**：OSS 里只有 `oss.db.enc` + 若干 `.bkup`，无时间戳多版本
5. **备份流程**：每次备份先拉取 OSS DB，备份后上传新 oss.db（旧的重命名为 .bkup）
6. **启动流程**：启动时自动拉取 OSS DB 初始化本地
7. **不破坏**：去重机制、压缩加密、日志记录、恢复功能正常
8. **测试通过**：单测 + E2E（含跨环境模拟）

---

## 7. 待 Architect 确认的技术点（非需求歧义）

这些是实现层面，Architect 阶段定：
1. oss.db 在 OSS 的具体路径（`meta/db/oss.db.enc`？）
2. .bkup 保留数量默认值（3？5？）
3. 启动时拉取 OSS DB 失败的重试策略
4. 备份时"拉取 OSS DB → 合并"的具体合并算法（直接覆盖本地？还是 merge？）
5. DB schema migration 如何处理已有生产 DB 的版本列删除
6. 多机器并发备份的冲突检测（最后写入获胜？加锁？）—— 倾向不处理，文档说明

---

## 8. 请舵手确认

请确认以上 v2 文档正确反映了你的 6 条补充决策。如有偏差请指出。

确认后进入 Architect 阶段，输出 `docs/DESIGN.md`。
