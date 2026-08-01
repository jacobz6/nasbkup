# NAS Backup 脚本说明文档

本文档介绍项目中所有辅助脚本的用途、用法和注意事项。

---

## 目录

- [Docker 相关脚本](#docker-相关脚本)
- [后端脚本](#后端脚本)
- [部署与验证脚本](#部署与验证脚本)
- [测试工具脚本](#测试工具脚本)
- [项目根目录脚本](#项目根目录脚本)

---

## Docker 相关脚本

### `docker/entrypoint.sh`

容器启动入口脚本，负责容器初始化和服务启动。

**位置**：`docker/entrypoint.sh`

**职责**：
1. 创建数据目录和日志目录
2. 首次启动自动生成 `master.key`（32 字节随机密钥，base64 编码）
3. 首次启动自动生成 `rclone.conf` 模板（含占位符）
4. 校验后端二进制和配置文件存在
5. 启动后端服务（后台运行）
6. 等待后端就绪（最多 30 秒）
7. 启动 Nginx（前台运行，阻塞主进程）
8. 捕获 SIGTERM/SIGINT 信号，优雅关闭所有进程

**环境变量**：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `DATA_DIR` | `/app/data` | 数据目录 |
| `KEY_FILE` | `${DATA_DIR}/master.key` | 加密密钥路径 |
| `RCLONE_CONF` | `${DATA_DIR}/rclone.conf` | rclone 配置路径 |
| `CONFIG_FILE` | `/app/config.yaml` | 应用配置路径 |

**优雅关闭流程**：
1. 收到终止信号
2. 向 Nginx 发送 TERM 信号
3. 向后端发送 TERM 信号，等待最多 30 秒
4. 超时后强制 KILL 后端进程
5. 退出

---

## 后端脚本

### `nas-backup-backend/scripts/setup-rclone.sh`

交互式 rclone 配置脚本，自动配置 OSS 和 crypt 远程存储。

**位置**：`nas-backup-backend/scripts/setup-rclone.sh`

**功能**：
- 引导用户输入 OSS Endpoint、Bucket、AccessKey ID/Secret
- 自动生成 rclone crypt 加密密码
- 自动生成完整的 `rclone.conf` 配置文件
- 设置正确的文件权限（600）

**使用方法**：

```bash
cd nas-backup-backend
chmod +x scripts/setup-rclone.sh
./scripts/setup-rclone.sh
```

**交互流程**：
1. 输入 OSS Endpoint（如 `oss-cn-hangzhou.aliyuncs.com`）
2. 输入 Bucket 名称
3. 输入 AccessKey ID
4. 输入 AccessKey Secret
5. 确认配置，自动生成文件

---

### `nas-backup-backend/scripts/patch-rclone-crypt-password.sh`

一键修复 rclone crypt 远端缺失密码的问题。

**位置**：`nas-backup-backend/scripts/patch-rclone-crypt-password.sh`

**使用场景**：当 rclone.conf 中 `[oss-crypt]` 段的 `password` 或 `password2` 字段缺失时，使用此脚本自动补全。

**使用方法**：

```bash
cd nas-backup-backend
chmod +x scripts/patch-rclone-crypt-password.sh
./scripts/patch-rclone-crypt-password.sh
```

**脚本会**：
1. 检查 rclone.conf 是否存在
2. 检查是否有 `[oss-crypt]` 段
3. 如果 password 缺失，自动生成加密密码
4. 备份原配置文件为 `rclone.conf.bak`
5. 写入修复后的配置

---

### `nas-backup-backend/scripts/backup.sh`

CLI 手动触发备份脚本，优先通过 API 触发，服务未运行时尝试直连二进制。

**位置**：`nas-backup-backend/scripts/backup.sh`

**使用方法**：

```bash
cd nas-backup-backend
chmod +x scripts/backup.sh

# 默认触发 auto 模式备份
./scripts/backup.sh

# 指定备份类型
./scripts/backup.sh full        # 全量备份
./scripts/backup.sh incremental # 增量备份
./scripts/backup.sh auto        # 智能模式（自动判断）

# 指定配置文件
./scripts/backup.sh auto -c /path/to/config.yaml
```

**工作逻辑**：
1. 检查后端 HTTP 服务是否运行（`/api/dashboard/stats`）
2. 如果服务运行，通过 API 触发备份
3. 如果服务未运行，尝试直接调用后端二进制执行备份
4. 输出备份状态

---

### `nas-backup-backend/run_tests.sh`

一键测试脚本，执行完整的测试流程。

**位置**：`nas-backup-backend/run_tests.sh`

**使用方法**：

```bash
cd nas-backup-backend
chmod +x run_tests.sh
./run_tests.sh
```

**执行流程**：
1. **go vet**：静态代码检查
2. **单元测试**：运行所有包的单元测试（9 个包）
3. **API 集成测试**：运行 API 层集成测试
4. **覆盖率统计**：生成测试覆盖率报告
5. **E2E 连通性检查**：验证服务可正常启动（可选）

**环境变量**：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SKIP_E2E` | `0` | 设为 `1` 跳过 E2E 测试 |
| `VERBOSE` | `0` | 设为 `1` 输出详细测试日志 |

---

### `nas-backup-backend/scripts/backup_restore_test.py`

Python 备份恢复闭环测试脚本，验证完整备份→恢复流程。

**位置**：`nas-backup-backend/scripts/backup_restore_test.py`

**功能**：
- 创建测试文件集
- 触发备份并等待完成
- 验证备份数据完整性
- 执行恢复操作
- 校验恢复后的文件与原始文件一致
- 清理测试数据

**依赖**：
- Python 3.7+
- requests 库（`pip install requests`）

**使用方法**：

```bash
cd nas-backup-backend
pip install requests --break-system-packages

# 确保后端服务运行中
python3 scripts/backup_restore_test.py

# 指定 API 地址
python3 scripts/backup_restore_test.py --api-url http://127.0.0.1:8080
```

---

## 部署与验证脚本

以下脚本位于项目根目录的 `scripts/` 文件夹，用于 macOS 部署和端到端验证。

### `scripts/deploy-macos.sh`

macOS 一键部署脚本，自动安装依赖、配置环境并启动服务。

**位置**：`scripts/deploy-macos.sh`

**功能**：
- 检测并安装 Homebrew、Go、Node.js、rclone、zstd 等依赖
- 自动下载并配置 rclone binary
- 创建数据目录和日志目录
- 生成 `master.key` 加密密钥
- 启动后端服务（后台运行）
- 运行健康检查验证服务可用性

**使用方法**：

```bash
chmod +x scripts/deploy-macos.sh
./scripts/deploy-macos.sh
```

---

### `scripts/verify-e2e.sh`

端到端验证 Shell 脚本，快速验证备份与恢复核心功能。

**位置**：`scripts/verify-e2e.sh`

**功能**：
- 启动后端服务（如未运行）
- 生成测试文件
- 触发备份并等待完成
- 执行恢复操作
- 校验恢复文件完整性
- 输出验证结果

**使用方法**：

```bash
chmod +x scripts/verify-e2e.sh
./scripts/verify-e2e.sh
```

---

### `scripts/verify_e2e.py`

Python 端到端验证脚本，提供更详细的测试覆盖和报告生成。

**位置**：`scripts/verify_e2e.py`

**功能**：
- 23 项端到端测试用例（API、备份、恢复、对账等）
- 生成 JSON 格式测试报告（`e2e-report.json`）
- 生成 HTML 格式验收报告
- 支持自定义 API 地址和测试参数

**依赖**：Python 3.7+

**使用方法**：

```bash
# 确保后端服务运行中
python3 scripts/verify_e2e.py

# 指定 API 地址
python3 scripts/verify_e2e.py --api-url http://127.0.0.1:8080
```

---

### `scripts/verify_cloud_archive.py`

云端归档验证脚本，验证真实 OSS 冷归档存储的完整流程。

**位置**：`scripts/verify_cloud_archive.py`

**功能**：
- 验证文件上传到 OSS ColdArchive
- 测试标准/加急解冻流程
- 验证解冻后数据完整性
- 生成云端归档验收报告（HTML + JSON）

**使用前提**：已配置真实 OSS 凭证（`config.yaml`）

**使用方法**：

```bash
python3 scripts/verify_cloud_archive.py --config nas-backup-backend/config.yaml
```

---

### `scripts/generate_report.py`

验收报告生成器，将测试结果汇总为结构化 HTML 报告。

**位置**：`scripts/generate_report.py`

**使用方法**：

```bash
python3 scripts/generate_report.py --input e2e-report.json --output acceptance-report.html
```

---

### `scripts/TROUBLESHOOTING.md`

故障排查指南，汇总常见问题和解决方案。

**位置**：`scripts/TROUBLESHOOTING.md`

---

## 测试工具脚本

### `nas_file_generator.py`

测试文件生成器，模拟 NAS 文件结构，用于备份功能测试。

**位置**：项目根目录 `nas_file_generator.py`

**功能**：
- 生成多种类型的测试文件（文档、图片、视频、音频、代码、压缩包等）
- 支持控制文件数量、大小范围、目录深度
- 生成的文件包含真实可识别的内容（非全零填充）
- 可用于性能测试和功能验证

**依赖**：
- Python 3.7+
- Pillow（可选，用于生成真实图片）

**使用方法**：

```bash
# 基本用法：生成 500 个测试文件到指定目录
python3 nas_file_generator.py --output /mnt/data/test-files --count 500

# 完整参数
python3 nas_file_generator.py \
  --output /mnt/data/test-files \
  --count 1000 \
  --min-size 1KB \
  --max-size 100MB \
  --max-depth 5 \
  --seed 42

# 不生成媒体文件（加快速度）
python3 nas_file_generator.py --output /tmp/test --count 100 --no-media
```

**参数说明**：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--output` | 必填 | 输出目录 |
| `--count` | `100` | 生成文件数量 |
| `--min-size` | `1KB` | 最小文件大小 |
| `--max-size` | `10MB` | 最大文件大小 |
| `--max-depth` | `4` | 最大目录深度 |
| `--seed` | 随机 | 随机种子（用于可复现的测试） |
| `--no-media` | `false` | 跳过图片/视频/音频生成 |

**生成的文件类型**：

| 类型 | 扩展名 | 说明 |
|------|--------|------|
| 文本文档 | `.txt`, `.md`, `.doc`, `.pdf` | 包含随机文本内容 |
| 图片 | `.jpg`, `.png`, `.gif`, `.webp` | 使用 Pillow 生成真实图片（如有） |
| 视频 | `.mp4`, `.mkv`, `.avi` | 占位文件（小尺寸） |
| 音频 | `.mp3`, `.flac`, `.wav` | 占位文件 |
| 代码 | `.py`, `.go`, `.js`, `.ts`, `.html`, `.css` | 包含示例代码片段 |
| 压缩包 | `.zip`, `.tar.gz`, `.7z` | 内含小文件 |
| 数据文件 | `.json`, `.csv`, `.xml`, `.sql` | 结构化数据 |

---

## 脚本使用最佳实践

### 首次部署配置流程

```bash
# 1. 生成加密密钥
openssl rand -base64 32 > ./data/master.key
chmod 600 ./data/master.key

# 2. 交互式配置 rclone
./nas-backup-backend/scripts/setup-rclone.sh

# 3. 启动服务
docker compose up -d --build

# 4. 生成测试文件验证备份
python3 nas_file_generator.py --output /mnt/nas/test --count 50

# 5. 触发备份测试
curl -X POST http://127.0.0.1:8080/api/backup/trigger

# 6. 运行完整测试
cd nas-backup-backend && ./run_tests.sh
```

### 日常维护脚本

```bash
# 手动备份数据库（Docker 环境）
#!/bin/bash
BACKUP_DIR=./db-backups
DATE=$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP_DIR"
cp ./data/nas-backup.db "$BACKUP_DIR/nas-backup_$DATE.db"
ls -t "$BACKUP_DIR"/nas-backup_*.db | tail -n +31 | xargs -r rm
```

```bash
# 检查服务健康状态
#!/bin/bash
if curl -fsS http://127.0.0.1:8080/api/dashboard/stats > /dev/null; then
    echo "✅ NAS Backup 服务正常"
else
    echo "❌ NAS Backup 服务异常"
    docker compose restart
fi
```

---

## 注意事项

1. **权限问题**：所有 `.sh` 脚本使用前需要添加可执行权限：`chmod +x script.sh`
2. **路径问题**：执行脚本时注意工作目录，部分脚本假设在特定目录下运行
3. **密钥安全**：`setup-rclone.sh` 生成的密码请妥善保存，丢失无法恢复数据
4. **测试数据**：`nas_file_generator.py` 生成的文件仅用于测试，不要与真实数据混放
5. **Python 依赖**：Python 脚本需要使用 `--break-system-packages` 标志安装依赖（Debian/Ubuntu 系统）
