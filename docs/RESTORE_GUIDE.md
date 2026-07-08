# NAS Backup 恢复操作指南

## 概述

本文档面向系统管理员，提供 NAS 云端备份系统的数据恢复操作全流程指引。涵盖从日常单文件/目录恢复到整台 NAS 灾难恢复的各类场景。

无论你是通过 Web UI 进行日常恢复操作，还是需要在全新环境中从零重建并拉取全部云端数据，本文档都将提供逐步指引。

**适用读者**：NAS 系统管理员、运维人员。

**前置假设**：读者具备基本的 Linux 命令行操作能力，了解本系统的基本部署架构（参见 `DEPLOYMENT.md`）。

---

## 必备文件清单（非常重要）

恢复 NAS 备份数据需要以下 **三个关键文件**，缺一不可。这些文件统称为**必备三件套**，必须与云端数据分开、异地安全保存。

### 1. `master.key` -- AES-256 主密钥文件

- **用途**：AES-256-GCM 加密算法的主密钥。系统在上传文件到 OSS 前使用此密钥对数据进行加密，恢复时必须使用同一密钥进行解密。
- **丢失后果**：**所有云端加密数据永远无法恢复**。OSS 中的数据全部为加密态，没有此密钥即无法解密。
- **默认位置**：`./data/master.key`
- **生成方式**：`openssl rand -base64 32 > ./data/master.key`

### 2. `rclone.conf` -- rclone 配置文件

- **用途**：包含 OSS 访问凭证（AccessKey ID/Secret）以及 rclone crypt 远程存储的加密密码（password/password2）。系统通过此配置与阿里云 OSS 通信。
- **丢失后果**：无法连接 OSS 云存储，也无法正确解密 rclone crypt 层的数据文件名和内容。
- **默认位置**：`./data/rclone.conf`
- **说明**：系统可从 `config.yaml` 中的 OSS 配置自动生成此文件，但建议保留已生成好的副本以避免版本差异。

### 3. `nas-backup.db` -- SQLite 数据库（可选，已自动同步到 OSS）

- **用途**：记录所有文件的元数据（路径、大小、修改时间、SHA-256 哈希值）、哈希索引（去重映射）、备份会话历史、备份-文件关联关系（storage_key、加密 IV、压缩类型等），以及恢复作业记录。
- **丢失后果**：系统无法定位云端对象的存储路径、加密参数和版本信息，实质上无法执行有意义的恢复操作。
- **默认位置**：`./data/nas-backup.db`
- **自动备份**：**每次成功备份后，系统会自动将加密后的数据库上传到 OSS 的 `meta/db/` 目录**（保留最近 3 个版本）。因此，即使本地数据库丢失，也可以通过 `restore-cli bootstrap` 命令从 OSS 恢复。**这意味着在灾难恢复时，你只需要 `master.key` 和 `rclone.conf` 两个文件。**

### 推荐的安全备份方式

由于数据库已自动同步到 OSS，**只需要安全保存以下两件套**即可完成完整灾难恢复：

| 文件 | 说明 | 丢失后果 |
|------|------|----------|
| **`master.key`** | AES-256 主密钥 | 所有云端加密数据永远无法恢复 |
| **`rclone.conf`** | OSS 凭证 + crypt 密码 | 无法连接 OSS 或解密 rclone crypt 层 |

建议采用**至少两种**以下方式进行异地备份：

| 方式 | 说明 | 建议频率 |
|------|------|----------|
| **加密 U 盘** | 将两件套拷贝到加密 U 盘，存放在异地（如办公室保险柜） | 每次配置变更后 |
| **另一台设备/服务器** | 通过 scp/rsync 同步到另一台可信设备 | 每日自动同步 |
| **云存储** | 加密打包后上传到另一个云存储服务（非同一 OSS bucket） | 每周自动备份 |
| **打印纸质副本** | 将密钥和关键凭证打印后封存（仅限密钥等短内容） | 配置初始化时 |

> **严重警告**：`master.key` 和 `rclone.conf` 包含敏感凭证，备份时务必进行加密保护。切勿将明文密钥文件上传到未加密的云存储或发送到不安全的信道。

---

## 恢复方式概览

本系统提供两种恢复方式，可根据场景灵活选择：

### 1. Web UI 恢复（推荐日常使用）

- **入口**：通过浏览器访问系统前端页面（如 `http://<服务器IP>`），在 Web 界面中操作。
- **特点**：
  - 可视化操作，支持文件浏览和搜索
  - 异步执行恢复任务，不阻塞 UI
  - 通过 SSE（Server-Sent Events）实时推送恢复进度
  - 支持查看恢复作业历史记录
  - 支持取消正在运行的恢复任务
- **适用场景**：恢复少量文件、按目录恢复、日常运维操作。

### 2. CLI 命令行恢复（适合脚本化/高级用户）

- **工具**：`restore-cli` 独立命令行工具。
- **特点**：
  - 不依赖 HTTP 服务，直接操作数据库和 OSS
  - 支持验证（verify）和恢复（restore）两种模式
  - 支持 ColdArchive 解冻加速（`--expedited` 标志）
  - 可方便地集成到 Shell 脚本或 cron 任务中
- **适用场景**：大批量恢复、自动化脚本、无 Web UI 环境下的紧急恢复、服务器迁移后的数据校验。

---

## 场景 A：部分文件丢失/损坏恢复（日常最常用）

适用于个别文件误删、磁盘坏道导致少量文件损坏、或需要恢复某个历史版本的场景。

### 前置条件

1. NAS Backup 系统正常运行（后端服务和前端均可访问）。
2. `master.key`、`rclone.conf`、`nas-backup.db` 三个文件完好且位于 `./data/` 目录。
3. 已成功执行过至少一次备份，且目标文件包含在备份范围内。
4. 网络可正常访问阿里云 OSS。

### 操作步骤（Web UI）

1. **浏览可恢复文件**
   - 打开浏览器，访问系统前端页面。
   - 通过文件浏览器或恢复页面查看可恢复的文件列表。
   - 可通过搜索框过滤文件名，或按目录浏览。

2. **选择备份版本（可选）**
   - 系统默认使用最近一次完成的备份（latest completed）。
   - 如需恢复特定版本，可通过备份会话列表选择目标 `backup_id`。

3. **发起恢复任务**
   - 选择需要恢复的文件或目录。
   - 指定恢复目标目录（`output_dir`），该目录必须在配置允许的恢复基础目录下。
   - 选择冲突策略：
     - `skip`（默认）：如果目标位置已有同名文件，跳过该文件并记录为失败。
     - `overwrite`：直接覆盖已有文件。
     - `rename`：自动在文件名后追加时间戳后缀以避免冲突。
   - 对于 ColdArchive 存储的文件，可选择是否使用加速解冻（`expedited`）。
   - 点击"开始恢复"。

4. **监控恢复进度**
   - 恢复任务创建后异步执行，页面通过 SSE 实时展示进度：
     - 当前阶段（准备 / 解冻 / 下载 / 解密 / 解压 / 校验 / 移动）
     - 已恢复文件数 / 总文件数
     - 已恢复数据量 / 总数据量
     - 单文件级别的成功/失败状态
   - 可在恢复作业列表中查看历史任务详情。

5. **处理失败文件**
   - 恢复完成后检查失败文件列表。
   - 对于 ColdArchive 文件，若解冻超时（默认最大等待 30 分钟），可在解冻完成后再重新恢复。
   - 对于 OSS 404 错误，需检查 `hash_index` 和 `backup_files` 的一致性（可通过 Reconcile 功能排查）。

### 操作步骤（CLI）

```bash
# 1. 查看最近的备份会话
./restore-cli -config config.yaml backups

# 2. 列出可恢复的文件（默认使用最近一次完成的备份）
./restore-cli -config config.yaml list

# 3. 列出特定目录下的可恢复文件
./restore-cli -config config.yaml list /mnt/data/documents

# 4. 查看单个文件的详细信息（含存储密钥、压缩类型、哈希等）
./restore-cli -config config.yaml info /mnt/data/documents/report.pdf

# 5. 恢复单个文件到指定目录
./restore-cli -config config.yaml restore /mnt/data/documents/report.pdf -o /tmp/restored/

# 6. 恢复整个目录到指定位置
./restore-cli -config config.yaml restore-dir /mnt/data/documents -o /tmp/restored/

# 7. 恢复特定备份版本的文件（通过 --backup-id 指定）
./restore-cli -config config.yaml --backup-id 3 restore /mnt/data/documents/report.pdf -o /tmp/restored/

# 8. 使用加速解冻恢复 ColdArchive 文件
./restore-cli -config config.yaml --expedited restore /mnt/data/documents/archive.zip -o /tmp/restored/

# 9. 验证单个文件（下载 -> 解密 -> 解压 -> SHA-256 校验，不写入输出目录）
./restore-cli -config config.yaml verify /mnt/data/documents/report.pdf

# 10. 批量验证目录下的文件
./restore-cli -config config.yaml verify-dir /mnt/data/documents

# 11. 采样验证（仅验证前 10 个文件）
./restore-cli -config config.yaml verify-dir /mnt/data/documents --limit 10
```

### 注意事项

- **恢复路径规则**：
  - 恢复单个文件时，保留其直属父目录名。例如 `/mnt/data/docs/report.pdf` 恢复到 `/tmp/restored/docs/report.pdf`。
  - 恢复多个文件时，自动计算最长公共目录前缀并保留相对路径结构。
- **ColdArchive 解冻等待**：系统在解冻 OSS 归档对象时会轮询等待，最长等待 30 分钟。若超时可稍后重试。
- **并发恢复**：系统根据 `storage.concurrency` 配置（默认 8）并发处理多个文件，ColdArchive 解冻等待期间不会消耗 OSS 带宽。
- **备份互斥**：恢复任务执行期间无法同时执行备份任务，反之亦然。同一时间只能有一个恢复任务运行。
- **哈希校验**：每个文件恢复后都会进行 SHA-256 哈希校验，确保数据完整性。

---

## 场景 B：整台 NAS 丢失灾难恢复

适用于整台 NAS 设备损坏、丢失、被勒索软件攻击等极端情况，需要在新环境中从零恢复所有数据。

### 前置条件

1. 必备二件套（`master.key`、`rclone.conf`）已从异地安全备份中取回。
2. 阿里云 OSS 中的备份数据完好（可在 OSS 控制台确认 bucket 中有数据）。
3. 准备好新环境（一台 Linux 服务器，建议配置参见 `DEPLOYMENT.md`）。
4. 原始 NAS 至少完成过一次成功的备份（数据库已自动同步到 OSS）。

### 操作步骤

#### 1. 准备新环境

按照 `DEPLOYMENT.md` 的指引完成基础环境搭建：

```bash
# 安装系统依赖
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl wget git nginx build-essential sqlite3

# 安装 Go（用于编译后端）
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh

# 安装 rclone
sudo -v ; curl https://rclone.org/install.sh | sudo bash

# 安装 Node.js（用于构建前端）
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
```

#### 2. 安装 nas-backup 系统

```bash
# 创建部署目录
sudo mkdir -p /opt/nas-backup
sudo chown $USER:$USER /opt/nas-backup
cd /opt/nas-backup

# 将项目代码上传到此目录（从代码仓库获取或从备份中恢复）
# 目录结构应为：
# /opt/nas-backup/
#   nas-backup-backend/
#   nas-backup-frontend/
```

编译后端：

```bash
cd /opt/nas-backup/nas-backup-backend
go mod download
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o nas-backup cmd/nas-backup/main.go
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o restore-cli cmd/restore-cli/main.go
```

构建前端：

```bash
cd /opt/nas-backup/nas-backup-frontend
npm ci
npm run build
```

#### 3. 放置必备文件并恢复数据库

将异地备份的二件套放置到正确位置，然后从 OSS 恢复数据库：

```bash
# 创建数据目录
mkdir -p /opt/nas-backup/nas-backup-backend/data/logs

# 从备份介质复制二件套
cp /path/to/backup/master.key   /opt/nas-backup/nas-backup-backend/data/master.key
cp /path/to/backup/rclone.conf   /opt/nas-backup/nas-backup-backend/data/rclone.conf

# 设置正确的文件权限
chmod 600 /opt/nas-backup/nas-backup-backend/data/master.key
chmod 600 /opt/nas-backup/nas-backup-backend/data/rclone.conf
```

> **注意**：`master.key` 和 `rclone.conf` 包含敏感凭证，必须设置 600 权限。

从 OSS 恢复数据库（自动下载最新版本并解密）：

```bash
cd /opt/nas-backup/nas-backup-backend

# 使用 restore-cli 从 OSS 拉取加密数据库并解密到本地
./restore-cli -config config.yaml bootstrap

# 输出示例：
# Available database backup versions:
#   [1] nas-backup-20260709-103000.db
#   [2] nas-backup-20260708-103000.db
#   [3] nas-backup-20260707-103000.db
#
# Bootstrapping latest version: nas-backup-20260709-103000.db
# Database restored to: ./data/nas-backup.db
# You can now start the nas-backup service normally.
```

如果需要恢复特定历史版本，可以先列出所有版本：

```bash
# 列出 OSS 中的数据库备份版本（需先手动 bootstrap 任意版本）
# 或者使用 --o 指定输出路径，不覆盖默认路径
./restore-cli -config config.yaml bootstrap -o ./data/nas-backup.db
```

#### 4. 配置 config.yaml

编辑配置文件，确保路径与原始环境一致：

```bash
nano /opt/nas-backup/nas-backup-backend/config.yaml
```

关键配置项：

```yaml
server:
  host: "0.0.0.0"
  port: 8080

database:
  path: "/opt/nas-backup/nas-backup-backend/data/nas-backup.db"

backup:
  # 恢复阶段暂时不需要配置备份目录，但如需后续继续备份，需配置
  directories:
    - path: "/mnt/data/documents"
      recursive: true
      enabled: true
      description: "Documents"
  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "/opt/nas-backup/nas-backup-backend/data/master.key"

oss:
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  bucket: "your-bucket-name"
  access_key_id: "your-access-key-id"
  access_key_secret: "your-access-key-secret"
  region: "cn-hangzhou"

rclone:
  binary_path: "/usr/bin/rclone"
  config_path: "/opt/nas-backup/nas-backup-backend/data/rclone.conf"
  remote_name: "oss-crypt"
```

> **说明**：OSS 凭证信息可以从 `rclone.conf` 中获取（以混淆形式存储），也可以从你自己的安全记录中恢复。

#### 5. 启动系统

```bash
cd /opt/nas-backup/nas-backup-backend
./nas-backup -config config.yaml
```

或者配置 systemd 服务后启动（推荐生产环境）：

```bash
sudo nano /etc/systemd/system/nas-backup.service
```

```ini
[Unit]
Description=NAS Backup Service
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/nas-backup/nas-backup-backend
ExecStart=/opt/nas-backup/nas-backup-backend/nas-backup -config /opt/nas-backup/nas-backup-backend/config.yaml
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup
sudo systemctl start nas-backup
```

配置 Nginx（参见 `DEPLOYMENT.md` 第 4 节），然后通过浏览器访问验证系统正常运行。

#### 6. 使用 Web UI 或 CLI 恢复数据

**方式一：通过 Web UI**

1. 浏览器访问 `http://<服务器IP>`。
2. 在恢复页面浏览可恢复的文件列表。
3. 选择需要恢复的目录，指定恢复目标路径。
4. 发起恢复任务并等待完成。

**方式二：通过 CLI（适合大批量恢复）**

```bash
# 查看所有备份会话，确认数据版本
./restore-cli -config config.yaml backups

# 列出所有可恢复文件
./restore-cli -config config.yaml list

# 恢复整个备份目录
./restore-cli -config config.yaml restore-dir /mnt/data/documents -o /mnt/data/documents
./restore-cli -config config.yaml restore-dir /mnt/data/photos -o /mnt/data/photos

# 或逐目录恢复并限制并发数（适合 ColdArchive 场景）
./restore-cli -config config.yaml --expedited restore-dir /mnt/data/documents -o /mnt/data/documents
```

### 验证恢复完整性

恢复完成后，务必进行数据完整性验证：

```bash
# 方式一：CLI 验证（下载 -> 解密 -> 解压 -> SHA-256 校验）
./restore-cli -config config.yaml verify-dir /mnt/data/documents
./restore-cli -config config.yaml verify-dir /mnt/data/photos

# 方式二：采样验证（仅验证部分文件以节省时间）
./restore-cli -config config.yaml verify-dir /mnt/data/documents --limit 20

# 方式三：通过 Web UI 查看恢复作业结果
# 检查恢复作业详情中的成功/失败文件数和失败文件列表
```

验证要点：
- 每个恢复的文件都经过 SHA-256 哈希校验，哈希值必须与 `nas-backup.db` 中记录的一致。
- 对比恢复后的文件总大小与备份记录的总大小。
- 随机抽查若干文件，确认内容可正常打开。

---

## 场景 C：全新 NAS 全盘迁移

适用于旧 NAS 老化更换新设备，或从一台 NAS 迁移到另一台 NAS 的场景。

### 与场景 B 的区别

| 维度 | 场景 B（灾难恢复） | 场景 C（全盘迁移） |
|------|---------------------|---------------------|
| 触发原因 | NAS 损坏/丢失/被攻击 | 计划性设备更换 |
| 必备文件 | 从异地备份取回 `master.key` + `rclone.conf` | 从旧 NAS 直接拷贝 |
| OSS 数据 | 确认完好即可 | 同一 bucket，数据完好 |
| 原始数据 | 可能部分丢失 | 旧 NAS 仍可访问 |
| 数据库 | 通过 `restore-cli bootstrap` 从 OSS 恢复 | 使用旧 NAS 的 db 文件或 OSS 恢复 |
| 恢复范围 | 仅恢复备份中有的数据 | 可对比新旧数据完整性 |

### 操作步骤

1. **在旧 NAS 上停止备份服务**
   ```bash
   sudo systemctl stop nas-backup
   ```

2. **备份旧 NAS 的三件套**
   ```bash
   mkdir -p /tmp/nas-backup-essentials
   cp /opt/nas-backup/nas-backup-backend/data/master.key   /tmp/nas-backup-essentials/
   cp /opt/nas-backup/nas-backup-backend/data/rclone.conf   /tmp/nas-backup-essentials/
   cp /opt/nas-backup/nas-backup-backend/data/nas-backup.db /tmp/nas-backup-essentials/
   ```

3. **将三件套传输到新 NAS**
   ```bash
   scp /tmp/nas-backup-essentials/* user@new-nas-ip:/tmp/nas-backup-essentials/
   ```

4. **在新 NAS 上按场景 B 的步骤 1-5 安装和配置系统**，然后放置三件套到正确位置。

5. **恢复数据**
   ```bash
   # 恢复所有备份目录的数据
   ./restore-cli -config config.yaml restore-dir /mnt/data/documents -o /mnt/data/documents
   ./restore-cli -config config.yaml restore-dir /mnt/data/photos -o /mnt/data/photos
   ```

6. **启动新 NAS 的备份服务**，确认定时调度正常。

7. **验证旧 NAS 上未被备份的数据**：检查旧 NAS 上是否有新增但尚未备份的文件（可通过对比文件修改时间和最近一次备份时间来确认）。如有，需手动将这些文件复制到新 NAS。

### 验证数据完整性

```bash
# 1. 对比恢复后的文件数量与备份记录
./restore-cli -config config.yaml list /mnt/data/documents | wc -l

# 2. 采样验证文件哈希
./restore-cli -config config.yaml verify-dir /mnt/data/documents --limit 20
./restore-cli -config config.yaml verify-dir /mnt/data/photos --limit 20

# 3. 检查 OSS 存储健康状态（通过 Web UI 或 API）
curl http://127.0.0.1:8080/api/storage/health

# 4. 运行 Reconcile 检查 OSS/数据库一致性
curl -X POST http://127.0.0.1:8080/api/reconcile?dry_run=true
```

---

## CLI 命令参考

`restore-cli` 是独立命令行恢复工具，直接操作数据库和 OSS，无需 HTTP 服务。

### 基本用法

```bash
./restore-cli -config <config.yaml路径> <命令> [参数] [标志]
```

### 全局标志

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--config` | 配置文件路径 | `config.yaml` |
| `--backup-id N` | 指定备份会话 ID（0 = 最近一次完成的备份） | `0` |
| `--expedited` | 使用加速解冻 ColdArchive 对象 | `false` |
| `-o <目录>` | 恢复操作的输出目录 | （必需，用于 restore/restore-dir） |
| `--limit N` | 限制处理的文件数量（0 = 全部） | `0` |

### 命令列表

#### `backups` -- 列出备份会话

```bash
./restore-cli -config config.yaml backups
```

输出示例：
```
ID     TYPE         STATUS      FILES       SIZE         COMPLETED_AT
--------------------------------------------------------------------------------
1      full         completed   1234        15.6 GiB     2026-07-01 03:45:12
2      incremental  completed   56          234.5 MiB    2026-07-05 03:02:33
3      full         completed   1300        16.2 GiB     2026-07-08 03:30:00
```

#### `list [目录路径]` -- 列出可恢复文件

```bash
# 列出所有可恢复文件
./restore-cli -config config.yaml list

# 列出指定目录下的可恢复文件
./restore-cli -config config.yaml list /mnt/data/documents

# 列出特定备份版本中的文件
./restore-cli -config config.yaml --backup-id 2 list /mnt/data/documents
```

输出包含文件路径、大小、哈希前 8 位和修改时间。

#### `info <文件路径>` -- 查看文件详细备份信息

```bash
./restore-cli -config config.yaml info /mnt/data/documents/report.pdf
```

输出包含文件记录（ID、路径、大小、哈希、状态）和备份文件记录（备份 ID、存储密钥、压缩类型、原始大小、存储大小、加密 IV）。

#### `verify <文件路径>` -- 验证单个文件

```bash
./restore-cli -config config.yaml verify /mnt/data/documents/report.pdf
```

完整执行 下载 -> 解密 -> 解压 -> SHA-256 校验 流水线，验证通过输出 `VERIFIED`，验证失败输出 `FAILED`。验证使用临时目录，结束后自动清理。

#### `verify-dir <目录路径>` -- 批量验证目录

```bash
# 验证目录下所有文件
./restore-cli -config config.yaml verify-dir /mnt/data/documents

# 仅验证前 20 个文件
./restore-cli -config config.yaml verify-dir /mnt/data/documents --limit 20

# 验证特定备份版本
./restore-cli -config config.yaml --backup-id 2 verify-dir /mnt/data/documents
```

输出包含总数、成功数、失败数、数据量和耗时。

#### `restore <文件路径> -o <输出目录>` -- 恢复单个文件

```bash
# 基本恢复
./restore-cli -config config.yaml restore /mnt/data/documents/report.pdf -o /tmp/restored/

# 恢复特定版本
./restore-cli -config config.yaml --backup-id 2 restore /mnt/data/documents/report.pdf -o /tmp/restored/

# 使用加速解冻
./restore-cli -config config.yaml --expedited restore /mnt/data/documents/archive.zip -o /tmp/restored/
```

#### `restore-dir <目录路径> -o <输出目录>` -- 恢复整个目录

```bash
# 恢复整个目录
./restore-cli -config config.yaml restore-dir /mnt/data/documents -o /tmp/restored/

# 限制恢复文件数
./restore-cli -config config.yaml restore-dir /mnt/data/documents -o /tmp/restored/ --limit 50

# 加速解冻 + 特定版本
./restore-cli -config config.yaml --backup-id 3 --expedited restore-dir /mnt/data/documents -o /tmp/restored/
```

输出汇总：总文件数、已恢复数、失败数、数据量和耗时。失败文件会列出具体路径。

#### `bootstrap [-o <数据库路径>]` -- 从 OSS 恢复数据库

用于灾难恢复场景，从 OSS 下载最新的加密数据库并解密到本地：

```bash
# 恢复到配置文件指定的默认路径
./restore-cli -config config.yaml bootstrap

# 恢复到自定义路径
./restore-cli -config config.yaml bootstrap -o /tmp/nas-backup.db
```

此命令不需要本地数据库已存在（它是恢复数据库本身），只需 `master.key` 和 `rclone.conf`。

#### `db-backup` -- 手动触发数据库上传到 OSS

正常情况下数据库在每次备份成功后自动上传。此命令用于手动触发：

```bash
./restore-cli -config config.yaml db-backup
```

上传的数据库保留最近 3 个版本，旧版本自动清理。

---

## ColdArchive 解冻说明

阿里云 OSS 的 ColdArchive（冷归档）存储类提供极低的存储成本，但在读取数据前需要执行**解冻（Restore/Thaw）**操作。

### 存储类对比

| 存储类 | 最低存储时长 | 取回费用 | 标准解冻时间 | 加速解冻时间 |
|--------|-------------|---------|-------------|-------------|
| Standard | 无 | 无 | 即时 | -- |
| Standard_IA (低频) | 30 天 | 按量 | 即时 | -- |
| Archive (归档) | 60 天 | 按量 | 1-10 小时 | 1-5 分钟 |
| ColdArchive (冷归档) | 180 天 | 按量 | 1-10 小时 | 1-10 分钟 |
| DeepColdArchive (深度冷归档) | 180 天 | 按量 | 12-48 小时 | 不支持 |

### 解冻机制

本系统对 ColdArchive/Archive 存储的文件自动处理解冻流程：

1. **检测**：系统在下载前通过 OSS `HeadObject` API 检查对象的 `X-Oss-Restore` 头。
2. **发起解冻**：如果对象处于归档状态，自动调用 `RestoreObject` API 发起解冻请求。
3. **轮询等待**：每 30 秒检查一次解冻状态，最长等待 30 分钟。
4. **下载**：解冻完成后正常下载。

### 解冻模式

- **Standard（标准解冻）**：1-10 小时完成，费用较低。系统默认使用此模式。
- **Expedited（加速解冻）**：1-10 分钟完成，费用较高。通过 `--expedited` 标志（CLI）或 Web UI 中的"加速解冻"选项启用。

### 解冻保留期

- 系统发起解冻请求时设置 **7 天**保留期。在此期间对象可正常读取，超过保留期后对象重新回到归档状态。
- 如果需要再次访问已过期的归档文件，需要重新发起解冻。

### 费用注意事项

- **取回费用**：每次解冻都会产生数据取回费用，按取回的数据量计费。
- **提前删除费用**：ColdArchive 对象如果在最低存储时长（180 天）内被删除或转换为其他存储类，会产生提前删除费用。
- **加速解冻费用**：Expedited 模式的取回费用高于 Standard 模式。

> **建议**：对于不紧急的恢复操作，使用标准解冻以节省费用。仅在紧急恢复场景使用加速解冻。

---

## 常见问题排查（FAQ）

### Q1：恢复时提示 "hash inconsistency detected"

**原因**：`files` 表中的哈希值与 `backup_files` 表中的 `storage_key` 不一致。这通常由旧版本代码的 double-hashing bug 导致。

**解决**：使用当前版本代码重新执行备份，修复不一致的记录，然后再尝试恢复。

### Q2：恢复时提示 OSS 404 NoSuchKey

**原因**：云端对象不存在。可能原因：
- 对象已被垃圾回收（GC）错误清理。
- `hash_index` 中 `ref_count` 不正确导致 GC 误删。
- 对象从未成功上传。

**解决**：
1. 通过 Reconcile 功能检查 OSS/数据库一致性：`POST /api/reconcile?dry_run=true`。
2. 如确认为 GC 误删，需从其他备份副本恢复（如有）。
3. 如原始文件仍在本地，重新执行备份。

### Q3：恢复时提示 "object not restored after 30m0s"

**原因**：ColdArchive 解冻超时。标准解冻可能需要 1-10 小时，系统默认等待上限为 30 分钟。

**解决**：
1. 等待足够时间后重新执行恢复。
2. 或使用 `--expedited` 标志加速解冻。
3. 对于大量文件，建议先批量发起解冻请求，等待所有文件解冻完成后再统一恢复。

### Q4：恢复时提示 "a backup is currently running"

**原因**：恢复和备份操作互斥，无法同时执行。

**解决**：等待当前备份完成后再发起恢复，或取消正在运行的备份任务。

### Q5：恢复时提示 "a restore is already running"

**原因**：系统同时只允许一个恢复任务运行。

**解决**：等待当前恢复任务完成，或通过 Web UI / API 取消当前恢复任务：
```bash
# 通过 API 取消恢复任务（替换 {id} 为实际任务 ID）
curl -X POST http://127.0.0.1:8080/api/restore/jobs/{id}/cancel
```

### Q6：master.key 丢失怎么办？

**后果**：**无法恢复**。所有云端数据均为 AES-256-GCM 加密态，没有主密钥无法解密。

**预防**：
1. 立即按照本文档"必备文件清单"部分的建议备份三件套。
2. 建议配置后立即执行备份并验证恢复流程。

### Q7：nas-backup.db 损坏怎么办？

**后果**：无法定位云端对象、无法知道哪些文件已备份、无法执行恢复。

**解决**：系统已自动将加密数据库备份到 OSS，可直接从云端恢复：

```bash
# 方法1：使用 restore-cli 从 OSS 恢复最新数据库
./restore-cli -config config.yaml bootstrap

# 方法2：手动检查本地数据库是否可修复
sqlite3 ./data/nas-backup.db "PRAGMA integrity_check;"
```

> **注意**：数据库备份在每次成功备份后自动执行，保留最近 3 个版本。如果最近的备份本身也是损坏的，可以尝试恢复更早的版本（需要手动指定 OSS key）。

**预防**：系统已自动处理，无需手动备份。

### Q8：rclone.conf 中的密码忘记了怎么办？

**解决**：
1. 如果 OSS 的 `access_key_id` 和 `access_key_secret` 仍然可用，系统可以从 `config.yaml` 自动重新生成 `rclone.conf`。
2. 删除旧的 `rclone.conf`，重启服务，系统会自动从 `config.yaml` 生成新配置。
3. 如果 OSS 凭证也丢失了，需要在阿里云控制台重新创建 AccessKey。

### Q9：如何确认 OSS 连接正常？

```bash
# 方式一：CLI 验证
./restore-cli -config config.yaml backups
# 如能列出备份会话，说明数据库和 OSS 连接均正常

# 方式二：API 验证
curl http://127.0.0.1:8080/api/storage/health

# 方式三：手动测试 rclone
rclone lsf oss-crypt: --config ./data/rclone.conf --max-depth 1
```

### Q10：恢复大量文件时如何提高效率？

- 调高 `config.yaml` 中的 `storage.concurrency`（默认 8，最高建议不超过 16 以避免 OSS 限流）。
- 对于 ColdArchive 文件，先批量发起标准解冻，等待解冻完成后再统一恢复（避免每个文件都等待解冻）。
- 使用 CLI 的 `--limit` 参数分批恢复，便于控制进度和排查问题。
- 确保网络带宽充足（OSS 下载受网络带宽限制）。

---

## 附录：必备文件备份脚本

以下 Shell 脚本可定期自动备份必备三件套到指定安全位置（如加密 U 盘挂载点或远程服务器）。建议通过 cron 定时执行。

```bash
#!/bin/bash
# ---------------------------------------------------------------------------
# NAS Backup 必备三件套备份脚本
# 用途：定期将 master.key、rclone.conf、nas-backup.db 备份到安全位置
# 建议 cron：每日凌晨 2 点执行
#   0 2 * * * /opt/nas-backup/scripts/backup-essentials.sh
# ---------------------------------------------------------------------------

set -euo pipefail

# ====== 配置区 ======

# 数据源目录（nas-backup-backend 的 data 目录）
DATA_DIR="/opt/nas-backup/nas-backup-backend/data"

# 必备文件列表
ESSENTIAL_FILES=(
    "master.key"
    "rclone.conf"
    "nas-backup.db"
)

# 备份目标目录（修改为你的安全存储位置）
# 示例：加密 U 盘挂载点、远程服务器同步目录、或本地加密备份目录
BACKUP_TARGET="/mnt/backup-usb/nas-backup-essentials"

# 备份保留份数
KEEP_COUNT=30

# 日志文件
LOG_FILE="/opt/nas-backup/nas-backup-backend/data/logs/essential-backup.log"

# ====== 脚本逻辑 ======

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE"
}

error() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] ERROR: $*" | tee -a "$LOG_FILE" >&2
}

# 检查数据目录
if [ ! -d "$DATA_DIR" ]; then
    error "数据目录不存在: $DATA_DIR"
    exit 1
fi

# 检查必备文件是否齐全
for f in "${ESSENTIAL_FILES[@]}"; do
    if [ ! -f "$DATA_DIR/$f" ]; then
        error "必备文件缺失: $DATA_DIR/$f"
        exit 1
    fi
done

# 创建带时间戳的备份目录
TIMESTAMP=$(date '+%Y%m%d_%H%M%S')
BACKUP_DIR="${BACKUP_TARGET}/${TIMESTAMP}"
mkdir -p "$BACKUP_DIR"

# 复制文件
log "开始备份必备三件套..."
for f in "${ESSENTIAL_FILES[@]}"; do
    cp "$DATA_DIR/$f" "$BACKUP_DIR/$f"
    log "  已备份: $f"
done

# 设置权限（密钥文件权限收紧）
chmod 600 "$BACKUP_DIR/master.key" "$BACKUP_DIR/rclone.conf"
chmod 644 "$BACKUP_DIR/nas-backup.db"

# 验证备份完整性
log "验证备份完整性..."
ERRORS=0
for f in "${ESSENTIAL_FILES[@]}"; do
    SRC_SIZE=$(stat -c%s "$DATA_DIR/$f" 2>/dev/null || echo "0")
    DST_SIZE=$(stat -c%s "$BACKUP_DIR/$f" 2>/dev/null || echo "0")
    if [ "$SRC_SIZE" != "$DST_SIZE" ]; then
        error "  文件大小不一致: $f (源: ${SRC_SIZE}B, 备份: ${DST_SIZE}B)"
        ERRORS=$((ERRORS + 1))
    else
        log "  验证通过: $f (${SRC_SIZE} bytes)"
    fi
done

if [ "$ERRORS" -gt 0 ]; then
    error "备份验证失败，存在 $ERRORS 个文件不一致"
    exit 1
fi

# 清理旧备份，保留最近 KEEP_COUNT 份
log "清理旧备份（保留最近 ${KEEP_COUNT} 份）..."
ls -dt "${BACKUP_TARGET}"/[0-9]*/ 2>/dev/null | tail -n +$((KEEP_COUNT + 1)) | while read -r old_dir; do
    rm -rf "$old_dir"
    log "  已删除旧备份: $old_dir"
done

# 可选：如果目标是远程服务器，使用 rsync 同步
# log "同步到远程服务器..."
# rsync -avz --delete "$BACKUP_TARGET/" user@remote-server:/path/to/backup/nas-backup-essentials/

log "备份完成: $BACKUP_DIR"
```

### 使用方法

1. 将脚本保存到 `/opt/nas-backup/scripts/backup-essentials.sh`。
2. 修改脚本中的 `DATA_DIR` 和 `BACKUP_TARGET` 为你的实际路径。
3. 赋予执行权限：`chmod +x /opt/nas-backup/scripts/backup-essentials.sh`。
4. 手动测试一次：`/opt/nas-backup/scripts/backup-essentials.sh`。
5. 添加到 cron 定时任务：
   ```bash
   (crontab -l 2>/dev/null; echo "0 2 * * * /opt/nas-backup/scripts/backup-essentials.sh") | crontab -
   ```
6. 首次配置完成后立即手动执行一次，确认备份文件可正常读取。
7. **重要**：在灾难恢复演练中实际使用备份文件恢复一次，验证备份的有效性。建议每半年演练一次。

---

> **文档版本**：基于 nas-backup 系统当前代码库生成。
> **最后更新**：2026-07-08
