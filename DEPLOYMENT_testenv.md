# NAS Backup 测试环境部署指南（Debian）

本文档描述如何在 Debian 服务器上快速部署 NAS Backup **测试环境**。
适用于功能验证、开发调试和日常测试，不适用于生产使用。

---

## 环境要求

- Debian 12 (Bookworm) 或更高版本
- 至少 2GB RAM
- 需要备份的目录挂载到服务器（如 `/mnt/data`）
- 局域网内可访问

---

## 1. 系统准备

### 1.1 安装基础依赖

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl wget git nginx build-essential sqlite3
```

### 1.2 安装 Go（后端运行需要）

```bash
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
rm go1.25.0.linux-amd64.tar.gz

echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh

go version
```

### 1.3 安装 Node.js（前端开发服务器需要）

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs

node -v   # v20.x.x
npm -v    # 10.x.x
```

### 1.4 安装 rclone（后端依赖）

```bash
sudo -v ; curl https://rclone.org/install.sh | sudo bash
rclone version
```

---

## 2. 项目部署

### 2.1 创建部署目录

```bash
sudo mkdir -p /opt/nas-backup-test
sudo chown $USER:$USER /opt/nas-backup-test
cd /opt/nas-backup-test
```

### 2.2 上传项目代码

```bash
# 使用 scp（从本地上传）
scp -r nas-backup-backend nas-backup-frontend user@your-server-ip:/opt/nas-backup-test/

# 或使用 git clone
# git clone <your-repo> .
```

目录结构：
```
/opt/nas-backup-test/
├── nas-backup-backend/
└── nas-backup-frontend/
```

---

## 3. 后端部署（开发模式）

测试环境使用 `go run` 直接运行，无需编译，支持快速重启。

### 3.1 准备配置

```bash
# 创建数据目录
mkdir -p /opt/nas-backup-test/nas-backup-backend/data/logs

# 编辑配置文件
nano /opt/nas-backup-test/nas-backup-backend/config.yaml
```

**测试环境配置项**：

```yaml
server:
  host: "0.0.0.0"           # 局域网内可直接访问
  port: 8080
  read_timeout_sec: 60       # 测试环境超时放宽
  write_timeout_sec: 120

database:
  path: "/opt/nas-backup-test/nas-backup-backend/data/nas-backup.db"

backup:
  directories:
    - path: "/mnt/data/documents"
      recursive: true
      enabled: true
      description: "Documents"

  schedule:
    enabled: false           # 测试环境默认关闭定时任务，手动触发
    cron_expr: "0 3 * * 0"
    timezone: "Asia/Shanghai"

  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "/opt/nas-backup-test/nas-backup-backend/data/master.key"

oss:
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  bucket: "your-bucket-name"
  access_key_id: "your-access-key-id"
  access_key_secret: "your-access-key-secret"
  storage_class: "ColdArchive"
  region: "cn-hangzhou"

rclone:
  binary_path: "/usr/bin/rclone"
  config_path: "/opt/nas-backup-test/nas-backup-backend/data/rclone.conf"
  remote_name: "oss-crypt"

logging:
  level: "debug"             # 测试环境使用 debug 级别，输出更多日志
  file_path: "/opt/nas-backup-test/nas-backup-backend/data/logs/nas-backup.log"
  max_size_mb: 50
  max_files: 10
```

### 3.2 生成加密密钥

```bash
openssl rand -base64 32 > /opt/nas-backup-test/nas-backup-backend/data/master.key
```

### 3.3 配置 rclone

```bash
rclone config --config /opt/nas-backup-test/nas-backup-backend/data/rclone.conf
```

### 3.4 创建 Systemd 服务

```bash
sudo nano /etc/systemd/system/nas-backup-test.service
```

内容如下：

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

# 资源限制（测试环境适当放宽）
LimitNOFILE=65536
MemoryMax=1G

[Install]
WantedBy=multi-user.target
```

> **说明**：使用 `go run` 而非编译后的二进制，方便修改代码后快速重启生效。
> 如果需要更高性能，可改为编译后运行，参考 `DEPLOYMENT.md`。

### 3.5 启动服务

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup-test
sudo systemctl start nas-backup-test

# 查看状态
sudo systemctl status nas-backup-test
sudo journalctl -u nas-backup-test -f
```

### 3.6 验证后端 API

```bash
curl http://127.0.0.1:8080/api/dashboard/stats
```

---

## 4. 前端部署（开发模式）

测试环境使用 Vite 开发服务器，支持热模块替换（HMR），修改代码后浏览器自动刷新。

### 4.1 安装依赖

```bash
cd /opt/nas-backup-test/nas-backup-frontend
npm install
```

### 4.2 创建 Systemd 服务（前端开发服务器）

```bash
sudo nano /etc/systemd/system/nas-backup-test-frontend.service
```

内容如下：

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

> **说明**：`--host 0.0.0.0` 使 Vite 开发服务器监听所有网卡，局域网内可直接访问。

### 4.3 启动前端服务

```bash
sudo systemctl daemon-reload
sudo systemctl enable nas-backup-test-frontend
sudo systemctl start nas-backup-test-frontend

# 查看状态
sudo systemctl status nas-backup-test-frontend
sudo journalctl -u nas-backup-test-frontend -f
```

### 4.4 配置 Nginx（可选）

如果需要通过 80 端口统一访问，可配置 Nginx 反向代理到 Vite 开发服务器：

```bash
sudo nano /etc/nginx/sites-available/nas-backup-test
```

```nginx
server {
    listen 80;
    server_name _;

    # 前端开发服务器（Vite 默认 5173 端口）
    location / {
        proxy_pass http://127.0.0.1:5173;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # API 反向代理到后端
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
}
```

启用站点：

```bash
sudo ln -sf /etc/nginx/sites-available/nas-backup-test /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx
```

> **注意**：如果不配置 Nginx，也可以直接访问 Vite 开发服务器端口 `http://<服务器IP>:5173`。

---

## 5. 访问方式

| 方式 | 地址 | 说明 |
|------|------|------|
| 直接访问前端 | `http://<服务器IP>:5173` | Vite 开发服务器，支持 HMR |
| 通过 Nginx 访问 | `http://<服务器IP>` | 统一 80 端口（需配置 Nginx） |
| 直接访问后端 API | `http://<服务器IP>:8080` | 后端 API 接口 |

---

## 6. 测试数据生成

项目提供了测试数据生成工具，可用于快速创建模拟文件进行备份测试：

```bash
cd /opt/nas-backup-test
python3 nas_file_generator.py --output /mnt/data/test-files --count 500
```

---

## 7. 常用操作

### 7.1 查看日志

```bash
# 后端日志（debug 级别，输出详细）
sudo journalctl -u nas-backup-test -f

# 应用日志文件
tail -f /opt/nas-backup-test/nas-backup-backend/data/logs/nas-backup.log

# 前端开发服务器日志
sudo journalctl -u nas-backup-test-frontend -f

# Nginx 日志
tail -f /var/log/nginx/access.log
tail -f /var/log/nginx/error.log
```

### 7.2 重启服务（代码更新后）

```bash
# 后端：修改代码后重启
sudo systemctl restart nas-backup-test

# 前端：Vite 开发服务器支持 HMR，通常无需重启
# 如需重启：
sudo systemctl restart nas-backup-test-frontend
```

### 7.3 重置测试数据

```bash
# 停止后端服务
sudo systemctl stop nas-backup-test

# 清空数据库（重新初始化）
rm -f /opt/nas-backup-test/nas-backup-backend/data/nas-backup.db
rm -f /opt/nas-backup-test/nas-backup-backend/data/nas-backup.db-shm
rm -f /opt/nas-backup-test/nas-backup-backend/data/nas-backup.db-wal

# 重启服务，数据库会自动重建
sudo systemctl start nas-backup-test
```

### 7.4 手动触发备份测试

```bash
curl -X POST http://127.0.0.1:8080/api/backup/trigger
```

查看备份状态：

```bash
curl http://127.0.0.1:8080/api/backup/status
```

---

## 8. 与生产环境的区别

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
| HTTPS | 不需要 | 不需要（局域网） |

---

## 9. 故障排查

| 问题 | 排查方法 |
|------|----------|
| 后端无法启动 | `sudo journalctl -u nas-backup-test -n 50` |
| 前端开发服务器无法启动 | `sudo journalctl -u nas-backup-test-frontend -n 50` |
| API 返回 502 | 检查后端是否运行：`curl http://127.0.0.1:8080/api/dashboard/stats` |
| HMR 不生效 | 确认 Vite 监听 `0.0.0.0`，检查 WebSocket 连接 |
| 备份失败 | 检查 rclone 配置和 OSS 凭证 |
| 数据库错误 | 删除 `.db` 文件重启服务，让数据库自动重建 |

---

## 10. 目录结构参考

```
/opt/nas-backup-test/
├── nas-backup-backend/
│   ├── cmd/
│   ├── internal/
│   ├── data/
│   │   ├── nas-backup.db       # 测试数据库（可随时重置）
│   │   ├── master.key
│   │   ├── rclone.conf
│   │   └── logs/
│   │       └── nas-backup.log
│   ├── config.yaml
│   └── go.mod
├── nas-backup-frontend/
│   ├── src/
│   ├── node_modules/
│   ├── index.html
│   └── package.json
└── nas_file_generator.py        # 测试数据生成工具
```

---

部署完成！局域网内通过 `http://<服务器IP>:5173` 即可访问测试环境。
