# NAS Backup System 部署指南

本文档覆盖三种部署方式，按需选择：

| 方式 | 适用场景 | 特点 |
|------|----------|------|
| **A. Docker Compose** | 生产部署（推荐） | 一键启动，单镜像含前后端 + Nginx，5 分钟上线 |
| **B. Debian 裸机生产** | 无 Docker 的 NAS | systemd + Nginx，编译二进制，资源占用低 |
| **C. 测试环境** | 开发调试 | `go run` + Vite HMR，改代码即时生效 |

---

## 环境要求（公共）

- 至少 2GB RAM（建议 4GB+）
- 需要备份的 NAS 目录可在宿主机访问
- 阿里云 OSS 账号（用于存储备份数据）
- 方式 B/C 额外需要：Debian 12+ / macOS

---

## 方式 A：Docker Compose 部署（推荐）

### A.1 快速开始

```bash
# 1. 克隆项目
git clone <your-repo-url> nasbkup_system
cd nasbkup_system

# 2. 编辑 docker-compose.yml，修改备份源目录挂载
#    将 /mnt/nas/documents 等改为你的实际 NAS 路径
vim docker-compose.yml

# 3. 构建并启动
docker compose up -d --build

# 4. 查看日志确认启动
docker compose logs -f

# 5. 访问 Web UI
#    http://<你的服务器IP>:8080
```

### A.2 端口与挂载配置

`docker-compose.yml` 默认配置：

```yaml
ports:
  - "8080:80"        # 宿主机:8080 -> 容器:80（Nginx 统一入口）
  # - "8081:8080"   # 可选：直连后端 API（调试用）

volumes:
  - ./data:/app/data                                    # 持久化数据（必须保留）
  - /mnt/nas/documents:/mnt/nas/documents:ro            # 备份源（:ro 只读保护）
```

> **注意**：容器内路径需与 Web UI 中配置的备份目录路径一致，建议保持相同路径结构。

### A.3 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `TZ` | `Asia/Shanghai` | 时区 |
| `DATA_DIR` | `/app/data` | 数据目录 |
| `KEY_FILE` | `/app/data/config/master.key` | 加密密钥文件路径 |
| `RCLONE_CONF` | `/app/data/rclone.conf` | rclone 配置文件路径 |
| `CONFIG_FILE` | `/app/config.yaml` | 应用配置文件路径 |

### A.4 资源限制

```yaml
mem_limit: 1g          # 内存上限 1GB
mem_reservation: 256m  # 内存预留 256MB
cpus: "2.0"            # CPU 核心数上限 2
```

### A.5 数据持久化

容器内 `/app/data` 通过 volume 挂载到宿主机 `./data`：

```
./data/
├── nas-backup.db          # SQLite 数据库（元数据）
├── rclone.conf            # rclone OSS 配置
├── config/
│   └── master.key         # AES-256 加密主密钥（极其重要！）
└── logs/
    ├── nas-backup.log
    └── nas-backup-stdout.log
```

### A.6 更新部署

```bash
git pull
docker compose up -d --build    # 数据卷不会丢失
docker compose logs -f
```

### A.7 镜像构建说明

Dockerfile 采用三阶段构建，最终镜像基于 `debian:bookworm-slim`，包含：Nginx、rclone、zstd、dumb-init、Go 后端二进制、React 前端静态文件。镜像大小约 200-300MB。

---

## 方式 B：Debian 裸机生产部署

适用于无 Docker 或希望直接运行二进制的 NAS 设备（绿联/极空间/群晖等）。

### B.1 系统准备

#### B.1.1 安装基础依赖

```bash
sudo apt update
sudo apt install -y curl wget git nginx build-essential sqlite3
```

> ⚠️ **NAS 安全提示**：切勿在定制 NAS 系统上执行 `apt upgrade` / `apt dist-upgrade`，可能破坏厂商固件。仅安装缺失包即可。

#### B.1.2 安装 Go（后端编译需要）

```bash
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

#### B.1.3 安装 Node.js（前端构建需要）

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
node -v   # v20.x.x
```

#### B.1.4 安装 rclone（后端依赖）

```bash
sudo -v ; curl https://rclone.org/install.sh | sudo bash
rclone version
```

### B.2 后端部署

#### B.2.1 部署目录与代码

```bash
sudo mkdir -p /opt/nas-backup
sudo chown $USER:$USER /opt/nas-backup
cd /opt/nas-backup
# 上传 nas-backup-backend 和 nas-backup-frontend 两个文件夹
scp -r nas-backup-backend nas-backup-frontend user@server:/opt/nas-backup/
```

#### B.2.2 编译后端

```bash
cd /opt/nas-backup/nas-backup-backend
go mod download
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o nas-backup cmd/nas-backup/main.go
./nas-backup --help
```

#### B.2.3 配置文件

```bash
mkdir -p /opt/nas-backup/nas-backup-backend/data/logs
nano /opt/nas-backup/nas-backup-backend/config.yaml
```

生产环境配置参考：

```yaml
server:
  host: "0.0.0.0"           # 监听所有网卡，局域网内可直接访问
  port: 8080
  read_timeout_sec: 30
  write_timeout_sec: 60

database:
  path: "/opt/nas-backup/nas-backup-backend/data/nas-backup.db"

backup:
  directories:
    - path: "/mnt/data/documents"
      recursive: true
      enabled: true
  schedule:
    enabled: true
    cron_expr: "0 3 * * 0"    # 每周日凌晨 3 点
    timezone: "Asia/Shanghai"
  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "/opt/nas-backup/nas-backup-backend/config/master.key"

oss:
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  bucket: "your-bucket-name"
  access_key_id: "your-access-key-id"
  access_key_secret: "your-access-key-secret"
  storage_class: "ColdArchive"
  region: "cn-hangzhou"

rclone:
  binary_path: "/usr/bin/rclone"
  config_path: "/opt/nas-backup/nas-backup-backend/data/rclone.conf"
  remote_name: "oss"

logging:
  level: "info"
  file_path: "/opt/nas-backup/nas-backup-backend/data/logs/nas-backup.log"
  max_size_mb: 100
  max_files: 30
```

#### B.2.4 创建 Systemd 服务

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
MemoryMax=512M

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup
sudo systemctl start nas-backup
sudo systemctl status nas-backup
```

#### B.2.5 验证后端

```bash
curl http://127.0.0.1:8080/api/dashboard/stats
```

### B.3 前端部署

#### B.3.1 构建前端

```bash
cd /opt/nas-backup/nas-backup-frontend
npm ci
npm run build    # 产物在 dist/
```

#### B.3.2 配置 Nginx

```bash
sudo nano /etc/nginx/sites-available/nas-backup
```

```nginx
server {
    listen 80;
    server_name _;

    location / {
        root /opt/nas-backup/nas-backup-frontend/dist;
        index index.html;
        try_files $uri $uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_connect_timeout 30s;
        proxy_send_timeout 120s;
        proxy_read_timeout 120s;
    }

    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;
    gzip_min_length 1000;

    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2)$ {
        expires 30d;
        add_header Cache-Control "public";
    }
}
```

```bash
sudo ln -sf /etc/nginx/sites-available/nas-backup /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx
```

> **NAS 端口冲突提示**：若 NAS 系统 80 端口已被占用，改为 `listen 8090;` 并通过 NAS 防火墙放行。

### B.4 访问方式

```
http://<服务器局域网IP>
```

### B.5 更新部署

```bash
# 后端
cd /opt/nas-backup/nas-backup-backend
git pull
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o nas-backup cmd/nas-backup/main.go
sudo systemctl restart nas-backup

# 前端
cd /opt/nas-backup/nas-backup-frontend
git pull
npm ci --production=false
npm run build
# Nginx 自动 serving 新文件，无需重启
```

> 💡 也可使用项目提供的 `scripts/update-debian.sh` 一键完成 pull + 构建 + 重启 + 健康检查，详见 [SCRIPTS.md](SCRIPTS.md)。

---

## 方式 C：测试环境部署（开发模式）

适用于功能验证和开发调试，改代码即时生效。

### C.1 系统准备

同方式 B.1（安装 Go / Node.js / rclone）。

### C.2 后端部署（go run 开发模式）

```bash
sudo mkdir -p /opt/nas-backup-test
sudo chown $USER:$USER /opt/nas-backup-test
cd /opt/nas-backup-test
# 上传 nas-backup-backend 和 nas-backup-frontend
```

测试环境 config.yaml 关键差异：

```yaml
server:
  read_timeout_sec: 60       # 测试环境超时放宽
  write_timeout_sec: 120

backup:
  schedule:
    enabled: false           # 测试环境默认关闭定时任务

logging:
  level: "debug"             # 测试环境使用 debug 级别
  max_size_mb: 50
  max_files: 10
```

创建 systemd 服务（用 `go run` 而非编译二进制）：

```bash
sudo nano /etc/systemd/system/nas-backup-test.service
```

```ini
[Unit]
Description=NAS Backup Test Service
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/nas-backup-test/nas-backup-backend
ExecStart=/usr/local/go/bin/go run cmd/nas-backup/main.go -- -config /opt/nas-backup-test/nas-backup-backend/config.yaml
Restart=always
RestartSec=5
Environment=HOME=/root
Environment=PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
Environment=GOPATH=/root/go
Environment=GOGC=off
LimitNOFILE=65536
MemoryMax=1G

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup-test
sudo systemctl start nas-backup-test
```

### C.3 前端部署（Vite HMR）

```bash
cd /opt/nas-backup-test/nas-backup-frontend
npm install
```

创建前端开发服务器服务：

```bash
sudo nano /etc/systemd/system/nas-backup-test-frontend.service
```

```ini
[Unit]
Description=NAS Backup Test Frontend Dev Server
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/nas-backup-test/nas-backup-frontend
ExecStart=/usr/bin/npm run dev -- --host 0.0.0.0
Restart=always
RestartSec=5
LimitNOFILE=65536
MemoryMax=512M

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup-test-frontend
sudo systemctl start nas-backup-test-frontend
```

### C.4 访问方式

| 方式 | 地址 | 说明 |
|------|------|------|
| 直接访问前端 | `http://<服务器IP>:5173` | Vite 开发服务器，支持 HMR |
| 通过 Nginx 访问 | `http://<服务器IP>` | 统一 80 端口（需配置 Nginx 反代 5173） |
| 直接访问后端 API | `http://<服务器IP>:8080` | 后端 API 接口 |

### C.5 重置测试数据

```bash
sudo systemctl stop nas-backup-test
rm -f /opt/nas-backup-test/nas-backup-backend/data/nas-backup.db*
sudo systemctl start nas-backup-test
```

### C.6 测试环境与生产环境对比

| 项目 | 测试环境 | 生产环境 |
|------|----------|----------|
| 部署路径 | `/opt/nas-backup-test/` | `/opt/nas-backup/` |
| 后端运行方式 | `go run`（无需编译） | 编译二进制 + `-ldflags="-s -w"` |
| 前端运行方式 | Vite 开发服务器（HMR） | `npm run build` + Nginx 静态托管 |
| 日志级别 | `debug` | `info` |
| 定时任务 | 默认关闭 | 默认开启 |
| 超时设置 | 放宽（60s/120s） | 标准（30s/60s） |
| 内存限制 | 1GB | 512MB |
| 服务名 | `nas-backup-test` | `nas-backup` |

---

## 公共配置说明

### 密钥生成（首次部署必做）

```bash
openssl rand -base64 32 > ./config/master.key
chmod 600 ./config/master.key
```

> ⚠️ **`master.key` 和 `rclone.conf` 是恢复数据的唯一凭证，必须异地备份！**
> - `master.key` 丢失 → 所有云端加密数据永远无法解密
> - `rclone.conf` 丢失 → 无法连接 OSS 访问备份数据

### rclone 配置

**方式一：通过 Web UI 配置（推荐）**

访问 Web UI →「策略设置」→「上传」，填写 OSS Endpoint / Bucket / AccessKey，保存后系统自动更新 rclone 配置。

**方式二：交互式配置**

```bash
rclone config --config ./data/rclone.conf
```

或使用项目脚本：

```bash
cd nas-backup-backend
./scripts/setup-rclone.sh
```

**方式三：手动编辑**

```ini
[oss]
type = s3
provider = Aliyun
access_key_id = YOUR_ACCESS_KEY_ID
secret_access_key = YOUR_ACCESS_KEY_SECRET
endpoint = oss-cn-hangzhou.aliyuncs.com
acl = private
```

> 仅需单个原生 `[oss]` 远程，无 crypt 层；内容加/解密由应用层 `master.key` 负责。

### OSS 配置（Docker 环境首次启动后）

容器首次启动会自动生成 `master.key` 和 `rclone.conf` 模板，但 OSS 凭证为空。配置后重启容器：

```bash
docker compose restart
```

---

## 常用操作

### 日志查看

```bash
# Docker
docker compose logs -f
docker compose logs --tail 100 -f
docker compose exec nas-backup tail -f /app/data/logs/nas-backup.log

# 裸机
sudo journalctl -u nas-backup -f              # 后端 systemd 日志
tail -f /opt/nas-backup/nas-backup-backend/data/logs/nas-backup.log   # 应用日志
tail -f /var/log/nginx/access.log             # Nginx 日志
```

### 健康检查

```bash
curl http://127.0.0.1:8080/api/dashboard/stats
# 返回 JSON 统计数据即正常
```

### 手动触发备份

```bash
curl -X POST http://127.0.0.1:8080/api/backup/trigger
curl -X POST http://127.0.0.1:8080/api/backup/trigger -H "Content-Type: application/json" -d '{"type":"full"}'
curl http://127.0.0.1:8080/api/backup/status
```

### 数据库定期备份

```bash
cat > ./backup-db.sh << 'EOF'
#!/bin/bash
BACKUP_DIR="./db-backups"
DATE=$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP_DIR"
cp ./data/nas-backup.db "$BACKUP_DIR/nas-backup_$DATE.db"
ls -t "$BACKUP_DIR"/nas-backup_*.db | tail -n +31 | xargs -r rm
EOF
chmod +x ./backup-db.sh

# 添加到 crontab 每日自动备份
(crontab -l 2>/dev/null; echo "0 2 * * * cd $(pwd) && ./backup-db.sh") | crontab -
```

> 系统每次备份后会自动将加密数据库上传到 OSS `meta/db/`，但建议本地也定期备份。

### 停止/启动/重启

```bash
# Docker
docker compose stop / start / restart

# 裸机
sudo systemctl stop / start / restart nas-backup
```

---

## 灾难恢复

如需在新环境恢复数据，参考 [RESTORE_GUIDE.md](RESTORE_GUIDE.md) 的「场景 B：整台 NAS 丢失灾难恢复」。

简要步骤：

1. 在新环境部署（方式 A 或 B）
2. 将备份的 `master.key` 放到 `config/` 目录、`rclone.conf` 放到 `./data/` 目录
3. 启动服务
4. 使用 `restore-cli` 从 OSS 引导恢复数据库：

```bash
# Docker
docker compose exec nas-backup restore-cli -config /app/config.yaml bootstrap

# 裸机
cd /opt/nas-backup/nas-backup-backend
./restore-cli -config config/config.yaml bootstrap
```

5. 通过 Web UI 或 CLI 恢复文件

---

## 故障排查

| 问题 | 排查方法 |
|------|----------|
| 容器启动后立即退出 | `docker compose logs` 查看错误日志 |
| 后端无法启动（裸机） | `sudo journalctl -u nas-backup -n 50` |
| 无法访问 Web UI | 检查端口映射/防火墙；`docker compose ps` / `systemctl status nas-backup` |
| API 返回 502 | 等待后端启动（首次 10-30 秒）；检查后端是否运行 |
| 前端白屏 | 检查 `dist/` 是否存在；`nginx -t` 验证配置 |
| 备份失败 | 检查 rclone 配置和 OSS 凭证；`rclone lsd oss:` 测试连通性 |
| 权限错误无法读取 NAS 目录 | 检查宿主机目录权限；NFS/SMB 挂载需允许 Docker/systemd 访问 |
| 数据库被锁定 | 确保只有一个实例运行；检查 `./data/` 目录权限 |
| 局域网无法访问 | 确认服务器 IP；检查 Nginx 监听端口；NAS 防火墙放行 |

### 常见问题

**Q: 容器内无法读取挂载的 NAS 目录？**

A: 1) 确认宿主机目录存在且可访问；2) 检查 SELinux/AppArmor；3) NFS/SMB 挂载需允许 Docker 访问。

**Q: 如何修改配置后不重建镜像？**

A: 挂载自定义配置文件覆盖：`- ./my-config.yaml:/app/config.yaml:ro`

**Q: NAS 上 80 端口被系统占用？**

A: Nginx 改监听 8090 等空闲端口，并在 NAS 防火墙放行。

---

部署完成！通过 `http://<服务器IP>:8080`（Docker）或 `http://<服务器IP>`（裸机）即可使用。
