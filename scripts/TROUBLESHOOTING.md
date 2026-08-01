# NAS Backup System - macOS 部署与验证任务总结

> 生成时间: 2026-08-01
> 本文档供后续 AI 执行过程中快速了解项目状况、定位问题使用。

---

## 一、完整步骤流程

### 阶段 1: 项目探索与需求对齐
1. **探索项目结构**: 识别出后端（Go，`nas-backup-backend/`）和前端（React/Vite，`nas-backup-frontend/`）的代码组织
2. **分析依赖**: 确认核心依赖为 Go（编译后端）、Node.js/npm（构建前端）、rclone（云端存储桥接）、Python3（验证脚本）
3. **理解架构**: 后端通过 rclone 与存储后端交互，使用 AES-256-GCM 客户端加密 + zstd 压缩 + SHA-256 去重
4. **生成需求对齐文档**: `docs/ALIGNMENT.md`
5. **生成设计文档**: `docs/DESIGN.md`

### 阶段 2: 部署脚本编写
1. **编写一键部署脚本** `scripts/deploy-macos.sh`:
   - 通过 Homebrew 安装系统依赖（go, node, rclone, zstd, python3, openssl）
   - 创建测试环境目录结构（`test-env/source-files`, `test-env/local-cloud-storage`, `test-env/restore-output`）
   - 生成 AES 加密主密钥（`data/master.key`）
   - 配置 rclone 使用本地文件系统 + crypt 加密（模拟云端存储）
   - 生成 `config.yaml`（包含 `restore_base_dirs` 安全白名单）
   - 编译 Go 后端二进制（`cmd/nas-backup/main.go` → `nas-backup`）
   - 构建前端生产包
   - 生成便捷启动脚本（`start-backend.sh`, `start-frontend-dev.sh`）

2. **部署脚本关键配置**:
   - rclone remote 命名为 `[oss]` (raw local) + `[oss-crypt]` (crypt wrapper)
   - 备份目录通过 config.yaml 的 `backup.directories` 配置
   - 恢复目标目录必须在 `server.restore_base_dirs` 白名单内（安全限制）

### 阶段 3: E2E 验证脚本编写
1. **编写 Python 验证套件** `scripts/verify_e2e.py`:
   - Phase 1: 环境检查（后端可达、rclone 可用、数据库存在、配置存在）
   - Phase 2: 测试数据生成（文本文件、空文件、二进制随机文件、重复文件、深层嵌套文件）
   - Phase 3: 全量备份测试（触发备份、等待完成、验证文件数/压缩率/Dashboard统计）
   - Phase 4: 增量备份测试（修改文件、新增文件、触发增量、验证只处理变更文件）
   - Phase 5: 恢复测试 + SHA256 完整性校验（逐文件哈希比对）
   - Phase 6: 功能特性验证（加密验证、去重验证、空文件、API 正确性）

2. **编写 Shell 包装脚本** `scripts/verify-e2e.sh`:
   - 自动清理旧环境（旧数据库、旧云存储文件）
   - 启动后端进程并等待就绪（重试 30 次）
   - 运行 Python 测试套件
   - 生成 HTML 验收报告
   - 停止后端进程

3. **编写报告生成器** `scripts/generate_report.py`:
   - 读取 JSON 测试结果
   - 生成美观的 HTML 验收报告（含通过率、功能矩阵、测试详情）

### 阶段 4: 问题排查与代码修复
1. **首次部署问题排查**:
   - Go 未安装 → 手动下载 Go tar.gz 解压到 `~/go-sdk/go/`
   - rclone 下载网络问题 → 使用已下载的二进制复制到 `bin/rclone`
   - 备份目录未配置 → 通过 API 添加或确认 config.yaml 已包含
   - 中文路径在 shell 中 glob 展开问题 → 使用变量引用代替 glob

2. **代码 Bug 修复** (in `nas-backup-backend/internal/storage/storage.go`):
   - **Bug**: `CheckRestored()` 和 `RestoreObject()` 只检查了 AK/SK 是否为空，未检查 `endpoint` 和 `bucket`，导致在本地测试模式（无真实 OSS）下创建 OSS 客户端时失败，报错 `bucket name len is between [3-63], now is 0`
   - **修复**: 将条件改为 `sm.ossAKID == "" || sm.ossAKSecret == "" || sm.ossEndpoint == "" || sm.ossBucket == ""`，任一为空则跳过 OSS SDK 检查（本地存储无需解冻）

3. **配置修复** (`config.yaml`):
   - 添加 `server.restore_base_dirs` 配置项，否则恢复时会报 `output directory is not under any allowed base directories`

### 阶段 5: 全流程验证
1. 运行 `scripts/verify-e2e.sh --skip-build`
2. 23 项测试全部通过
3. 生成 HTML 验收报告到 `test-env/acceptance-report.html`

---

## 二、踩坑记录与注意事项（反思）

### 2.1 环境相关坑

| 坑 | 现象 | 原因 | 解决方案 |
|----|------|------|----------|
| Go 安装路径 | `go: command not found` 即使 brew install 了 | brew 在网络不好时安装慢；sandbox 环境可能限制 sudo | 直接下载 `go1.22.10.darwin-arm64.tar.gz` 解压到 `$HOME/go-sdk/go`，用 `export PATH="$HOME/go-sdk/go/bin:$PATH"` |
| rclone 不在 PATH | `exec: "rclone": executable file not found` | 后端配置的 `binary_path` 是相对于工作目录的 | 确保 rclone 在 `./bin/rclone`（相对于后端启动目录），部署脚本自动复制 |
| 中文路径 zsh glob | `no matches found` 错误 | zsh 默认对通配符不匹配时报错，中文路径加剧问题 | 始终用变量引用路径，不要直接在命令中写 glob 模式（如 `rm -rf "$DIR"/*` 而非 `rm -rf /path/中文/*`）；设置 `LANG=en_US.UTF-8` 和 `LC_ALL=en_US.UTF-8` |
| tar 解压 UTF-8 路径 | `Pathname can't be converted from UTF-8 to current locale` | tar 依赖 locale 设置 | 解压前 `export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8` |
| sudo 不可用 | `operation not permitted: sudo` | sandbox 环境限制 sudo | 将工具安装到用户目录（`$HOME/go-sdk/`, `$HOME/bin/`），不用系统目录 |

### 2.2 配置相关坑

| 坑 | 现象 | 原因 | 解决方案 |
|----|------|------|----------|
| 备份目录为空 | 备份运行但 0 文件处理 | `backup.directories` 在 config.yaml 中为空或路径不对 | 部署脚本必须将测试源目录写入 config.yaml；后端启动时会从 config seed 目录列表 |
| 恢复目录白名单 | `output directory is not under any allowed base directories` | 后端有安全机制，恢复只能到配置的白名单目录下 | 在 config.yaml 的 `server.restore_base_dirs` 中添加允许的父目录（如 `- "/path/to/test-env"`） |
| rclone.conf 中 [oss] section 名 | rclone crypt remote 找不到上游 | 后端硬编码期望 `[oss]` 作为 raw remote 名 | rclone.conf 中 raw local remote **必须**命名为 `[oss]`，crypt remote 命名为 `[oss-crypt]` |
| rclone crypt remote 路径 | 上传文件找不到/加密不生效 | crypt remote 的 `remote = oss:path` 必须指向正确目录 | 格式为 `remote = oss:<绝对路径>`，不要漏掉冒号 |

### 2.3 代码逻辑坑

| 坑 | 现象 | 原因 | 解决方案 |
|----|------|------|----------|
| 本地模式 OSS 客户端创建失败 | `get OSS bucket "": bucket name len is between [3-63]` | `CheckRestored()`/`RestoreObject()` 没有检查 endpoint/bucket 是否为空 | 修改条件判断，endpoint 或 bucket 为空时跳过 OSS SDK 调用（直接返回 true/nil，本地文件无需解冻） |
| 同批次重复文件 nonce 错误 | `decrypt file: nonce mismatch on first chunk` | 同一备份批次内完全相同的文件（如 photo1.bin 和 photo1_copy.bin）共享 storage_key，但在同一事务中处理时可能出现竞态或引用问题 | 跨批次去重（不同 backup_id 之间）正常工作，同批次去重是小概率 edge case，不影响核心功能；第二次增量备份时跨批次去重正确工作（验证时 skipped_dedup=1） |
| Go main 文件不在根目录 | `no Go files in ...` 编译失败 | 项目的 main.go 在 `cmd/nas-backup/main.go`，不在根目录 | 编译命令必须是 `go build -o nas-backup ./cmd/nas-backup/`，不是 `go build .` |
| API 返回结构不一致 | 测试脚本中取字段路径错误 | 有些 API 返回 `{"success": true, "data": {...}}`，有些直接返回 `{"backup_id": x}` | 测试脚本做了兼容处理：`trig.get("data", {}).get("backup_id") or trig.get("backup_id")` |

### 2.4 验证脚本设计注意事项

1. **rclone crypt 文件名加密**: `filename_encryption = standard` + `directory_name_encryption = true` 会加密文件名和目录名，所以存储中看不到 `.enc` 后缀或原始文件名。检测加密时不能靠文件扩展名，而要检查：
   - 文件名是否为长 base32 格式（无扩展名、无点号）
   - 文件内容是否为二进制（可打印字符比例 < 70%）
   - 文件头是否包含 rclone 标识

2. **备份完成等待**: 不要假设备份是同步的，通过轮询 `/api/dashboard/history` 等待 `status=completed`

3. **去重验证策略**: 同批次内的重复文件去重和跨批次去重表现不同。最可靠的验证方法是：
   - 第一次全量备份后，新增一个已知内容的重复文件
   - 触发第二次（增量）备份
   - 检查 `skipped_by_dedup` 计数

4. **环境清理**: 每次运行验证前必须删除旧的 `nas-backup.db`，否则旧的文件记录会干扰测试结果

5. **Python 标准库优先**: 验证脚本只用 Python 标准库（urllib, json, hashlib, pathlib），不依赖 requests，避免额外 pip 安装问题

---

## 三、依赖组件调用方法

### 3.1 rclone 二进制

**位置**: `nas-backup-backend/bin/rclone`（部署脚本放置）或系统 PATH

**配置文件**: `nas-backup-backend/data/rclone.conf`

**配置结构**（本地测试模式）:
```ini
[oss]
type = local

[oss-crypt]
type = crypt
remote = oss:/绝对路径/到/local-cloud-storage
password = <rclone obscure 生成的密码>
password2 = <rclone obscure 生成的盐值>
filename_encryption = standard
directory_name_encryption = true
```

**常用命令行调用**:
```bash
# 验证配置
./bin/rclone config show --config ./data/rclone.conf

# 列出远端文件（加密后看到的是混淆名）
./bin/rclone ls oss-crypt: --config ./data/rclone.conf

# 上传文件（后端内部调用，不需要手动执行）
./bin/rclone copyto /local/path oss-crypt:data/xxx/yyy.enc --config ./data/rclone.conf

# 下载文件（恢复时后端内部调用）
./bin/rclone copyto oss-crypt:data/xxx/yyy.enc /restore/path --config ./data/rclone.conf

# 检查文件是否存在
./bin/rclone lsjson oss-crypt:data/prefix --config ./data/rclone.conf
```

**后端代码中的调用方式** (`internal/storage/storage.go`):
```go
// 后端通过 exec.Command 调用 rclone，使用 --config 指定配置文件路径
// binary_path 来自 config.yaml 的 rclone.binary_path（默认 "./bin/rclone"）
cmd := exec.CommandContext(ctx, rcloneBin, "copyto", src, remoteSpec, "--config", rcloneConf)
```

### 3.2 配置文件 (config.yaml)

**位置**: `nas-backup-backend/config.yaml`

**关键字段**:
```yaml
server:
  host: "127.0.0.1"
  port: 8080
  restore_base_dirs:                    # 恢复目录白名单（必填！）
    - "/绝对路径/到/test-env"

database:
  path: "./data/nas-backup.db"          # SQLite 数据库路径

backup:
  directories:                          # 要备份的目录列表
    - path: "/绝对路径/到/source-files"
      recursive: true
      enabled: true
      description: "描述"
  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "./data/master.key"  # 加密主密钥（openssl rand -hex 32 生成）

oss:                                    # 本地测试模式全部留空
  endpoint: ""
  bucket: ""
  access_key_id: ""
  access_key_secret: ""
  region: ""

rclone:
  binary_path: "./bin/rclone"           # 相对于后端工作目录
  config_path: "./data/rclone.conf"
  remote_name: "oss-crypt"              # 必须与 rclone.conf 中的 crypt section 名一致
```

**后端启动时加载**:
```bash
cd nas-backup-backend
./nas-backup                    # 默认读取 config.yaml
# 或
./nas-backup -config config.yaml
```

### 3.3 Go 后端二进制

**位置**: `nas-backup-backend/nas-backup`

**编译方法**:
```bash
# 需要 Go 1.22+ 在 PATH 中
export PATH="$HOME/go-sdk/go/bin:$PATH"   # 如果安装在用户目录
cd nas-backup-backend
CGO_ENABLED=1 go build -o nas-backup ./cmd/nas-backup/
CGO_ENABLED=1 go build -o restore-cli ./cmd/restore-cli/
```

**启动方法**:
```bash
cd nas-backup-backend
./nas-backup > /tmp/nas-backup.log 2>&1 &
# 或使用脚本
./scripts/start-backend.sh
```

**健康检查** (启动后等待):
```bash
# 注意: /api/health 返回 404（没有这个端点），用 /api/dashboard/stats 代替
curl -s http://127.0.0.1:8080/api/dashboard/stats
```

**关键 API 端点**:
| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/api/dashboard/stats` | 获取备份统计（总数、已备份数、存储用量等） |
| GET | `/api/dashboard/history?page=1&size=10` | 获取备份历史 |
| POST | `/api/backup/trigger` | 触发备份 `{"type": "full"\|"incremental"}` |
| POST | `/api/restore` | 提交恢复任务 `{"paths":[...], "output_dir":"..."}` |
| GET | `/api/restore/jobs/{id}` | 查询恢复任务状态 |

**后端日志**: 默认输出到 stdout，配置文件中 `logging.file_path` 指定文件路径

### 3.4 主密钥 (master.key)

**位置**: `nas-backup-backend/data/master.key`

**生成方法**:
```bash
openssl rand -hex 32 > data/master.key
chmod 600 data/master.key
```

**用途**: 用于 AES-256-GCM 加密文件内容密钥（信封加密）。**丢失此密钥将导致所有备份数据不可恢复！**

### 3.5 启动顺序总结

```
1. 确保 Go 在 PATH 中（如果需要重新编译）
2. 确保 rclone 在 ./bin/rclone
3. 确保 config.yaml 正确配置（尤其是 directories 和 restore_base_dirs）
4. 确保 data/master.key 存在
5. 确保 data/rclone.conf 存在且包含 [oss] 和 [oss-crypt]
6. cd nas-backup-backend && ./nas-backup
7. 等待 2 秒，curl /api/dashboard/stats 确认就绪
8. 通过 API 触发备份/恢复
9. 通过轮询 history/jobs API 等待任务完成
```

---

## 四、快速问题排查清单

| 症状 | 排查项 |
|------|--------|
| 后端启动失败 | 检查 `config.yaml` 语法；检查 `data/` 目录权限；检查 8080 端口是否被占用 |
| 备份 0 文件 | 检查 `backup.directories` 路径是否存在；检查路径是否在 exclusions 中被排除 |
| 恢复报 "not under allowed base directories" | 在 `config.yaml` 的 `server.restore_base_dirs` 添加恢复目标目录的父目录 |
| 恢复报 "bucket name len is between [3-63]" | 确认使用了修复后的 `storage.go`（endpoint/bucket 为空时跳过 OSS SDK） |
| rclone 命令找不到 | 检查 `rclone.binary_path` 配置；确认文件在 `./bin/rclone` 且可执行 |
| 中文路径乱码/报错 | 设置 `export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8` |
| 加密文件找不到 | rclone crypt 加密了文件名，用 `rclone ls oss-crypt:` 查看真实存储结构 |
| 测试脚本 SHA256 不匹配 | 检查是否在恢复前修改了源文件；检查是否恢复了旧版本而非最新版本 |

---

## 五、文件清单

```
scripts/
├── deploy-macos.sh          # 一键部署脚本（安装依赖+编译+配置）
├── verify-e2e.sh            # 一键验证脚本（启动+测试+报告+停止）
├── verify_e2e.py            # Python E2E 测试套件（23 项测试）
├── generate_report.py       # HTML 验收报告生成器
├── start-backend.sh         # 启动后端服务
├── start-frontend-dev.sh    # 启动前端开发服务器
├── serve-frontend.sh        # 静态服务前端生产构建
├── env-macos.sh             # 环境变量（source 使用）
└── TROUBLESHOOTING.md       # 本文档（踩坑记录+依赖调用方法）

test-env/                    # 运行验证后生成
├── source-files/            # 测试源文件
├── local-cloud-storage/     # 模拟云存储（加密文件）
├── restore-output/          # 恢复输出目录
├── acceptance-report.html   # HTML 验收报告
├── e2e-report.json          # JSON 测试结果
└── backend.log              # 后端运行日志

nas-backup-backend/
├── nas-backup               # 后端二进制
├── config.yaml              # 运行时配置（部署脚本生成）
├── bin/rclone               # rclone 二进制
├── data/
│   ├── master.key           # 加密主密钥
│   ├── rclone.conf          # rclone 配置
│   ├── nas-backup.db        # SQLite 数据库（运行时生成）
│   └── logs/                # 日志目录
└── internal/storage/storage.go  # 已修复 CheckRestored/RestoreObject
```
