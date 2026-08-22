# NAS Backup System — 核心流程全业务测试用例文档

> **定位**：本文件是测试**用例设计文档**（不含测试代码），覆盖核心业务全流程与各类边界条件。
> **依据**：[WIKI.md](WIKI.md)、[AGENTS.md](../AGENTS.md)、[TROUBLESHOOTING.md](TROUBLESHOOTING.md)、[RESTORE_GUIDE.md](RESTORE_GUIDE.md)、[DEPLOYMENT.md](DEPLOYMENT.md)、[SCRIPTS.md](SCRIPTS.md)、真实代码核对（2026-08-22）。
> **代码为准原则**：若本文档与真实代码不一致，以真实代码为准，并回改本文档。

---

## 目录

1. [文档约定](#1-文档约定)
2. [测试环境与前置准备](#2-测试环境与前置准备)
3. [M01 启动与初始化（TC-INIT）](#m01-启动与初始化tc-init)
4. [M02 备份核心流程（TC-BKP）](#m02-备份核心流程tc-bkp)
5. [M03 文件扫描与变更检测（TC-SCAN）](#m03-文件扫描与变更检测tc-scan)
6. [M04 内容寻址去重（TC-DEDUP）](#m04-内容寻址去重tc-dedup)
7. [M05 压缩（TC-CMPR）](#m05-压缩tc-cmpr)
8. [M06 加密（TC-CRPT）](#m06-加密tc-crpt)
9. [M07 存储上传/下载（TC-UPLD）](#m07-存储上传下载tc-upld)
10. [M08 定时调度（TC-SCHD）](#m08-定时调度tc-schd)
11. [M09 恢复流程（TC-RSTR）](#m09-恢复流程tc-rstr)
12. [M10 归档解冻（TC-THAW）](#m10-归档解冻tc-thaw)
13. [M11 OSS DB 同步与灾难恢复（TC-DBSYNC）](#m11-oss-db-同步与灾难恢复tc-dbsync)
14. [M12 垃圾回收（TC-GC）](#m12-垃圾回收tc-gc)
15. [M13 数据一致性对账（TC-RCNL）](#m13-数据一致性对账tc-rcnl)
16. [M14 SSE 实时进度（TC-SSE）](#m14-sse-实时进度tc-sse)
17. [M15 内容选择管理（TC-CONT）](#m15-内容选择管理tc-cont)
18. [M16 策略配置（TC-STRAT）](#m16-策略配置tc-strat)
19. [M17 仪表板统计（TC-DASH）](#m17-仪表板统计tc-dash)
20. [M18 日志（TC-LOG）](#m18-日志tc-log)
21. [M19 API 通用行为与安全（TC-API）](#m19-api-通用行为与安全tc-api)
22. [M20 互斥与并发（TC-MUTX）](#m20-互斥与并发tc-mutx)
23. [M21 崩溃恢复（TC-CRSH）](#m21-崩溃恢复tc-crsh)
24. [M22 restore-cli（TC-CLI）](#m22-restore-clitc-cli)
25. [M23 部署与运维脚本（TC-DEP）](#m23-部署与运维脚本tc-dep)
26. [M24 前端 UI（TC-FE）](#m24-前端-uitc-fe)
27. [E2E 串联场景（TCE）](#e2e-串联场景tce)
28. [附录 A：现有测试覆盖映射](#附录-a现有测试覆盖映射)
29. [附录 B：已知陈旧断言与测试债清单](#附录-b已知陈旧断言与测试债清单)

---

## 1. 文档约定

### 1.1 用例编号规则

```
TC-<模块>-<序号>   例：TC-BKP-003
TCE-<序号>         端到端串联场景（跨模块）
```

### 1.2 优先级定义

| 级别 | 定义 | 判定标准 |
|------|------|----------|
| **P0** | 核心里径 | 数据安全/数据丢失/主流程闭环；失败即阻塞发布 |
| **P1** | 重要路径 | 常用功能、常见边界；失败需当版本修复 |
| **P2** | 次要边界 | 罕见边界、体验优化；可跟踪修复 |

### 1.3 测试层级定义

| 标记 | 层级 | 运行方式 |
|------|------|----------|
| UT | Go 单元测试 | `go test -short ./internal/...` |
| IT | Go 集成测试 | `go test`（非 short 模式） |
| API | HTTP 接口测试 | curl / Python（后端运行中） |
| E2E | 端到端 | `bash scripts/verify-e2e.sh` |
| CLI | 命令行 | restore-cli / rclone / sqlite3 |
| MAN | 人工操作 | 浏览器 UI / 真实 OSS 桶 / 断网等 |

### 1.4 现有覆盖标记

- ✅ 已有自动化覆盖（单测/E2E 中已存在对应断言）
- ⚠️ 部分覆盖（存在但断言不完整或场景不全）
- ❌ 缺失（本文档新增设计，待实现）

### 1.5 关键口径（测试断言必须遵守）

> 以下口径来自代码核对与 [WIKI 附录 C](WIKI.md#附录-c2026-08-16-oss-直连改造--两个非阻塞问题修复记录)，断言写错会误判：

1. **去版本化**：`Engine.StartBackup()` 无参数，每次备份是**独立会话**；请求体中 `type` 字段仅为向后兼容被接受，**不产生 full/incremental 语义**。
2. **存储键格式**：`data/<hash前2位>/<hash>.enc`（内容寻址、无日期/类型段）。
3. **ref_count 不变量**：`hash_index.ref_count == COUNT(backup_files.storage_key)`（**不是**活跃文件数）。
4. **解冻等待**：`maxThawWait = 6h`，`thawPollInterval = 30s`；恢复 job 总超时 8h；HTTP 请求级超时 4h 说法已被 job 级 8h 取代（以 `restore_job.go:175` 为准）。
5. **恢复目标归一化**：`restore_to_original=true` 或 `output_dir=""` ⇒ job 记录中 `output_dir='__original__'`，恢复目标 = `files.path`。
6. **备份终态集合**：`pending / running / completed / completed_with_errors / failed / cancelled`（注意 `completed_with_errors` 是合法终态）。
7. **x-oss-restore header**：`ongoing-request="true"`（解冻中）/ `ongoing-request="false", expiry-date="..."`（已解冻），**值带引号**。
8. **API 响应包装**：部分端点返回 `{"success":true,"data":{...}}`，部分直接返回对象；测试脚本需兼容两种（参考 verify_e2e.py 的处理）。

---

## 2. 测试环境与前置准备

### 2.1 环境矩阵

| 环境 | 用途 | 说明 |
|------|------|------|
| **本地模拟**（macOS） | UT / IT / 大部分 E2E | rclone remote 指向本地目录（`--local` 部署），不产生 OSS 费用 |
| **真实 OSS**（Archive 桶） | 解冻/闭环验收 | `verify_cloud_archive.py`；Archive 解冻约 60s，非幂等 |
| **真实 OSS**（Standard 桶或已解冻对象） | E2E 全绿基线 | 需**清空桶 + 全新备份**（避免旧主密钥密文触发 nonce mismatch） |

### 2.2 通用前置（每个测试轮次开始时）

```bash
export PATH="$HOME/go-sdk/go/bin:$PATH"
# 清理旧环境（防止旧 DB/旧对象污染）
rm -f nas-backup-backend/data/nas-backup.db*
# 部署并启动（本地模拟模式）
./scripts/deploy.sh --local
./scripts/start.sh start
# 健康探活（注意：没有 /api/health 端点！）
curl -s http://127.0.0.1:8080/api/dashboard/stats
```

### 2.3 测试数据集（`scripts/nas_file_generator.py` 或手工）

| 类别 | 示例 | 目的 |
|------|------|------|
| 空文件 | `empty.txt`（0 字节） | 空文件边界 |
| 小文本 | <1KB | 基线路径 |
| 可压缩文件 | 重复行文本 500+ 行 | zstd 生效验证 |
| 不可压缩文件 | `os.urandom` 二进制 50KB/100KB | 跳过压缩收益验证 |
| 跨块文件 | ≥ 512KB（>2 个 256KB 加密分块） | 流式分块加密边界 |
| 大文件 | ≥ 10MB | 上传/下载/内存边界 |
| 重复内容 | A 与 B 内容完全相同 | 同批次去重 |
| 跨批次重复 | 备份后新增已知内容副本 | 跨批次去重 |
| 深层嵌套 | `nested/deep/path/f.txt` | 路径处理 |
| 中文路径 | `文档/报告_2026.pdf` | 编码边界（macOS/Linux 差异） |
| 特殊字符名 | 含空格、`#`、`&`、emoji 的文件名 | rclone 转义/引号 |
| 符号链接 | 链接→文件、链接→目录、循环链接 | 扫描器行为 |
| 权限受限 | 0000 权限文件 | 扫描错误处理 |
| 排除类文件 | `.log`/`.tmp`/`node_modules`/`.git`/`.DS_Store` | 默认排除规则 |
| 已压缩类型 | `.zip`/`.mp4`/`.jpg` | skipTypes 跳过压缩 |

---

## M01 启动与初始化（TC-INIT）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-INIT-001 | 正常启动全流程 | API | P0 | 合法 config.yaml 启动二进制 | 日志按序输出：配置加载→日志初始化→DB 打开/迁移→CleanupStaleRunning→组件初始化→InitFromOSS→HTTP 监听；`/api/dashboard/stats` 200 | ✅（部分，integration） |
| TC-INIT-002 | 首次启动自动建库 | CLI | P0 | 删除 data/nas-backup.db 后启动 | 自动创建 DB 并执行迁移 001→002→003；`schema_migrations` 含 3 条记录 | ⚠️ |
| TC-INIT-003 | config.yaml 不存在 | CLI | P1 | `-config /nonexistent.yaml` 启动 | 返回默认配置继续启动或明确报错退出（二选一，与 `config.Load` 实际行为一致），不得 panic | ⚠️（TestLoadNonexistentFile 有 UT） |
| TC-INIT-004 | config.yaml 语法错误 | CLI | P1 | 写入非法 YAML（如缺冒号）启动 | 启动失败并输出可定位的错误（含文件路径），不 panic | ❌（启动级无，仅 UT TestLoadInvalidYAML） |
| TC-INIT-005 | 端口占用 | CLI | P1 | 先占用 8080 再启动 | 报 `address already in use` 退出，错误信息明确 | ❌ |
| TC-INIT-006 | data/ 目录不存在/无写权限 | CLI | P1 | data 目录只读启动 | EnsureDataDirs 报错，错误信息指明路径与权限 | ❌ |
| TC-INIT-007 | master.key 不存在时自动生成 | UT | P0 | 指向不存在密钥路径创建 Encryptor | 自动生成 32 字节密钥，文件权限 0600 | ✅（TestNewEncryptorCreatesKey） |
| TC-INIT-008 | master.key 尺寸非法 | UT | P0 | 密钥文件写入 16 字节 | 报错拒绝，不得静默截断/补零 | ✅（TestLoadMasterKeyWrongSize） |
| TC-INIT-009 | rclone.conf 不存在自动生成 | CLI | P0 | 删除 data/rclone.conf 后启动 | `EnsureRcloneConfig` 按 config.yaml 的 oss 段生成 `[oss] type=s3` 配置；`cat data/rclone.conf` 验证 type=s3 且四要素齐全 | ⚠️ |
| TC-INIT-010 | rclone.conf 残留 `type=local`（关键坑） | CLI | **P0** | 手工将 rclone.conf 改为 `type=local` 后重启 | 记录实际行为：当前版本仅 repair 不转 s3 ⇒ 用例断言**启动日志应告警**；若行为为静默残留，此用例标记为**已知缺陷回归探针**（备份将写本地只读目录失败） | ❌（AGENTS.md 2026-08-17 记录的坑，无自动防护测试） |
| TC-INIT-011 | 启动时 InitFromOSS 拉取权威 DB | IT | P0 | 本地 DB 删除，OSS 存在 `oss.db.enc`，启动 | 启动日志含 InitFromOSS 拉取成功；本地 DB 恢复且 `backups` 表与 OSS 版本一致 | ⚠️（db_backup UT 有，启动链路无） |
| TC-INIT-012 | 启动时 OSS 不可达降级 | IT | P1 | 断网/错误 AK 启动 | waitForOSS 有限重试后降级使用本地 DB 启动（不阻塞 HTTP 服务）；日志记录降级原因 | ❌ |
| TC-INIT-013 | 残留 running/pending 备份清理 | UT | P0 | 预置 DB 含 running、pending 记录后重启 | 两条记录均被标记 failed，error_message 注明崩溃清理；`IsRunning()` 返回 false | ✅（TestBackupRepository_CRUD / CleanupStaleRunning 相关） |
| TC-INIT-014 | 残留 running restore_jobs 清理 | UT | P1 | 预置 running 状态恢复任务后重启 | 被标记 failed/cancelled；`RestoreJobRepo.GetRunning()` 为空 | ✅（TestRestoreJobRepository_CleanupStaleRunning） |
| TC-INIT-015 | SIGINT/SIGTERM 优雅关闭 | MAN | P1 | 备份运行中发送 SIGTERM | 30s 内完成关闭：HTTP 停止接受新请求、进行中备份进入可恢复状态、DB 连接关闭 | ❌ |
| TC-INIT-016 | 启动并发初始化幂等 | IT | P2 | 连续快速 restart 两次 | 第二次启动正常清理第一次残留；无重复调度任务注册 | ❌ |

---

## M02 备份核心流程（TC-BKP）

> 去版本化后：**每次备份为独立会话**，扫描所有启用目录，Unchanged 文件跳过上传（skipped_inc 语义已并入统计），不区分全量/增量。

### 关键用例详述

**TC-BKP-001 首次备份主链路（P0，E2E）**

- 前置：全新 DB（无任何记录）、本地模拟云存储、2.3 数据集就绪、备份目录已配置。
- 步骤：
  1. `POST /api/backup/trigger`（body `{}`，验证 202 + `backup_id`）。
  2. 轮询 `GET /api/dashboard/history` 至终态。
  3. 校验统计字段。
  4. `sqlite3` 查 `files` / `backup_files` / `hash_index` 三表。
- 预期：
  - status ∈ {completed, completed_with_errors}（无失败文件时应为 completed）。
  - `total_files` ≥ 测试文件数；`uploaded_size` ≤ `total_size`。
  - 每个测试文件在 `files` 表有一条 active 记录且 hash 非空。
  - 每个上传文件在 `backup_files` 有记录，`storage_key` 符合 `data/<hash前2位>/<hash>.enc`。
  - `hash_index.ref_count` = 该 storage_key 在 backup_files 中的行数。
  - 云端（rclone lsl）可列出对应 `.enc` 对象。
- 覆盖：⚠️（verify_e2e Phase 3 有，但未校验三表不变量）

### 用例表

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-BKP-001 | 首次备份主链路 | E2E | P0 | 见上文详述 | 见上文详述 | ⚠️ |
| TC-BKP-002 | 二次备份无变更 | E2E | P0 | 不改动任何文件再次触发 | 新 backup 记录创建；uploaded_size≈0；无新 OSS 对象；原对象未删除 | ❌ |
| TC-BKP-003 | 新增文件备份 | E2E | P0 | 新增 N 个文件后触发 | 仅新文件被上传；`files` 表新增 active 记录；旧文件 backup_files 关联保留 | ⚠️（E2E Phase 4 有，未查表） |
| TC-BKP-004 | 修改文件备份 | E2E | P0 | 修改文件内容后触发 | 新哈希新 storage_key 上传；`files` 表该文件 hash 更新；旧对象 ref_count 递减（若旧内容无其他引用→降为 0 并置 orphaned_at） | ⚠️ |
| TC-BKP-005 | 删除文件备份 | E2E | P0 | 删除源文件后触发 | `files.status='deleted'`；对应 hash_index.ref_count 递减；孤儿对象**不立即从 OSS 删除**（等 GC 宽限期） | ❌ |
| TC-BKP-006 | mtime-only 变更（内容未变） | E2E | P1 | `touch` 文件（改 mtime 不改内容）后触发 | 哈希计算后降级 Unchanged，不重复上传；日志有降级说明 | ❌ |
| TC-BKP-007 | 空文件备份 | E2E | P1 | 0 字节文件纳入备份 | 成功上传与恢复；OSS 对象仅含 salt+nonce 等开销字节；统计不报错 | ✅（E2E 有 empty_file） |
| TC-BKP-008 | 大文件备份（>10MB） | E2E | P1 | 大文件触发备份 | 上传成功；恢复后 SHA-256 一致；无内存异常 | ⚠️ |
| TC-BKP-009 | 备份目录为空目录 | E2E | P1 | 备份目录配置为空目录触发 | backup completed，total_files=0，不报错 | ❌ |
| TC-BKP-010 | 备份目录不存在 | API | P1 | 配置指向不存在路径触发 | 扫描错误记录在 ScanResult.Errors；备份状态与 error_message 体现；不 panic | ✅（TestScanNonExistentDirectory UT 层有） |
| TC-BKP-011 | 备份目录被禁用 | API | P1 | enabled=false 后触发 | 该目录文件全部不处理；日志说明跳过 | ❌ |
| TC-BKP-012 | 备份中单文件上传失败 | IT | P1 | 注入一个上传必失败文件（如权限/超大） | 该文件计入 failed_files；会话终态 completed_with_errors；其余文件正常 | ✅（TestFileRepository_MarkBackup* UT 层） |
| TC-BKP-013 | 备份完成触发 PushOSSDB | IT | P0 | 完成一次备份后检查 OSS | OSS 出现新 `oss.db.enc`（或 meta/db 下新版本）；旧版本重命名为 `.bkup`，保留 N 份 | ⚠️（TestPruneBkupsKeys/TestBkupKeepCount UT 有；链路级无） |
| TC-BKP-014 | 备份完成异步触发 ReconcileIfNeeded | IT | P1 | 完成一次健康备份 | 后台对账执行但零修复（AppliedFixes=0，不回写 OSS）；不阻塞下一次备份 | ❌ |
| TC-BKP-015 | 备份进度百分比单调性 | E2E | P2 | 订阅 SSE 记录全程 percent 序列 | 序列单调不减、0→100；阶段顺序 scanning→hashing→deduplicating→uploading→finalizing→终态 | ❌ |
| TC-BKP-016 | trigger 请求兼容旧 type 字段 | API | P2 | body 带 `{"type":"full"}` / `"incremental"` / `"auto"` / 非法值 | 全部被接受（向后兼容）或非法值返回 400（以实现为准）；不因未知 type 500 | ❌ |
| TC-BKP-017 | 备份取消后再次触发 | API | P0 | 触发→立即 cancel→再触发 | 第二次可正常启动（running 状态被正确清理，无死锁） | ❌ |
| TC-BKP-018 | 上传后存在性校验 | UT | P1 | 上传成功路径注入 | 每个上传对象执行 Exists 校验并有日志；校验失败计入 failed | ❌ |
| TC-BKP-019 | 备份统计字段完整性 | API | P1 | 完成备份后查 history | total_files/total_size/uploaded_size/skipped_dedup/failed_files/compress_saved 字段齐全且数值自洽（uploaded ≤ total） | ⚠️ |
| TC-BKP-020 | 中文路径文件备份 | E2E | P1 | 中文目录/文件名触发备份+恢复 | 上传/恢复无损；rclone 转义正确；统计正确 | ❌（TROUBLESHOOTING 记录过中文路径坑，E2E 未覆盖） |
| TC-BKP-021 | 特殊字符文件名备份 | E2E | P2 | 文件名含空格/`#`/`&`/emoji | 全链路无损 | ❌ |
| TC-BKP-022 | 权限不可读文件 | E2E | P2 | chmod 000 的文件在备份目录 | 记录扫描错误跳过；会话 completed_with_errors；不中断其他文件 | ✅（TestScanErrorHandling UT 层） |

---

## M03 文件扫描与变更检测（TC-SCAN）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-SCAN-001 | 新增文件识别 | UT | P0 | 预置 DB 后新增文件扫描 | ChangeType=Added | ✅ |
| TC-SCAN-002 | 修改文件识别 | UT | P0 | 内容变化 | ChangeType=Modified，OldHash/NewHash 正确 | ✅ |
| TC-SCAN-003 | 删除文件识别 | UT | P0 | DB 有记录但磁盘删除 | ChangeType=Deleted | ✅ |
| TC-SCAN-004 | 未变更识别 | UT | P0 | size+mtime 均未变 | ChangeType=Unchanged | ✅ |
| TC-SCAN-005 | 符号链接→文件 | UT | P0 | 备份目录含指向文件的 symlink | symlink 本身被跳过（不跟随） | ✅（TestScanSymlink） |
| TC-SCAN-006 | 符号链接→目录 | UT | P1 | 指向目录的 symlink | 跟随进入目录扫描内容 | ✅ |
| TC-SCAN-007 | 循环符号链接 | UT | **P0** | a→b→a 循环 | dev+inode 循环检测生效，不无限递归、不重复计数 | ✅（TestScanCircularSymlinks） |
| TC-SCAN-008 | extension 排除规则 | UT | P0 | `*.log` 规则 + .log/.LOG 文件 | 两种大小写均被排除 | ✅（TestScanWithExcludedExtensions） |
| TC-SCAN-009 | directory 排除规则 | UT | P0 | `node_modules` 规则 | 整个目录树（含子目录）跳过 | ✅（TestScanWithExcludedDirs） |
| TC-SCAN-010 | pattern 排除规则 | UT | P1 | `.DS_Store` 模式 | 文件名 glob 命中即排除 | ✅（TestScanWithExcludedPatterns） |
| TC-SCAN-011 | size_exceed 上限 | UT | P1 | MaxFileSize 上限 + 超限文件 | 超限文件跳过，不报错 | ✅（TestScanWithSizeFilters） |
| TC-SCAN-012 | size 下限 | UT | P2 | MinFileSize + 小于下限文件 | 跳过 | ✅ |
| TC-SCAN-013 | 规则禁用后生效 | UT | P1 | enabled=false 的排除规则 | 规则不生效，文件被扫描 | ❌（扫描层） |
| TC-SCAN-014 | 默认排除规则集 | UT | P1 | 全新配置首次扫描 | 默认 7 条规则生效（tmp/log/node_modules/.git/__pycache__/.DS_Store/Thumbs.db） | ✅（TestExclusionDefaults） |
| TC-SCAN-015 | 空目录扫描 | UT | P1 | 只有空目录 | 无文件变更、无错误 | ✅（TestScanEmptyDirectory） |
| TC-SCAN-016 | 深层嵌套 | UT | P1 | 深层路径 | 正确入库，路径无损 | ⚠️（E2E 有 deep path，UT 无） |
| TC-SCAN-017 | 扫描错误收集 | UT | P1 | 混入不可读目录 | Errors 列表包含路径与原因；不影响其余扫描 | ✅（TestScanErrorHandling） |
| TC-SCAN-018 | 多备份目录去重边界 | UT | P2 | 两个启用的备份目录有包含关系（/a 与 /a/sub） | 明确行为（以实现为准：重复处理或子目录去重），不重复计数 ref_count | ❌ |
| TC-SCAN-019 | inode 复用场景 | UT | P2 | 删除后新建同 inode 文件 | 变更识别正确（不因 inode 相同误判 Unchanged） | ❌ |

---

## M04 内容寻址去重（TC-DEDUP）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-DEDUP-001 | 同批次重复文件 | E2E | **P0** | 同一次备份含两份相同内容文件 | 第一份上传（pendingByHash 缓存元数据）；第二份跳过上传；skipped_by_dedup ≥ 1；ref_count=2 | ⚠️（TROUBLESHOOTING 记录同批次曾有小概率 nonce mismatch 边界） |
| TC-DEDUP-002 | 跨批次重复文件 | E2E | **P0** | 备份后新增相同内容副本，再触发备份 | 跳过上传（查 hash_index）；ref_count 递增；OSS 对象数不变 | ⚠️（E2E 6b 有，断言宽松） |
| TC-DEDUP-003 | 去重节省统计 | API | P1 | 构造重复后查 dashboard | 去重节省 = Σ file_size×(ref_count−1)；unique_hash_count < total_files | ⚠️ |
| TC-DEDUP-004 | 空哈希文件兜底 | UT | P1 | 哈希为空的变更进入去重 | 直接加入上传列表（不查库不跳过） | ✅（TestDeduplicateEmptyHash） |
| TC-DEDUP-005 | 非 Added/Modified 不参与 | UT | P1 | Deleted/Unchanged 混入 | 去重模块忽略 | ✅（TestDeduplicateSkipsNonAddedModified） |
| TC-DEDUP-006 | 去重后引用计数正确性 | E2E | **P0** | 同一内容 3 路径 + 删除其中 1 个 | ref_count 3→2；再备份后对象仍保留（ref>0 不进 GC） | ❌ |
| TC-DEDUP-007 | 同批次去重元数据复用 | UT | P1 | 同批次第二份重复文件 | 从 pendingByHash 复用 encrypted_iv 等元数据；恢复两份均成功解密 | ❌（历史 bug 区，建议补防护测试） |
| TC-DEDUP-008 | OSS 存在性校验防幽灵引用 | IT | P1 | hash_index 有记录但 OSS 对象被外删 | 上传前存在性校验发现缺失→重新上传（防崩溃后引用丢失对象） | ❌ |
| TC-DEDUP-009 | 全部已存在 | UT | P2 | 所有变更哈希均已存在 | ToUpload 空，Skipped 全量，TotalSaved 正确 | ✅（TestDeduplicateAllExisting） |
| TC-DEDUP-010 | ref_count 不变量回归 | UT | **P0** | 任意操作序列后断言 | `hash_index.ref_count == COUNT(backup_files.storage_key)` 恒成立（防 C.3 口径回退） | ✅（TestHashRepository_HasRefCountMismatches_CountsBackupFiles） |

---

## M05 压缩（TC-CMPR）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-CMPR-001 | 可压缩文件压缩/解压往返 | UT | P0 | 文本文件 Compress→Decompress | 内容 SHA-256 与原文件一致 | ✅（TestCompressAndDecompress） |
| TC-CMPR-002 | 空文件压缩 | UT | P1 | 0 字节文件 | 成功；解压还原 0 字节 | ✅（TestCompressEmptyFile） |
| TC-CMPR-003 | 随机二进制压缩 | UT | P1 | os.urandom 数据 | 压缩成功但压缩率≈0（或负开销）；压缩开关判断不依赖内容 | ✅（TestCompressBinaryData） |
| TC-CMPR-004 | skipTypes 生效 | UT | P0 | .mp4/.zip 等默认跳过类型 | ShouldCompress=false，不压缩 | ✅（TestShouldCompress / TestCompressSkippedType） |
| TC-CMPR-005 | 压缩禁用开关 | UT | P1 | enabled=false | 全部文件不压缩（compress_type='none'） | ✅（TestShouldCompressDisabled） |
| TC-CMPR-006 | 大小写扩展名 | UT | P2 | `.MP4`/`.Mp4` | 大小写不敏感跳过 | ✅ |
| TC-CMPR-007 | 多压缩级别 | UT | P2 | level 1/9/22 | 全部成功；输出可解压还原 | ✅（TestCompressMultipleLevels） |
| TC-CMPR-008 | zstd 二进制缺失 | UT | P1 | 环境无 zstd 时 | FindZcloneBinary 找不到→压缩步骤报错可定位；备份降级或报错不 panic | ⚠️（TestCompressNotFound 部分覆盖） |
| TC-CMPR-009 | 压缩超时保护 | UT | P2 | SetTimeout 极小值 | 超时中断并返回错误 | ❌ |
| TC-CMPR-010 | compress_saved 统计 | E2E | P1 | 可压缩文件备份后 | compress_saved > 0 且 ≈ Σ(original_size−stored_size)（仅压缩文件） | ⚠️（E2E 有断言但 dashboard 聚合未赋值——已知限制） |

---

## M06 加密（TC-CRPT）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-CRPT-001 | 加解密往返（小/中/二进制/空） | UT | P0 | 各尺寸文件 Encrypt→Decrypt | 全部还原一致 | ✅（TestEncryptDecrypt* 系列） |
| TC-CRPT-002 | 跨 256KB 分块加密 | UT | P0 | 恰好 256KB、256KB+1、512KB 文件 | 分块边界正确；解密还原一致 | ⚠️（MediumFile 有，边界尺寸无显式断言） |
| TC-CRPT-003 | 密文结构 | UT | P1 | 解析密文 | salt(32B) 开头 + 每块 nonce(12B)+ciphertext+tag | ✅（TestEncryptedFileStructure） |
| TC-CRPT-004 | 同文件两次加密密文不同 | UT | P1 | 同一文件加密两次 | 输出不同（随机 salt/nonce）；均可解密还原 | ✅（TestMultipleEncryptsProduceDifferentOutput） |
| TC-CRPT-005 | DEK 派生唯一性 | UT | P1 | 不同 salt 派生 | DEK 不同 | ✅（TestDeriveDEKUniqueness） |
| TC-CRPT-006 | 首块 nonce 校验（IV 不匹配） | UT | **P0** | 用错误 IV 解密 | 报 `nonce mismatch on first chunk`，拒绝解密 | ✅（TestEncryptDecryptWrongIV） |
| TC-CRPT-007 | 密文篡改检测 | UT | **P0** | 翻转密文中部 1 字节 | GCM tag 校验失败，拒绝输出 | ✅（TestTamperedCiphertext） |
| TC-CRPT-008 | 主密钥不匹配（跨会话） | E2E | **P0** | 桶内旧密钥对象 + 新密钥会话去重跳过重传 → 恢复 | 报 `nonce mismatch on first chunk`；**测试结论标注为脏数据场景而非代码缺陷**（WIKI C.4） | ⚠️（真实 OSS 上偶发触发，无受控用例） |
| TC-CRPT-009 | 密钥文件权限 | UT | P1 | 生成后 stat | 0600 | ✅ |
| TC-CRPT-010 | chunkSize 最小值约束 | UT | P2 | SetChunkSize(512B) | 被钳制到 ≥1KB，不 panic | ❌ |
| TC-CRPT-011 | 存储无明文泄漏 | E2E | P1 | 抽取云端对象前 1KB | 已知明文模式（文件内容串）不出现在密文中 | ✅（E2E "No plaintext leak"） |

---

## M07 存储上传/下载（TC-UPLD）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-UPLD-001 | 上传/下载往返 | IT | P0 | Upload→Download→比对 | 字节级一致 | ⚠️ |
| TC-UPLD-002 | 上传重试（3 次指数退避） | IT | P1 | 注入前两次失败 | 2s→4s 退避后第三次成功；日志含重试记录 | ❌ |
| TC-UPLD-003 | 重试耗尽报错 | IT | P1 | 持续失败 | 3 次后返回含 stderr 的完整错误（不吞 rclone stderr） | ❌ |
| TC-UPLD-004 | Exists 检查 | IT | P1 | 存在/不存在的 key | true/false 正确 | ⚠️ |
| TC-UPLD-005 | Delete/DeleteBatch | IT | P1 | 删除对象 | 对象消失；批量删除全部生效 | ⚠️ |
| TC-UPLD-006 | GetStorageUsage | IT | P2 | rclone size 查询 | 返回 ≥0 字节数 | ❌ |
| TC-UPLD-007 | remoteSpec 路径构造 | UT | P1 | 构造 `oss:<bucket>/<key>` | S3 后端 bucket 在路径中（C.1 改造点） | ❌ |
| TC-UPLD-008 | rclone.conf 引号/字段清洗 | UT | P1 | storage_class 引号残留等 | repairRcloneConfig 清洗（去引号）；section 缺失不动 | ✅（TestNormalizeRemoteField 系列） |
| TC-UPLD-009 | 本地模式 OSS SDK 跳过 | UT | P1 | endpoint/bucket/AK/SK 全空 | OSS SDK 路径不进入（无 `bucket name len` 错误） | ⚠️ |
| TC-UPLD-010 | 真实 OSS 可列桶探针 | MAN | P0 | `./bin/rclone lsl oss: --config data/rclone.conf` | 能列出桶内容（备份失败排查第一步） | ❌（人工步骤，建议脚本化前置检查） |
| TC-UPLD-011 | 上传大对象分块 | E2E | P2 | ≥100MB 文件 | rclone 分块上传成功；下载还原一致 | ❌ |

---

## M08 定时调度（TC-SCHD）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-SCHD-001 | 合法 cron 启动 | UT | P0 | `0 3 * * *` Start | 调度器运行；NextRun 正确 | ✅（TestStartStopValidCron） |
| TC-SCHD-002 | 非法 cron 拒绝 | UT | P0 | `invalid` | Start 返回错误 | ✅（TestStartInvalidCron） |
| TC-SCHD-003 | 空 cron 表达式 | UT | P1 | 空串 | 拒绝或禁用（以实现为准），不 panic | ✅（TestStartEmptyCron） |
| TC-SCHD-004 | 动态更新表达式 | UT | P1 | 运行中 UpdateSchedule | 新表达式生效，NextRun 变化 | ✅（TestUpdateSchedule） |
| TC-SCHD-005 | 运行中更新非法表达式 | UT | P1 | 运行中更新为非法值 | 返回错误且旧调度保持不变 | ✅（TestUpdateScheduleInvalid） |
| TC-SCHD-006 | 调度触发备份 | IT | P0 | cron 设为 `* * * * *` 等待 1 分钟 | 自动触发一次备份会话；与手动触发等价 | ❌ |
| TC-SCHD-007 | 调度与手动并发防重 | IT | **P0** | 调度触发瞬间手动 trigger | 锁内双重检查：仅一个会话运行，另一个被拒（明确错误信息） | ❌ |
| TC-SCHD-008 | 时区正确性 | UT | P1 | Timezone=Asia/Shanghai | NextRun 按上海时区计算 | ⚠️ |
| TC-SCHD-009 | Stop 等待运行任务 | UT | P1 | 备份执行中 Stop | 优雅等待完成（不硬杀） | ⚠️ |
| TC-SCHD-010 | 启停幂等 | UT | P2 | Start 两次 / Stop 未启动 | 第二次 Start 报错或幂等；Stop 未启动不 panic | ✅（TestStartTwice / TestStopBeforeStart） |
| TC-SCHD-011 | 禁用调度不触发 | IT | P1 | enabled=false | 到点不触发 | ❌ |

---

## M09 恢复流程（TC-RSTR）

### 关键用例详述

**TC-RSTR-001 恢复主链路 + SHA-256 校验（P0，E2E）**

- 前置：至少一次成功备份；输出目录在 `restore_base_dirs` 白名单内。
- 步骤：
  1. 选 6 类代表文件（修改过的/未变的/空/二进制/可压缩/新增）。
  2. `POST /api/restore` `{paths, output_dir, conflict_strategy:"overwrite"}`。
  3. 轮询 `GET /api/restore/jobs/{id}` 至终态（**接受 completed / completed_with_errors / failed 三态**，见附录 B-1）。
  4. 逐文件比对 SHA-256。
- 预期：restored_files=6；每个恢复文件哈希与源一致；job 记录 total/restored/failed/elapsed 齐全。
- 覆盖：⚠️（E2E Phase 5 有，但终态判断只认 completed——陈旧断言）

### 用例表

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-RSTR-001 | 恢复主链路+完整性 | E2E | P0 | 见详述 | 见详述 | ⚠️ |
| TC-RSTR-002 | 恢复到原路径 | E2E | **P0** | `restore_to_original:true`（或 output_dir 留空） | job 记录 `output_dir='__original__'`；文件落回 `files.path` 原位置；后端日志 `path==output` | ❌（AGENTS.md 4.2 提供三步验证法，无自动用例） |
| TC-RSTR-003 | 冲突策略 skip | E2E | P0 | 目标已存在同名文件 + skip | 已存在文件跳过且记入失败列表；其余正常 | ❌ |
| TC-RSTR-004 | 冲突策略 overwrite | E2E | P0 | 目标已存在 + overwrite | 覆盖为备份版本内容 | ⚠️（E2E 用了 overwrite 但未构造真实冲突） |
| TC-RSTR-005 | 冲突策略 rename | E2E | P0 | 目标已存在 + rename | 新文件名追加时间戳；原文件保留；两者内容均正确 | ❌ |
| TC-RSTR-006 | 输出目录白名单拒绝 | API | **P0** | output_dir 不在 restore_base_dirs | 400/422 明确错误 `not under any allowed base directories`；不产生 job | ✅（TestValidateOutputDir） |
| TC-RSTR-007 | 路径遍历攻击 | API | **P0** | paths/output_dir 含 `../` 越界 | 被白名单校验拦截；无文件写出白名单外 | ⚠️（ValidateOutputDir 覆盖 output_dir；paths 遍历待确认补齐） |
| TC-RSTR-008 | 恢复不存在的路径 | API | P1 | paths 含从未备份的路径 | 创建 job 但该文件进 failed 列表；错误信息明确（或创建时 404，以实现为准） | ❌ |
| TC-RSTR-009 | 指定历史 backup_id 恢复 | E2E | P1 | 备份两次（文件内容不同），按旧 backup_id 恢复 | 恢复的是**旧版本**内容（哈希=旧版本） | ❌ |
| TC-RSTR-010 | backup_id=0 默认最近 | API | P1 | 不传 backup_id | 使用最近一次 completed（含 completed_with_errors？以实现为准并固化） | ⚠️ |
| TC-RSTR-011 | 大批量恢复 | E2E | P1 | 恢复 ≥100 文件 | 进度准确推进；SSE 不丢事件（允许 channel 满丢弃但日志有警告）；统计一致 | ❌ |
| TC-RSTR-012 | 恢复任务取消 | API | P0 | 运行中 cancel | 状态→cancelled；已恢复文件保留；后续文件不再处理；可立即发起新任务 | ❌ |
| TC-RSTR-013 | 单文件恢复失败不阻断 | E2E | P1 | 混入一个坏对象（如手工删 OSS 对象） | 该文件 failed；其余恢复完成；job=completed_with_errors | ❌ |
| TC-RSTR-014 | 恢复目录结构保持 | E2E | P1 | 多文件跨目录恢复 | 相对路径结构保留（公共前缀规则，参考 WIKI 11.5） | ⚠️ |
| TC-RSTR-015 | expedited 标志 | API | P2 | expedited=true 对 Archive 桶 | 记录告警并自动降级标准解冻（Archive 不支持加急，WIKI B.3） | ❌ |
| TC-RSTR-016 | 恢复中的文件级 SSE 事件 | API | P2 | 订阅恢复流 | phase 序列 preparing→thawing?→downloading→decrypting→decompressing?→verifying→moving→completed | ✅（TestRestorePhaseName UT 层） |
| TC-RSTR-017 | 重复发起恢复任务 | API | P0 | 已有 running 任务时再 POST | 拒绝 `a restore is already running`（单任务约束） | ✅（TestRestoreJobRepository_IsRunning） |
| TC-RSTR-018 | 恢复 jobs 列表过滤/分页 | API | P2 | status=completed 过滤、翻页 | 过滤与分页正确 | ✅（TestRestoreJobRepository_List UT 层） |
| TC-RSTR-019 | 空文件恢复 | E2E | P1 | 恢复 0 字节文件 | 还原 0 字节，哈希校验通过 | ✅（E2E 6c） |
| TC-RSTR-020 | 恢复 job 8h 超时保护 | UT | P2 | 注入永不完成的解冻 | 8h context 到期任务转 failed，error 含 deadline | ❌ |
| TC-RSTR-021 | 恢复到原路径时父目录已删 | E2E | P2 | 原路径父目录被删后恢复原路径 | 自动重建父目录（或明确报错，以实现为准并固化） | ❌ |
| TC-RSTR-022 | refresh-oss-db 接口 | API | P1 | POST /api/restore/refresh-oss-db | 从 OSS 重拉 DB；前端文件列表反映权威源；本地未完成 job 不被覆盖丢失（pullAndPreserveRestoreJobs） | ⚠️（TestRefreshOSSDBAPI 有） |
| TC-RSTR-023 | 可恢复文件列表接口 | API | P1 | dir_path/search/backup_id/page 各参数 | 过滤+分页+搜索正确；JOIN 无 ambiguous 错误（C.2 回归） | ✅（TestFileRepository_JoinByBackup_NoAmbiguousBackupID） |

---

## M10 归档解冻（TC-THAW）

> 依赖真实 OSS Archive/ColdArchive 桶（`verify_cloud_archive.py` 场景）。x-oss-restore 解析可 UT 化（无需真桶）。

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-THAW-001 | header 解析：解冻中（带引号） | UT | **P0** | `ongoing-request="true"` | completed=false, inProgress=true | ❌（AGENTS.md 4.4 明确指出缺失，建议加 storage_test.go） |
| TC-THAW-002 | header 解析：已解冻（带引号） | UT | **P0** | `ongoing-request="false", expiry-date="..."` | completed=true | ❌ |
| TC-THAW-003 | header 解析：无引号兼容 | UT | P1 | `ongoing-request=false` / `=true` | 兼容解析正确 | ❌ |
| TC-THAW-004 | header 缺失 + 归档存储类 | UT | P1 | StorageClass=Archive 且无 restore 头 | 判定需要解冻（B.3 修复点：空头≠可直接下载） | ❌ |
| TC-THAW-005 | 非归档对象直接下载 | UT | P1 | StorageClass=Standard | 不发起解冻，直接下载 | ❌ |
| TC-THAW-006 | RestoreObject 最小 XML body | UT | **P0** | 断言请求 body=`<RestoreRequest><Days>7</Days></RestoreRequest>` | 不含 Tier/JobParameters（防 MalformedXML 回归） | ❌ |
| TC-THAW-007 | 归档对象完整解冻恢复闭环 | MAN | **P0** | 真实 Archive 桶上传→恢复 | 解冻发起→轮询（30s 间隔）→下载→解密→哈希一致；Archive 约 60s 完成 | ⚠️（verify_cloud_archive.py 存在，需真实桶） |
| TC-THAW-008 | 解冻等待超时 | UT | P2 | 注入持续 ongoing | 6h（maxThawWait）后报 `object not restored after 6h`，任务失败不挂死 | ❌ |
| TC-THAW-009 | 等待期取消响应 | UT | P1 | 轮询中触发 cancel | select+time.After 立即退出（不被 30s 轮询阻塞） | ❌ |
| TC-THAW-010 | 解冻后 7 天窗口过期再访问 | MAN | P2 | 解冻 7 天后再次恢复 | 重新发起解冻（窗口过期对象回到归档态） | ❌ |
| TC-THAW-011 | 批量混合恢复（归档+标准） | E2E | P1 | 同批含归档与标准对象 | 归档的走解冻流程、标准的直接下载；顺序处理统计正确 | ❌ |
| TC-THAW-012 | CheckRestored 404 专门提示 | UT | P2 | 对象不存在时 | 返回含 404 上下文的错误提示（storage.go 专门提示，防双重哈希 GC 误删旧 bug） | ❌ |

---

## M11 OSS DB 同步与灾难恢复（TC-DBSYNC）

> 权威源模型：OSS `oss.db.enc` 唯一权威；本地 DB 是工作副本；备份前 PullOSSDB、备份后 PushOSSDB（旧版 `.bkup` 保留 N 份）。

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-DBSYNC-001 | 启动 InitFromOSS 拉取 | IT | **P0** | 本地 DB 落后于 OSS，启动 | 本地被权威版覆盖；备份记录与 OSS 一致 | ⚠️ |
| TC-DBSYNC-002 | 备份前 PullOSSDB | IT | P0 | 机器 B 发起备份前 | 先拉取 OSS 最新 DB（多机协同基础） | ❌ |
| TC-DBSYNC-003 | 备份后 PushOSSDB + 旧版本轮转 | IT | P0 | 连续两次备份 | 第二次 push 后旧版重命名 `.bkup`；保留数=配置 N，超出清理 | ✅（TestPruneBkupsKeys/TestBkupKeepCount UT） |
| TC-DBSYNC-004 | .bkup 命名模式 | UT | P1 | 生成文件名 | 符合 `oss.db.enc.bkup` 等既定模式 | ✅（TestBkupEncPattern） |
| TC-DBSYNC-005 | 多机共享桶同步 | MAN | P1 | 两实例同桶交替备份 | B 能看到 A 的备份记录（通过共享 oss.db） | ❌ |
| TC-DBSYNC-006 | bootstrap 灾难恢复 | CLI | **P0** | 删除本地 DB，restore-cli bootstrap | 列出版本→拉最新→解密落位；`backups` 命令立即可用 | ❌ |
| TC-DBSYNC-007 | bootstrap 自定义输出路径 | CLI | P2 | `bootstrap -o /tmp/x.db` | 写入指定路径不覆盖默认 | ❌ |
| TC-DBSYNC-008 | db-backup 手动触发 | CLI | P1 | restore-cli db-backup | 手动上传成功；版本轮转正常 | ❌ |
| TC-DBSYNC-009 | 恢复 jobs 导出降级 | UT | P2 | 导出 restore_jobs 失败（构造） | 主流程不被阻断，记录警告继续（戒条 7 失败降级） | ✅（TestRestoreJobRepository_ExportImport* 部分覆盖） |
| TC-DBSYNC-010 | Pull 与本地运行任务互斥 | IT | P1 | 备份运行中触发 refresh | 拒绝或等待（以实现为准），不得在备份中换库 | ❌ |
| TC-DBSYNC-011 | OSS DB 损坏/密钥错误 | MAN | P1 | 换 master.key 后 bootstrap | 解密失败报 `message authentication failed`，不落损坏 DB | ❌ |

---

## M12 垃圾回收（TC-GC）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-GC-001 | 孤儿对象宽限期内不删 | E2E | **P0** | ref_count=0 且 orphaned_at 未超期，触发 GC | OSS 对象保留；hash_index 保留 | ❌ |
| TC-GC-002 | 超宽限期孤儿删除 | E2E | **P0** | 手工把 orphaned_at 改早于宽限期，触发 GC | OSS 对象删除 + hash_index 记录删除 | ❌ |
| TC-GC-003 | ref>0 对象永不回收 | E2E | **P0** | 被引用对象触发 GC | 不删除（防数据丢失红线） | ❌ |
| TC-GC-004 | GC 异步执行 | API | P1 | POST /api/gc 立即返回 | 202/成功回执，后台执行 | ✅（TestGCEndpoint） |
| TC-GC-005 | GC 与备份互斥 | API | P1 | 备份运行中触发 GC | 拒绝或排队（以实现为准） | ❌ |
| TC-GC-006 | 已删除文件保留窗口 | UT | P1 | KeepDeletedDays 内外 | 窗口内 files.deleted 保留；超期可清理（与 GC 联动以实现为准） | ❌ |
| TC-GC-007 | GC 幂等 | E2E | P2 | 连续两次 GC | 第二次无对象可删，无错误 | ❌ |

---

## M13 数据一致性对账（TC-RCNL）

> 前端对账已收敛为后端自动（ReconcileIfNeeded），`/api/reconcile` 为运维逃生门。8 类不一致见 WIKI 3.6 表。

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-RCNL-001 | 健康库 dry-run 零问题 | API | P0 | 全新备份后 dry_run=true | 各类问题计数全 0；AppliedFixes=0 | ✅（TestReconcileAPI） |
| TC-RCNL-002 | dry-run 不修改任何数据 | API | **P0** | 构造漂移后 dry-run | 报告列出问题但 DB/OSS 无任何变化 | ⚠️ |
| TC-RCNL-003 | DanglingHashRefZero 修复 | UT | P1 | 构造 hash_index 有、OSS 无、ref=0 | 非 dry-run：删除 hash_index | ❌ |
| TC-RCNL-004 | DanglingHashRefNonZero 不修复 | UT | **P0** | 同上但 ref>0 | 仅记录错误，不自动修复（防误删） | ❌ |
| TC-RCNL-005 | OSSOnlyOrphans 仅告警 | UT | **P0** | OSS 有、hash_index 无 | 仅报告不删除（误删防线） | ❌ |
| TC-RCNL-006 | OrphanBackupFiles 删除 | UT | P1 | backup_files 有、hash_index 无、OSS 无 | 删除 orphan backup_files | ❌ |
| TC-RCNL-007 | 重建 hash_index（OSS 有对象） | UT | P1 | backup_files 指向缺失 hash_index 但 OSS 有对象 | 重建记录 ref_count=1 | ❌ |
| TC-RCNL-008 | RefCountMismatches 修正 | UT | **P0** | 手工 UPDATE ref_count 至错误值 | 修正为 backup_files 引用数（口径=storage_key 计数） | ✅（TestHashRepository_HasRefCountMismatches_CountsBackupFiles 基础） |
| TC-RCNL-009 | FailedBackupsWithFiles 修正 | UT | P1 | failed 备份但有 backup_files | 修正 completed 或清理 | ❌ |
| TC-RCNL-010 | CompletedBackupsNoFiles 修正 | UT | P1 | completed 备份无 backup_files | 修正为 failed | ❌ |
| TC-RCNL-011 | 备份运行中 409 | API | P0 | 备份运行中调用 reconcile | 409 Conflict | ✅（TestReconcileAPI 部分覆盖） |
| TC-RCNL-012 | 修复后 PushOSSDB 回流 | IT | **P0** | 非 dry-run 且 AppliedFixes>0 | 修复结果回写 OSS 权威源（否则下次 Pull 覆盖修复） | ❌ |
| TC-RCNL-013 | 无修复不回流 | IT | P1 | AppliedFixes=0 | 不上传 oss.db（避免无意义上传） | ❌ |
| TC-RCNL-014 | NeedsReconcile 轻量短路 | UT | P1 | 健康库 | NeedsReconcile=false → ReconcileIfNeeded 零开销返回（不列 OSS） | ❌ |
| TC-RCNL-015 | 备份完成自动触发自愈 | IT | P1 | 构造漂移→跑一次备份 | 后台自动 Reconcile(false) 修复并回流；无需人工 | ❌ |

---

## M14 SSE 实时进度（TC-SSE）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-SSE-001 | 连接与事件推送 | API | P0 | curl -N 订阅备份流，触发备份 | Content-Type: text/event-stream；收到 phase/progress/file/log 事件 | ❌ |
| TC-SSE-002 | 历史事件回放 | API | P1 | 备份进行中后接入 | 先收到历史事件（按时间序）再收实时 | ✅（TestRestoreProgressBroker_HistoryReplay UT 层） |
| TC-SSE-003 | 15s 心跳保活 | API | P1 | 静默期抓包 | `: heartbeat` 每 15s | ❌ |
| TC-SSE-004 | 客户端断开清理 | UT | P1 | 订阅后断开 | channel 清理，无 goroutine/channel 泄漏 | ✅（TestRestoreProgressBroker_Unsubscribe） |
| TC-SSE-005 | 慢客户端丢弃策略 | UT | P2 | channel 打满 | 非阻塞丢弃+警告日志（不阻塞备份主流程） | ❌ |
| TC-SSE-006 | 历史缓冲上限 | UT | P2 | 超过 200 条事件 | 保留最近 200 条 | ✅（TestRestoreProgressBroker_HistoryMaxSize） |
| TC-SSE-007 | 完成后 30s 清空历史 | UT | P2 | 备份完成后等待 | ClearHistory 生效 | ✅（TestRestoreProgressBroker_ClearHistory） |
| TC-SSE-008 | 恢复进度流 | API | P1 | 订阅 /api/restore/progress/stream | 恢复阶段事件正确推送 | ✅（UT 层有 Broker 测试） |
| TC-SSE-009 | SSE 写超时动态禁用 | MAN | P2 | 长连接 >60s（WriteTimeout 默认） | 连接不被服务端写超时切断 | ❌ |
| TC-SSE-010 | 断线自动重连（前端） | MAN | P2 | 断网 5s 恢复 | EventSource 自动重连并回放历史 | ❌ |

---

## M15 内容选择管理（TC-CONT）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-CONT-001 | 目录 CRUD | API | P0 | 增查改删备份目录 | 全链路成功；列表反映变化 | ✅（TestDirectories/TestBackupDirectory） |
| TC-CONT-002 | 目录 PATCH 部分更新 | API | P1 | 只传 `{enabled:false}` | 仅启用状态变化，其余字段不变 | ❌ |
| TC-CONT-003 | 重复目录路径拒绝 | API | P1 | 添加已存在 path | 唯一约束报错（UNIQUE） | ⚠️ |
| TC-CONT-004 | 排除规则 CRUD | API | P0 | 增查改删排除规则 | 全链路成功 | ✅ |
| TC-CONT-005 | 添加规则缺 pattern | API | P1 | POST 空 pattern | 400 拒绝 | ❌ |
| TC-CONT-006 | 非法 rule_type | API | P2 | rule_type=foo | 拒绝（CHECK 约束） | ❌ |
| TC-CONT-007 | fs/browse 路径浏览 | API | P1 | 浏览根/子目录 | FSEntry 标注 in_backup/partial_backup/has_update/will_backup 正确 | ✅（TestFSBrowse） |
| TC-CONT-008 | browse 不存在路径 | API | P2 | path=/no/such | 明确错误不 panic | ❌ |
| TC-CONT-009 | 部分纳入目录标注 | API | P1 | 目录纳入但含排除规则命中 | partial_backup=true（黄色徽章语义） | ⚠️ |
| TC-CONT-010 | 删除目录后备份 | E2E | P1 | 删除备份目录再触发 | 该目录文件不再扫描；已有 backup_files 历史保留 | ❌ |

---

## M16 策略配置（TC-STRAT）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-STRAT-001 | 五类配置读取默认值 | API | P1 | GET schedule/compression/upload/retention/encryption | 返回配置文件默认（DB 无覆盖时） | ✅（UT 层） |
| TC-STRAT-002 | 配置更新持久化到 config_kv | API | P0 | PUT 新值后 GET | DB 值优先于 YAML 默认；重启后仍生效 | ❌ |
| TC-STRAT-003 | 更新调度即时生效 | API | P1 | 运行中改 cron | 调度器动态更新（不重启） | ✅（TestUpdateSchedule） |
| TC-STRAT-004 | 非法压缩级别 | UT | P1 | level=99 | 校验拒绝 | ✅（TestValidateCompressionLevel） |
| TC-STRAT-005 | 非法端口 | UT | P2 | port=99999 | 校验拒绝 | ✅（TestValidatePortRange） |
| TC-STRAT-006 | 并发数边界 | API | P2 | concurrency=0 / 11 | 钳制或拒绝（以实现为准） | ❌ |
| TC-STRAT-007 | 加密算法只读 | API | P2 | 尝试改 algorithm | 拒绝或忽略（AES-256-GCM 固定） | ❌ |
| TC-STRAT-008 | 密钥路径更新 | API | P1 | 换 key_file_path | 新密钥加载；**警告**：与旧数据不兼容的后果应在 UI/日志提示 | ❌ |

---

## M17 仪表板统计（TC-DASH）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-DASH-001 | stats 全字段 | API | P0 | 备份后 GET stats | total_files/total_size/oss_storage_used/unique_hash_count/backup_count/last_backup_* 齐全且自洽 | ✅（TestDashboardStats/TestDashboard） |
| TC-DASH-002 | OSS 存储占用口径 | UT | P1 | 构造重复内容 | = SUM(stored_size) over DISTINCT storage_key | ❌ |
| TC-DASH-003 | 去重节省口径 | UT | P1 | 构造 ref=3 | = file_size×(ref−1) | ❌ |
| TC-DASH-004 | 空库 stats | API | P1 | 全新 DB | 全零值不报错；active_backup_running=false | ✅ |
| TC-DASH-005 | 备份运行中状态 | API | P1 | 备份中 GET | is_running=true（内存 OR DB 双查） | ⚠️ |
| TC-DASH-006 | history 分页 | API | P2 | page/size 组合 | 分页正确；size>200 被钳到 200 | ✅（TestBackupHistoryList） |
| TC-DASH-007 | storage/health 正常 | API | P1 | OSS 可达时 | 200 + latency_ms ≥ 0 | ❌ |
| TC-DASH-008 | storage/health 异常 503 | API | P1 | 断 OSS | 503 + 错误信息 | ❌ |
| TC-DASH-009 | storage/status 端点 | API | P2 | GET /api/storage/status | 返回 StorageStatus（与 health 的差异固化） | ❌ |

---

## M18 日志（TC-LOG）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-LOG-001 | 日志按 backup_id 过滤 | API | P1 | 备份后过滤 | 仅返回该会话日志 | ✅（TestLogs） |
| TC-LOG-002 | 级别过滤 | API | P2 | level=error | 仅 error | ✅ |
| TC-LOG-003 | 关键字搜索 | API | P2 | search=upload | message/detail 模糊命中 | ⚠️ |
| TC-LOG-004 | 时间范围过滤 | API | P2 | start/end | 区间过滤正确 | ❌ |
| TC-LOG-005 | 分页与 page_size 上限 | API | P2 | size=500 | 钳到 200 | ❌ |
| TC-LOG-006 | 单条日志详情 | API | P2 | GET /api/logs/{id} | detail 字段返回 | ✅ |
| TC-LOG-007 | 日志轮转 | UT | P2 | 超过 MaxSize | 轮转保留 maxFiles 个 | ❌ |
| TC-LOG-008 | 调用链完整性抽检 | MAN | P1 | 备份一轮后读日志 | 覆盖入口/关键步骤/结果/参数/耗时（戒条 9：应记尽记） | ❌ |

---

## M19 API 通用行为与安全（TC-API）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-API-001 | 标准响应包装 | API | P1 | 抽查各端点 | success/data/error 结构符合 WIKI 第 6 节 | ⚠️（响应格式不统一为已知项） |
| TC-API-002 | 非法 JSON body | API | P1 | POST 垃圾 body | 400 + 可读错误；不 panic | ❌ |
| TC-API-003 | 未知路由 404 | API | P2 | GET /api/nonexistent | 404 | ✅（TestMiddleware） |
| TC-API-004 | 方法不匹配 | API | P2 | DELETE /api/dashboard/stats | 405 | ❌ |
| TC-API-005 | CORS 头 | API | P2 | OPTIONS 预检 | 允许跨域头返回 | ✅（TestMiddleware） |
| TC-API-006 | 请求日志中间件分级 | UT | P2 | 4xx/5xx 请求 | 4xx→WARN、5xx→ERROR 日志 | ✅ |
| TC-API-007 | 并发 API 压力 | API | P2 | 50 并发 GET stats | 全部 200；无竞态崩溃 | ❌ |
| TC-API-008 | 路径参数注入 | API | P1 | id 传 `abc`/负数/超大数 | 400 或 404，不 500 | ❌ |
| TC-API-009 | 敏感信息不泄漏 | MAN | P1 | 错误响应/日志检查 | AK/SK/主密钥不出现在任何输出 | ❌ |

---

## M20 互斥与并发（TC-MUTX）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-MUTX-001 | 并发触发两个备份 | API | **P0** | 几乎同时 POST trigger ×2 | 仅一个 202；另一个明确拒绝（锁内双重检查：内存+DB） | ❌ |
| TC-MUTX-002 | 备份运行中发起恢复 | API | **P0** | 备份中 POST /api/restore | 拒绝 `a backup is currently running`（FAQ Q4） | ❌ |
| TC-MUTX-003 | 恢复运行中触发备份 | API | **P0** | 恢复中 POST trigger | 互斥拒绝 | ❌ |
| TC-MUTX-004 | 双恢复任务 | API | P0 | 恢复中再建恢复 | 拒绝 `a restore is already running`（FAQ Q5） | ✅（UT 层 IsRunning） |
| TC-MUTX-005 | 备份中取消再取消 | API | P2 | cancel 两次 | 第二次幂等或明确"无运行中任务" | ❌ |
| TC-MUTX-006 | 取消不存在的 backup_id | API | P2 | cancel?id=99999 | 明确错误，不误清理 | ❌ |
| TC-MUTX-007 | 备份中调用 reconcile | API | P0 | 备份中 POST /api/reconcile | 409 Conflict | ✅（TestReconcileAPI） |
| TC-MUTX-008 | DB 残留 running 兜底取消 | API | P1 | 构造 DB running 但内存无 → cancel | 兜底清理 DB 残留记录 | ❌ |

---

## M21 崩溃恢复（TC-CRSH）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-CRSH-001 | 备份中 kill -9 后重启 | E2E | **P0** | 上传阶段 kill -9 → 重启 | 残留 running→failed；可立即发起新备份；已上传对象保留（去重可复用） | ⚠️（CleanupStaleRunning UT 有，全链路无） |
| TC-CRSH-002 | 恢复中 kill -9 后重启 | E2E | P1 | 恢复中 kill -9 → 重启 | 残留 restore job→failed；可发起新恢复 | ✅（UT 层） |
| TC-CRSH-003 | 崩溃后 hash_index 漂移自愈 | E2E | P1 | 崩溃制造 ref 漂移 → 下次备份 | ReconcileIfNeeded 自动修复（NeedsReconcile 命中） | ❌ |
| TC-CRSH-004 | 崩溃后孤儿对象存在性校验 | E2E | P1 | 崩溃遗留 hash_index 但对象未传完 → 再备份 | 上传前 Exists 校验发现缺失 → 重新上传 | ❌ |
| TC-CRSH-005 | WAL 模式断电一致性 | MAN | P1 | 备份写入中模拟掉电（容器 kill） | SQLite WAL 恢复，DB 可打开（integrity_check ok） | ❌ |

---

## M22 restore-cli（TC-CLI）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-CLI-001 | backups 列表 | CLI | P1 | `restore-cli backups` | 最近 20 条：ID/状态/文件数/大小/完成时间 | ❌ |
| TC-CLI-002 | list 全量/目录/backup-id 过滤 | CLI | P1 | 三种调用 | 过滤正确；`--backup-id` 生效只列该会话文件 | ❌ |
| TC-CLI-003 | info 含 Lossless 与 StorageRatio | CLI | P1 | info 单文件 | 输出 Lossless=true（原始大小=OriginalSize） | ❌ |
| TC-CLI-004 | verify 单文件闭环 | CLI | **P0** | verify 已备份文件 | `✓ VERIFIED — hash matched`（下载→解密→解压→SHA-256）；临时目录自动清理 | ❌（无自动用例） |
| TC-CLI-005 | verify 失败输出 | CLI | P1 | verify 被篡改/删对象文件 | FAILED + 明确原因 | ❌ |
| TC-CLI-006 | verify-dir limit | CLI | P1 | `--limit 10` / `--limit 0` | 抽样 10 个 / 全量；Summary 的 Failed=0 | ❌ |
| TC-CLI-007 | restore 单文件路径规则 | CLI | P0 | restore /a/b/f.txt -o /out | 落地为 `/out/b/f.txt`（保留父目录名） | ❌ |
| TC-CLI-008 | restore-dir 公共前缀 | CLI | P0 | 目录含子目录恢复 | 相对结构保留（WIKI 11.5 规则） | ❌ |
| TC-CLI-009 | restore 冲突不静默覆盖 | CLI | P1 | 目标已存在同名文件 | 报错或明确策略（不静默覆盖——11.8 修复点） | ❌ |
| TC-CLI-010 | -o 缺失报错 | CLI | P2 | restore 不带 -o | 用法错误提示 | ❌ |
| TC-CLI-011 | 不存在文件恢复 | CLI | P2 | restore /no/such | `no backup file record found` | ❌ |
| TC-CLI-012 | --expedited 对 Archive | CLI | P2 | Archive 桶 expedited | 告警降级标准（B.3） | ❌ |
| TC-CLI-013 | CLI 与 API 行为一致 | IT | P1 | 同文件 CLI verify + API 恢复 | 结果一致（共享 Restorer） | ❌ |

---

## M23 部署与运维脚本（TC-DEP）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-DEP-001 | deploy.sh 默认=生产 | MAN | **P0** | 无参数执行（检查不实际推送） | 连接真实 OSS（config.yaml 桶）；**--local 才是本地模拟**（2026-08-17 掉头后的语义） | ❌ |
| TC-DEP-002 | deploy.sh --local 本地模拟 | MAN | P0 | --local 部署 | rclone remote 为本地目录；E2E 可跑 | ❌ |
| TC-DEP-003 | macOS deploy 无需 sudo | MAN | P1 | macOS 部署 | 全程无 sudo 要求 | ❌ |
| TC-DEP-004 | Debian systemd+nginx 部署 | MAN | P1 | Debian 全流程 | 服务自启、nginx 反代、SSE 配置生效（proxy_buffering off） | ❌ |
| TC-DEP-005 | start.sh start/stop/status | MAN | P1 | macOS 三命令 | PID 文件管理正确；stop 后端口释放 | ❌ |
| TC-DEP-006 | update-debian.sh 更新 | MAN | P2 | git pull+构建+重启 | 健康检查通过；rollback 可用 | ❌ |
| TC-DEP-007 | verify-e2e.sh --skip-build | MAN | P0 | 完整 E2E | 报告生成 test-env/acceptance-report.html；已知 3 个陈旧断言按附录 B 处理 | ⚠️ |
| TC-DEP-008 | verify_cloud_archive.py | MAN | P1 | 真实 Archive 桶 | 上传→解冻→恢复→SHA-256 全通过 | ⚠️ |
| TC-DEP-009 | nas_file_generator 生成 | MAN | P2 | --count/--size/--seed | 可复现数据集 | ❌ |
| TC-DEP-010 | 资源被 root 写坏后修复 | MAN | P2 | chown -R 恢复 | macOS 免 sudo 启动恢复 | ❌ |

---

## M24 前端 UI（TC-FE）

| ID | 用例 | 层级 | 优先级 | 步骤要点 | 预期结果 | 覆盖 |
|----|------|------|--------|----------|----------|------|
| TC-FE-001 | Dashboard 实时进度 | MAN | P0 | 触发备份观察 | 进度条/阶段名/当前文件/日志尾行实时刷新；完成后回 idle | ❌ |
| TC-FE-002 | SSE 断连降级轮询 | MAN | P1 | 断开 SSE | 降级 2s 轮询不白屏；恢复后回到 SSE | ❌ |
| TC-FE-003 | 恢复配置面板 | MAN | **P0** | 选择恢复目标（原路径/自定义目录）+冲突策略 | 请求体 restore_to_original/output_dir 正确（抓包验证 AGENTS.md 4.2 方法 3） | ❌ |
| TC-FE-004 | 恢复历史与状态徽章 | MAN | P1 | 完成恢复后 | 历史 列表刷新；状态徽章（completed_with_errors 应有对应展示） | ❌ |
| TC-FE-005 | 文件多选跨页保持 | MAN | P1 | 翻页选择 | 选中集合保持 | ❌ |
| TC-FE-006 | 更新按钮调 refresh-oss-db | MAN | P1 | 点击"更新" | 调 POST /api/restore/refresh-oss-db；列表刷新 | ❌ |
| TC-FE-007 | 危险操作二次确认 | MAN | P1 | 取消备份/GC | ConfirmDialog 弹出 | ❌ |
| TC-FE-008 | tsc 类型检查 | CLI | P0 | `tsc -b --noEmit` | 0 错误 | ✅（构建链） |
| TC-FE-009 | 中文界面与数据渲染 | MAN | P2 | 中文路径文件展示 | 无乱码 | ❌ |
| TC-FE-010 | 空态/骨架屏 | MAN | P2 | 无数据页面 | EmptyState/LoadingSkeleton 正常 | ❌ |

---

## E2E 串联场景（TCE）

跨模块业务闭环，跑通即视为版本可发布基线。

| ID | 场景 | 优先级 | 场景编排 | 通过判据 |
|----|------|--------|----------|----------|
| TCE-01 | 全新环境首备+恢复闭环 | P0 | 清库清桶→生成数据→备份→恢复到自定义目录→SHA-256 全比对 | 备份 completed；恢复 restored=total、failed=0；哈希全一致 |
| TCE-02 | 增量轮次闭环 | P0 | 首备→改/增/删→再备→按旧版本恢复→按新版本恢复 | 旧版本恢复旧内容；ref_count 变化符合不变量 |
| TCE-03 | 恢复到原路径全流程 | P0 | 备份→删源文件→恢复原路径 | 文件回原位；job.output_dir='__original__'；日志 path==output |
| TCE-04 | 灾难恢复演练 | **P0** | 备份→删本地 DB→bootstrap→恢复全量数据 | bootstrap 成功；数据完整恢复（RESTORE_GUIDE 场景 B） |
| TCE-05 | 崩溃自愈闭环 | P1 | 备份中 kill -9→重启→再备份→verify | 残留清理；新备份成功；对账自愈无残留告警 |
| TCE-06 | 多机共享桶协同 | P1 | 实例 A 备份→实例 B 启动→B 可见 A 记录→B 恢复 A 的文件 | 权威 DB 同步生效 |
| TCE-07 | 归档解冻全链路 | P0（需真桶） | Archive 上传→触发恢复→解冻等待→下载校验 | 全链路成功（verify_cloud_archive.py） |
| TCE-08 | GC 全生命周期 | P1 | 备份→删源→再备（ref=0）→GC（未到期不删）→改 orphaned_at→GC（删） | 宽限期语义正确 |
| TCE-09 | 互斥矩阵 | P0 | 备份×恢复×GC×reconcile 两两并发 | 非法组合全部被拒且错误信息准确 |
| TCE-10 | 对账修复回流 | P1 | 人为制造 ref 漂移→dry-run 报告→非 dry-run 修复→重启→再 dry-run | 二次 dry-run 零问题（修复已回流 OSS 权威源） |

---

## 附录 A：现有测试覆盖映射

### A.1 自动化资产

| 资产 | 位置 | 规模 |
|------|------|------|
| Go 单元测试 | `nas-backup-backend/internal/**/*_test.go` | 18 个文件、约 176 个测试函数 |
| Python E2E | `scripts/verify_e2e.py` | 23 项断言、6 阶段 |
| 云归档专项 | `scripts/verify_cloud_archive.py` | 需真实 OSS Archive 桶 |
| E2E 编排 | `scripts/verify-e2e.sh` | 启动后端+跑套件+HTML 报告 |

### A.2 模块覆盖度速览（本文档用例 vs 现有自动化）

| 模块 | 用例数 | 已覆盖 | 部分覆盖 | 缺失 | 覆盖率估计 |
|------|--------|--------|----------|------|------------|
| M01 启动 | 16 | 5 | 4 | 7 | ~40% |
| M02 备份 | 22 | 3 | 6 | 13 | ~30% |
| M03 扫描 | 19 | 15 | 2 | 2 | ~85% |
| M04 去重 | 10 | 4 | 3 | 3 | ~55% |
| M05 压缩 | 10 | 8 | 2 | 0 | ~90% |
| M06 加密 | 11 | 8 | 2 | 1 | ~85% |
| M07 存储 | 11 | 1 | 5 | 5 | ~25% |
| M08 调度 | 11 | 7 | 2 | 2 | ~70% |
| M09 恢复 | 23 | 6 | 6 | 11 | ~40% |
| M10 解冻 | 12 | 0 | 2 | 10 | ~10% |
| M11 DB 同步 | 11 | 3 | 3 | 5 | ~40% |
| M12 GC | 7 | 1 | 0 | 6 | ~15% |
| M13 对账 | 15 | 2 | 2 | 11 | ~20% |
| M14 SSE | 10 | 4 | 1 | 5 | ~45% |
| M15-24 其余 | — | 少量 | 少量 | 大量 | 低 |

> **结论**：底层算法模块（扫描/压缩/加密/调度）UT 覆盖良好；**业务链路层（备份执行、恢复执行、解冻、GC、对账、DB 同步、互斥、崩溃恢复）是系统性缺口**，建议按 P0 用例优先补齐（与本文件用例 ID 一一对应）。

---

## 附录 B：已知陈旧断言与测试债清单

> 以下为文档核对中确认的**测试资产自身问题**（非产品缺陷），补测试时必须同步修正：

| # | 问题 | 位置 | 影响 | 修正方向 |
|---|------|------|------|----------|
| B-1 | `wait_for_restore_completion` 只认 `completed`/`failed`，不认 `completed_with_errors` | verify_e2e.py:168 | 合法终态被误判为超时（WIKI C.4 记录的真实事故） | 终态集合扩为三值 |
| B-2 | 「Client-side encryption」断言读本地 `test-env/local-cloud-storage`，OSS 直连后该目录恒空 | verify_e2e.py:509 | 恒 FAIL 假阴性 | 改为 `rclone lsl oss:` 拉对象清单校验 `.enc` 与密文头 |
| B-3 | 「Backup types recorded」断言基于已删除的版本概念 | verify_e2e.py（历史） | 恒 FAIL | 删除该断言（去版本化） |
| B-4 | E2E 增量阶段注释与断言仍用 full/incremental 语义 | verify_e2e.py Phase 4 | 语义漂移 | 改为"第二次备份会话只处理变更" |
| B-5 | 解冻 header 解析（带引号两种值）无 UT | storage_test.go 缺失 | B.3 级回归无防护 | 新增 TC-THAW-001~004 对应 UT（AGENTS.md 4.4 已给出模板） |
| B-6 | wait_for_backup_completion 终态含 `partial`（非真实枚举值） | verify_e2e.py:154 | 永不匹配的死代码 | 改为 6 值终态集合 |
| B-7 | 真实 OSS E2E 非幂等（归档解冻 + 桶残留旧密钥对象） | verify-e2e.sh | 跨轮次 nonce mismatch 假失败 | 全绿基线 = 清桶+全新备份（运行手册固定为前置） |

---

> **文档版本**：v1.0（2026-08-22）
> **下一步**：经确认后按 P0 优先级补齐自动化（建议顺序：B-5 解冻 UT → TCE-01/03 闭环 E2E 修正 → M20 互斥 → M21 崩溃 → M13 对账构造用例）。
