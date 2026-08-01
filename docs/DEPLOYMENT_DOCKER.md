# NAS Backup Docker 部署指南

本文档描述如何使用 Docker Compose 快速部署 NAS Backup 系统。这是推荐的部署方式，一键启动，无需手动配置环境。

---

## 目录

- [环境要求](#环境要求)
- [快速开始](#快速开始)
- [配置说明](#配置说明)
- [数据持久化](#数据持久化)
- [备份源目录挂载](#备份源目录挂载)
- [OSS 配置](#oss-配置)
- [常用操作](#常用操作)
- [更新部署](#更新部署)
- [灾难恢复](#灾难恢复)
- [故障排查](#故障排查)

---

## 环境要求

- Docker 20.10+
- Docker Compose v2+
- 至少 2GB RAM（建议 4GB+）
- 需要备份的 NAS 目录可在宿主机访问
- 阿里云 OSS 账号（用于存储备份数据）

---

## 快速开始

### 1. 获取项目代码

```bash
git clone <your-repo-url> nasbkup_system
cd nasbkup_system
```

### 2. 修改备份源挂载路径

编辑 `docker-compose.yml`，修改 `volumes` 段中的备份源目录，将宿主机的 NAS 目录映射到容器内：

```yaml
volumes:
  # 持久化数据（必须保留）
  - ./data:/app/data

  # ---- 修改这里为你的实际 NAS 目录路径 ----
  # 格式: - 宿主机路径:容器内路径:ro  (ro = 只读挂载，保护源文件)
  - /mnt/nas/documents:/mnt/nas/documents:ro
  - /mnt/nas/photos:/mnt/nas/photos:ro
  # - /mnt/nas/videos:/mnt/nas/videos:ro
```

> **注意**：容器内的路径需要与后续在 Web UI 中配置的备份目录路径一致。建议使用相同的路径结构。

### 3. 构建并启动容器

```bash
# 构建镜像并后台启动
docker compose up -d --build

# 查看启动日志
docker compose logs -f
```

### 4. 访问系统

启动成功后，通过浏览器访问：

```
http://<宿主机IP>:8080
```

默认端口映射：宿主机 `8080` → 容器 `80`（Nginx 统一入口）。

---

## 配置说明

### 端口映射

`docker-compose.yml` 默认配置：

```yaml
ports:
  - "8080:80"        # 宿主机:8080 -> 容器:80（推荐，Nginx 统一入口）
  # - "8081:8080"   # 可选：直连后端 API（调试用）
```

如需修改宿主机端口，将 `8080:80` 改为你想要的端口，例如 `80:80` 直接使用 80 端口。

### 环境变量

容器支持以下环境变量（一般无需修改）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `TZ` | `Asia/Shanghai` | 时区 |
| `DATA_DIR` | `/app/data` | 数据目录 |
| `KEY_FILE` | `/app/data/master.key` | 加密密钥文件路径 |
| `RCLONE_CONF` | `/app/data/rclone.conf` | rclone 配置文件路径 |
| `CONFIG_FILE` | `/app/config.yaml` | 应用配置文件路径 |

### 资源限制

默认资源限制（可根据 NAS 规模调整）：

```yaml
mem_limit: 1g          # 内存上限 1GB
mem_reservation: 256m  # 内存预留 256MB
cpus: "2.0"            # CPU 核心数上限 2
```

---

## 数据持久化

容器内 `/app/data` 目录通过 volume 挂载到宿主机 `./data` 目录，包含所有持久化数据：

```
./data/
├── nas-backup.db          # SQLite 数据库（元数据）
├── master.key             # AES-256 加密主密钥（极其重要！）
├── rclone.conf            # rclone OSS 配置
└── logs/
    ├── nas-backup.log     # 应用日志
    └── nas-backup-stdout.log  # 后端标准输出日志
```

### ⚠️ 重要安全提醒

**`master.key` 和 `rclone.conf` 是恢复数据的唯一凭证，必须备份到容器外的安全位置！**

- `master.key` 丢失 → 所有云端加密数据永远无法解密
- `rclone.conf` 丢失 → 无法连接 OSS 访问备份数据

建议在首次启动后立即备份这两个文件：

```bash
# 备份到安全位置
cp ./data/master.key /path/to/secure/backup/
cp ./data/rclone.conf /path/to/secure/backup/
```

---

## 备份源目录挂载

### 挂载规则

- 使用只读挂载（`:ro`）防止备份系统误修改源文件
- 可以挂载多个目录
- 容器内路径建议与宿主机路径保持一致，便于管理
- 支持挂载子目录

### 挂载示例

```yaml
volumes:
  # 单目录挂载
  - /volume1/documents:/volume1/documents:ro

  # 多目录挂载
  - /volume1/photos:/volume1/photos:ro
  - /volume1/music:/volume1/music:ro
  - /volume1/videos:/volume1/videos:ro

  # 挂载整个 NAS 根目录（如需要）
  # - /volume1:/volume1:ro
```

### 修改挂载路径后

修改 `docker-compose.yml` 的 volumes 后需要重启容器：

```bash
docker compose up -d
```

---

## OSS 配置

首次启动后，容器会自动生成 `master.key` 和 `rclone.conf` 模板，但 OSS 凭证为空，需要配置后才能正常备份。

### 方式一：通过 Web UI 配置（推荐）

1. 访问 Web UI → 「策略设置」→「上传」
2. 填写以下信息：
   - OSS Endpoint（如 `oss-cn-hangzhou.aliyuncs.com`）
   - Bucket 名称
   - AccessKey ID
   - AccessKey Secret
   - 区域（如 `cn-hangzhou`）
   - 存储类型（推荐 `ColdArchive` 冷归档，成本最低）
3. 保存配置后系统会自动更新 rclone 配置

### 方式二：手动编辑 rclone.conf

```bash
# 编辑 rclone 配置
nano ./data/rclone.conf
```

填写以下内容：

```ini
[oss]
type = s3
provider = Aliyun
access_key_id = YOUR_ACCESS_KEY_ID
secret_access_key = YOUR_ACCESS_KEY_SECRET
endpoint = oss-cn-hangzhou.aliyuncs.com
acl = private

[oss-crypt]
type = crypt
remote = oss:your-bucket-name/nas-backup
filename_encryption = standard
directory_name_encryption = true
password = YOUR_RCLONE_CRYPT_PASSWORD
```

> **提示**：`password` 是 rclone crypt 层的加密密码，可以通过 `rclone obscure your-password` 生成。

配置完成后重启容器：

```bash
docker compose restart
```

---

## 常用操作

### 查看日志

```bash
# 查看所有日志
docker compose logs -f

# 查看最近 100 行日志
docker compose logs --tail 100 -f

# 仅查看后端日志
docker compose exec nas-backup tail -f /app/data/logs/nas-backup.log
```

### 进入容器

```bash
docker compose exec nas-backup bash
```

### 手动触发备份

```bash
# 通过 API 触发备份
curl -X POST http://127.0.0.1:8080/api/backup/trigger

# 指定备份类型（full/incremental/auto）
curl -X POST http://127.0.0.1:8080/api/backup/trigger -H "Content-Type: application/json" -d '{"type":"full"}'
```

### 查看备份状态

```bash
curl http://127.0.0.1:8080/api/backup/status
```

### 停止/启动/重启

```bash
# 停止
docker compose stop

# 启动
docker compose start

# 重启
docker compose restart
```

### 健康检查

容器内置健康检查，每 30 秒探测一次 API 端点：

```bash
# 查看健康状态
docker compose ps
```

---

## 更新部署

### 更新到最新版本

```bash
# 1. 拉取最新代码
git pull

# 2. 重新构建并启动（数据卷不会丢失）
docker compose up -d --build

# 3. 查看日志确认启动正常
docker compose logs -f
```

### 数据库备份（重要）

虽然系统每次备份后会自动将数据库加密上传到 OSS，但建议定期手动备份本地数据库：

```bash
# 创建备份脚本
cat > ./backup-db.sh << 'EOF'
#!/bin/bash
BACKUP_DIR="./db-backups"
DATE=$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP_DIR"
cp ./data/nas-backup.db "$BACKUP_DIR/nas-backup_$DATE.db"
ls -t "$BACKUP_DIR"/nas-backup_*.db | tail -n +31 | xargs -r rm
echo "Backup created: $BACKUP_DIR/nas-backup_$DATE.db"
EOF

chmod +x ./backup-db.sh

# 手动执行
./backup-db.sh

# 添加到 crontab 每日自动备份（可选）
(crontab -l 2>/dev/null; echo "0 2 * * * cd $(pwd) && ./backup-db.sh") | crontab -
```

---

## 灾难恢复

如果需要在新环境中恢复数据，请参考 [RESTORE_GUIDE.md](RESTORE_GUIDE.md) 的「场景 B：整台 NAS 丢失灾难恢复」章节。

简要步骤：

1. 在新环境安装 Docker 和 Docker Compose
2. 将备份的 `master.key` 和 `rclone.conf` 放到 `./data/` 目录
3. 启动容器
4. 使用 `restore-cli` 工具从 OSS 引导恢复数据库
5. 通过 Web UI 或 CLI 恢复文件

```bash
# 在容器内执行 bootstrap 恢复数据库
docker compose exec nas-backup restore-cli -config /app/config.yaml bootstrap
```

---

## 故障排查

| 问题 | 排查方法 |
|------|----------|
| 容器启动后立即退出 | `docker compose logs` 查看错误日志 |
| 无法访问 Web UI | 检查端口映射是否正确，`docker compose ps` 查看容器状态 |
| API 返回 502 | 等待后端启动完成（首次启动约 10-30 秒），查看后端日志 |
| 备份失败 | 检查 OSS 配置是否正确，`rclone lsd oss-crypt:` 测试连通性 |
| 权限错误无法读取 NAS 目录 | 检查宿主机目录权限，确保 Docker 进程有读取权限 |
| 数据库被锁定 | 确保只有一个容器实例在运行，检查 `./data/` 目录权限 |

### 常见问题

**Q: 容器内无法读取挂载的 NAS 目录？**

A: 检查以下几点：
1. 宿主机上该目录是否存在且可访问
2. SELinux/AppArmor 是否阻止了访问（可暂时禁用测试）
3. 尝试使用 `privileged: true` 模式（不推荐长期使用）
4. 对于 NFS/SMB 挂载，确保挂载选项允许 Docker 访问

**Q: 如何修改配置后不重建镜像？**

A: 可以挂载自定义配置文件覆盖容器内默认配置：

```yaml
volumes:
  - ./my-config.yaml:/app/config.yaml:ro
```

**Q: 如何查看容器内的文件结构？**

A: `docker compose exec nas-backup bash` 进入容器后浏览。

---

## 镜像构建说明

Dockerfile 采用三阶段构建，最终镜像基于 `debian:bookworm-slim`，包含：

- Nginx（前端静态 + API 反代 + SSE 配置）
- rclone（OSS 上传/下载）
- zstd（压缩/解压）
- dumb-init（PID 1 信号处理）
- 编译好的 Go 后端二进制
- 构建好的 React 前端静态文件

最终镜像大小约 200-300MB。

---

部署完成！通过 `http://<宿主机IP>:8080` 即可使用 NAS Backup 系统。
