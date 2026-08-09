# NAS Backup System - 故障排查指南

> 统一故障排查入口。涵盖环境/配置/代码/解冻/恢复各类问题。
> 如本文档与真实代码不一致，**以真实代码为准**。

---

## 一、环境与依赖问题

| 症状 | 根因 | 解决方案 |
|------|------|----------|
| `go: command not found` | Go 不在 PATH（用户目录安装） | `export PATH="$HOME/go-sdk/go/bin:$PATH"` 或 `export PATH="/usr/local/go/bin:$PATH"` |
| `no Go files in nas-backup-backend` | 错误执行 `go build .` | main.go 在子目录，必须 `go build -o nas-backup ./cmd/nas-backup/` |
| `exec: "rclone": executable file not found` | `nas-backup-backend/bin/rclone` 不存在或不可执行 | 重新部署：`./scripts/deploy.sh`；或手动复制 rclone 到 `bin/rclone` 并 `chmod +x` |
| rclone 下载网络问题 | 国内访问 downloads.rclone.org 慢 | 使用镜像或从已安装系统复制；项目 deploy.sh 已内置下载逻辑 |
| `operation not permitted: sudo` | sandbox 限制 sudo | 将工具安装到用户目录（`$HOME/go-sdk/`, `$HOME/.local/bin`） |

### 中文路径与环境变量坑

| 症状 | 根因 | 解决方案 |
|------|------|----------|
| `no matches found` 错误 | zsh 对通配符不匹配时报错，中文路径加剧 | 用变量引用路径：`rm -rf "$DIR"/*` 而非 `rm -rf /path/中文/*` |
| `Pathname can't be converted from UTF-8 to current locale` | tar 依赖 locale 设置 | `export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8` 后再解压 |
| 中文路径乱码 | 终端 locale 未设置 UTF-8 | `export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8` |

---

## 二、配置问题

| 症状 | 根因 | 解决方案 |
|------|------|----------|
| 备份运行但 0 文件处理 | `backup.directories` 为空或路径不对 | 检查 config.yaml 的 `backup.directories` 路径是否存在；通过 Web UI 或 API 添加 |
| `output directory is not under any allowed base directories` | 恢复目录白名单限制 | 在 config.yaml 的 `server.restore_base_dirs` 添加允许的父目录；或使用 `restore_to_original=true` |
| rclone crypt remote 找不到上游 | rclone.conf 中 raw remote 名称不对 | raw remote **必须**命名为 `[oss]`，crypt remote 命名为 `[oss-crypt]` |
| 上传文件找不到/加密不生效 | crypt remote 的 `remote = oss:path` 路径错误 | 格式为 `remote = oss:<绝对路径>`，不要漏掉冒号 |
| 后端启动失败 | config.yaml 语法错误 / `data/` 目录权限 / 端口占用 | `go run ./cmd/nas-backup/ -config config.yaml` 看详细错误；检查 8080 端口 |

### config.yaml 关键字段速查

```yaml
server:
  port: 8080
  restore_base_dirs:           # 恢复目录白名单（自定义目录模式需要）
    - "/abs/path/allowed"

oss:                          # 有值时才用 OSS SDK 发起解冻请求；全空 = 本地测试模式
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  bucket: "my-archive-bucket"
  access_key_id: "LTAIxxx"
  access_key_secret: "xxxx"
  region: "cn-hangzhou"

rclone:
  binary_path: "./bin/rclone"
  config_path: "./data/rclone.conf"
  remote_name: "oss-crypt"    # 必须与 rclone.conf 的 section 名一致
```

---

## 三、代码逻辑问题

| 症状 | 根因 | 解决方案 |
|------|------|----------|
| `bucket name len is between [3-63], now is 0` | `oss.*` 全空但 OSS SDK 代码路径误进入 | storage.go 有 AK/SK/Endpoint/Bucket 四重检查；确认未回退到旧代码 |
| `404 NoSuchKey` in CheckRestored | OSS 对象不存在；早期版本「双重哈希 Bug」导致 GC 误删 | storage.go 已给专门错误提示，按提示重跑备份 |
| 同批次重复文件 `nonce mismatch` | 同一备份批次内完全相同文件的 edge case | 跨批次去重正常；同批次是小概率，不影响核心功能；第二次增量备份时跨批次去重正确 |
| API 返回结构不一致 | 有些 API 返回 `{"success":true,"data":{...}}`，有些直接返回 `{"backup_id":x}` | 测试脚本兼容处理：`trig.get("data", {}).get("backup_id") or trig.get("backup_id")` |

---

## 四、解冻（Archive/ColdArchive）专项排查

### 4.1 核心概念：x-oss-restore header 两种值（**都带引号**）

```
正在解冻中：    x-oss-restore: ongoing-request="true"
已解冻（可用）：x-oss-restore: ongoing-request="false", expiry-date="..."
```

### 4.2 常见解冻异常

| 现象 | 排查方法 |
|------|----------|
| 前端一直显示「正在解冻中」> 1 小时（Archive 应 1 分钟内完成） | 1. Go 后端日志找 `slog.Info("archive object restored")`；2. 没有这句说明 `completed` 匹配不到 → 查是否回到无引号旧代码 |
| `RestoreObject` 报错 `MalformedXML / GlacierJobParameters is not supported` | 误用 `RestoreObjectDetail(RestoreConfiguration{Days:7})`（SDK 强制填 Tier=Standard，Archive 不支持）→ 必须用 `RestoreObjectXML` 发送最小 body |
| 单文件解冻超过 6 小时导致恢复失败 | restore.go `maxThawWait=6h`；Deep Cold Archive 可能 48h，需同时调整 restore_job.go 的 8h 超时 |
| 恢复被 context 取消且无其他错误 | restore_job.go `context.WithTimeout` 太短；默认 8h |
| `rclone: copyto failed: ... storage class Archive. Please restore first.` | 解冻未完成就提前下载；确保 `RestoreObject` 发出后 `CheckRestored` 返回 true 再 Download |

### 4.3 手动调试验证 CheckRestored

```bash
# 方法 1: 在 storage.go:CheckRestored 加 slog（改完 go build!）
#   slog.Info("debug thaw header", "key", ossKey, "header", restoreHeader, "storage_class", storageClass)

# 方法 2: 用 ossutil 直接看对象 meta
ossutil64 meta oss://bucket-name/path/to/object.bin | grep x-oss-restore
ossutil64 ls -s oss://bucket-name/path/   # 看 StorageClass: Archive/ColdArchive
```

### 4.4 判断解冻请求是否正确发出

- ✅ **正确**：`POST /objectKey?restore` body = `<RestoreRequest><Days>7</Days></RestoreRequest>`
  - 代码用 `RestoreObjectXML(ossKey, restoreXML)` 发送精确 body
- ❌ **错误 1**：body 为空（旧版 `bucket.RestoreObject(ossKey)`）→ 解冻静默不触发
- ❌ **错误 2**：body 含 `<JobParameters><Tier>Standard</Tier>` → Archive 桶不支持

> ⚠️ `RestoreObjectDetail(RestoreConfiguration{Days:7})` 在 SDK 内部会强制给 `Tier=""` 填上 `"Standard"`，对 Archive 桶必踩 MalformedXML 坑。详见 [WIKI 附录 B.3](WIKI.md#b3-归档存储检测逻辑错误--restoreobject-malformedxml严重)。

### 4.5 阿里云 OSS 解冻官方文档

- 解冻状态判断：https://help.aliyun.com/zh/oss/how-do-i-check-whether-the-oss-file-is-unfrozen
- V2 API RestoreObject：https://help.aliyun.com/zh/oss/developer-reference/v2-restore-an-object
- 用户指南解冻：https://help.aliyun.com/zh/oss/user-guide/restore-objects-for-access

---

## 五、恢复功能排查

### 5.1 判断恢复到原路径是否生效

**方法 1：看后端日志**

```
file restored path=<原文件绝对路径> output=<原文件绝对路径>
```
`path == output` 说明用原路径；`output` 以 `restore_base_dirs` 前缀开头说明是自定义目录。

**方法 2：看 restore_jobs DB 记录**

```bash
sqlite3 nas-backup-backend/data/nas-backup.db \
  "SELECT id, paths, output_dir, status, restore_to_original FROM restore_jobs ORDER BY id DESC LIMIT 5;"
```
归一化后 `output_dir` 字段为 `__original__`（非空串）。

**方法 3：手动抓包**

```bash
curl -s -X POST http://127.0.0.1:8080/api/restore \
  -H 'Content-Type: application/json' \
  -d '{"paths":["/要恢复的文件路径"],"backup_id":1,"output_dir":"","restore_to_original":true,"conflict_strategy":"skip","expedited":false}'
```

### 5.2 restore-cli 单文件恢复调试

```bash
cd nas-backup-backend
./restore-cli -file "/path/to/file" -backup-id 1 -to-original -config ./config.yaml
```

---

## 六、验证与测试排查

| 症状 | 解决方案 |
|------|----------|
| 测试脚本 SHA256 不匹配 | 检查恢复前是否修改了源文件；检查是否恢复了旧版本而非最新 |
| 测试受旧数据干扰 | 每次运行验证前删除旧 `nas-backup.db`：`rm -f data/nas-backup.db*` |
| 加密文件找不到 | rclone crypt 加密了文件名，用 `rclone ls oss-crypt:` 查看真实存储结构 |
| `/api/health` 返回 404 | 没有 `/api/health` 端点，用 `/api/dashboard/stats` 代替 |

### 去重验证策略

同批次内重复文件去重和跨批次去重表现不同。可靠验证方法：
1. 第一次全量备份后，新增一个已知内容的重复文件
2. 触发第二次（增量）备份
3. 检查 `skipped_by_dedup` 计数

---

## 七、快速问题排查清单

| 症状 | 排查项 |
|------|--------|
| 后端启动失败 | 检查 config.yaml 语法；检查 `data/` 目录权限；检查 8080 端口占用 |
| 备份 0 文件 | 检查 `backup.directories` 路径存在；检查路径是否在 exclusions 中被排除 |
| 恢复报白名单错误 | 在 `server.restore_base_dirs` 添加目标目录，或用 `restore_to_original=true` |
| 恢复报 bucket name 错误 | 确认 storage.go 有 endpoint/bucket 空值检查（本地模式跳过 OSS SDK） |
| rclone 命令找不到 | 检查 `rclone.binary_path` 配置；确认 `./bin/rclone` 可执行 |
| 中文路径乱码 | `export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8` |
| 加密文件找不到 | `rclone ls oss-crypt:` 查看加密后的存储结构 |
| 前端一直「解冻中」 | 见[第四节解冻专项排查](#四解冻archivecoldarchive专项排查) |

---

## 参考

- [WIKI.md 附录 B](WIKI.md#附录-b历史关键-bug-修复记录2026-08-验收) — 3 个关键 Bug 修复记录
- [AGENTS.md](../AGENTS.md) — 项目运行/调试命令速查
- [DEPLOYMENT.md](DEPLOYMENT.md) — 部署指南与故障排查
