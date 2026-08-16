# NAS Backup 脚本说明文档

本文档介绍项目中所有辅助脚本的用途、用法和注意事项。

---

## 目录

- [Docker 相关脚本](#docker-相关脚本)
- [后端脚本](#后端脚本)
- [部署与启停脚本](#部署与启停脚本)
- [验证脚本](#验证脚本)
- [测试工具脚本](#测试工具脚本)

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

也支持非交互式参数调用（被 `scripts/deploy.sh` 调用）：

```bash
./scripts/setup-rclone.sh \
    --endpoint oss-cn-hangzhou.aliyuncs.com \
    --bucket my-bucket \
    --ak LTAIxxx \
    --sk xxx \
    --config-path ./data/rclone.conf
```

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

---

## 部署与启停脚本

以下脚本位于项目根目录的 `scripts/` 文件夹，提供跨平台（macOS + Debian）的统一部署和启停能力。

### `scripts/deploy.sh`

统一部署脚本，自动检测操作系统（`uname -s`），按平台执行对应部署流程。

**位置**：`scripts/deploy.sh`

**平台行为**：
- **macOS**：Homebrew 安装依赖、本地文件系统模拟云存储、构建后端 + 前端、生成便捷启动脚本
- **Debian**：安全模式 apt（绝不 upgrade）、Go/Node/rclone 官方二进制下载、systemd + Nginx、编译后端 + 构建前端

**使用方法**：

```bash
# 自动检测平台部署
./scripts/deploy.sh

# 强制指定平台
./scripts/deploy.sh --platform macos
./scripts/deploy.sh --platform debian

# Debian 生产部署（需 root）
sudo ./scripts/deploy.sh

# 常用选项
./scripts/deploy.sh --skip-deps           # 跳过系统依赖安装
./scripts/deploy.sh --skip-frontend       # 跳过前端构建（Debian）
./scripts/deploy.sh --no-nginx            # 跳过 Nginx 配置（Debian）
./scripts/deploy.sh --with-oss            # 配置真实 OSS（macOS，交互式）
./scripts/deploy.sh --install-dir /opt/nas-backup  # 指定安装目录（Debian）
```

**架构说明**：deploy.sh 通过 `source` 引入平台模块：
- `scripts/lib/common.sh` — 公共函数库（颜色/路径/端口/HTTP）
- `scripts/lib/deploy-macos.sh` — macOS 部署模块
- `scripts/lib/deploy-debian.sh` — Debian 部署模块

---

### `scripts/start.sh`

统一启停脚本，自动检测操作系统，按平台管理服务。

**位置**：`scripts/start.sh`

**平台行为**：
- **macOS**：nohup + PID 文件管理后端/前端进程
- **Debian**：systemctl 管理 nas-backup 服务 + Nginx reload（UGREEN NAS 兼容）

**使用方法**：

```bash
# 启动（默认）
./scripts/start.sh start

# 停止（Debian 模式默认保留 Nginx）
./scripts/start.sh stop

# 停止后端 + Nginx（Debian，慎用）
sudo ./scripts/start.sh stop-all

# 重启
./scripts/start.sh restart

# 查看状态
./scripts/start.sh status

# API 健康检查（Debian）
./scripts/start.sh health

# 实时日志
./scripts/start.sh logs           # 默认 all
./scripts/start.sh logs be        # 后端 journal
./scripts/start.sh logs app       # 应用日志文件
./scripts/start.sh logs fe        # 前端/Nginx 日志
```

---

### `scripts/update-debian.sh`

Debian 代码更新脚本，一键完成 git pull + 构建 + 重启 + 健康检查。

**位置**：`scripts/update-debian.sh`

**使用方法**：

```bash
sudo ./scripts/update-debian.sh

# 常用选项
sudo ./scripts/update-debian.sh --no-pull       # 跳过 git pull
sudo ./scripts/update-debian.sh --no-frontend   # 跳过前端构建
sudo ./scripts/update-debian.sh --no-backend    # 跳过后端构建
sudo ./scripts/update-debian.sh --skip-tests    # 跳过 go vet

# 回滚到上一版本
sudo ./scripts/update-debian.sh rollback
```

---

### `scripts/upgrade-rclone-debian.sh`

rclone 版本升级脚本（Debian）。

**位置**：`scripts/upgrade-rclone-debian.sh`

---

## 验证脚本

### `scripts/verify-e2e.sh`

端到端验证 Shell 脚本入口，编排完整的 E2E 测试流程。

**位置**：`scripts/verify-e2e.sh`

**功能**：
- 自动清理旧环境（旧数据库、旧云存储文件）
- 启动后端进程并等待就绪（重试 30 次）
- 运行 Python 测试套件（`verify_e2e.py`）
- 生成 HTML 验收报告
- 停止后端进程

**使用方法**：

```bash
chmod +x scripts/verify-e2e.sh
./scripts/verify-e2e.sh              # 完整流程（含构建）
./scripts/verify-e2e.sh --skip-build # 跳过构建（后端/前端已编译好）
```

---

### `scripts/verify_e2e.py`

Python 端到端验证套件，23 项测试覆盖完整备份恢复闭环。

**位置**：`scripts/verify_e2e.py`

**测试覆盖**：
- Phase 1: 环境检查（后端可达、rclone 可用、数据库存在、配置存在）
- Phase 2: 测试数据生成（文本文件、空文件、二进制随机文件、重复文件、深层嵌套文件）
- Phase 3: 全量备份测试（触发备份、等待完成、验证文件数/压缩率/Dashboard统计）
- Phase 4: 增量备份测试（修改文件、新增文件、触发增量、验证只处理变更文件）
- Phase 5: 恢复测试 + SHA256 完整性校验（逐文件哈希比对）
- Phase 6: 功能特性验证（加密验证、去重验证、空文件、API 正确性）

**依赖**：Python 3.7+（仅标准库）

**使用方法**：

```bash
# 确保后端服务运行中
python3 scripts/verify_e2e.py
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

**使用前提**：已配置真实 OSS 凭证（`config.yaml` 的 `oss.*` 段）

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

## 测试工具脚本

### `scripts/nas_file_generator.py`

测试文件生成器，模拟 NAS 文件结构，用于备份功能测试。

**位置**：`scripts/nas_file_generator.py`

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
python3 scripts/nas_file_generator.py --output /mnt/data/test-files --count 500

# 完整参数
python3 scripts/nas_file_generator.py \
  --output /mnt/data/test-files \
  --count 1000 \
  --min-size 1KB \
  --max-size 100MB \
  --max-depth 5 \
  --seed 42

# 不生成媒体文件（加快速度）
python3 scripts/nas_file_generator.py --output /tmp/test --count 100 --no-media
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

---

## 脚本使用最佳实践

### 首次部署配置流程

```bash
# 1. 一键部署（自动安装依赖 + 编译 + 配置）
./scripts/deploy.sh              # macOS
sudo ./scripts/deploy.sh         # Debian 生产

# 2. 启动服务
./scripts/start.sh start         # macOS
sudo ./scripts/start.sh start    # Debian

# 3. 生成测试文件验证备份
python3 scripts/nas_file_generator.py --output /mnt/nas/test --count 50

# 4. 触发备份测试
curl -X POST http://127.0.0.1:8080/api/backup/trigger

# 5. 运行完整 E2E 测试
./scripts/verify-e2e.sh --skip-build
```

### 日常维护

```bash
# 查看服务状态
./scripts/start.sh status

# 健康检查（Debian）
./scripts/start.sh health

# 查看日志
./scripts/start.sh logs app

# 代码更新（Debian）
sudo ./scripts/update-debian.sh

# 手动备份数据库
sqlite3 nas-backup-backend/data/nas-backup.db ".backup './db-backups/nas-backup_$(date +%Y%m%d).db'"
```

---

## 注意事项

1. **权限问题**：所有 `.sh` 脚本使用前需要添加可执行权限：`chmod +x script.sh`
2. **Debian 需 root**：`deploy.sh` / `start.sh` / `update-debian.sh` 在 Debian 模式下操作 systemd/nginx 需要 root
3. **路径问题**：执行脚本时注意工作目录，部分脚本假设在项目根目录运行
4. **密钥安全**：`master.key` 和 `rclone.conf` 请妥善保存，丢失无法恢复数据
5. **测试数据**：`nas_file_generator.py` 生成的文件仅用于测试，不要与真实数据混放
6. **Python 依赖**：Python 脚本在 Debian/Ubuntu 系统安装依赖时使用 `--break-system-packages` 标志
