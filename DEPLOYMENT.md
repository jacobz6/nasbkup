# NAS Backup 局域网部署指南（Debian）

本文档描述如何在 Debian 服务器上以**局域网内部署**方式部署 NAS Backup 前后端分离项目。
服务仅在局域网内使用，不暴露到公网。

---

## 环境要求

- Debian 12 (Bookworm) 或更高版本
- 至少 2GB RAM，建议 4GB+
- 需要备份的目录挂载到服务器（如 `/mnt/data`）
- 局域网内客户端可通过服务器 IP 直接访问

---

## 1. 系统准备

### 1.1 安装基础依赖

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl wget git nginx build-essential sqlite3
```

### 1.2 安装 Go（后端编译需要）

```bash
# 下载并安装 Go 1.25+（根据后端 go.mod 要求）
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
rm go1.25.0.linux-amd64.tar.gz

# 配置环境变量
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh

# 验证
go version
```

### 1.3 安装 Node.js（前端构建需要）

```bash
# 使用 NodeSource 安装 Node.js 20
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs

# 验证
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
sudo mkdir -p /opt/nas-backup
sudo chown $USER:$USER /opt/nas-backup
cd /opt/nas-backup
```

### 2.2 上传项目代码

将 `nas-backup-backend` 和 `nas-backup-frontend` 两个文件夹上传到服务器：

```bash
# 方式1：使用 scp（从本地上传）
scp -r nas-backup-backend nas-backup-frontend user@your-server-ip:/opt/nas-backup/

# 方式2：使用 git clone
# git clone <your-repo> .
```

目录结构应为：
```
/opt/nas-backup/
├── nas-backup-backend/
└── nas-backup-frontend/
```

---

## 3. 后端部署

### 3.1 编译后端

```bash
cd /opt/nas-backup/nas-backup-backend

# 下载依赖
go mod download

# 编译生产二进制文件
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o nas-backup cmd/nas-backup/main.go

# 验证
./nas-backup --help
```

### 3.2 配置后端

```bash
# 创建数据目录
mkdir -p /opt/nas-backup/nas-backup-backend/data/logs

# 编辑配置文件
nano /opt/nas-backup/nas-backup-backend/config.yaml
```

**局域网部署配置项**：

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
      description: "Documents"

  schedule:
    enabled: true
    cron_expr: "0 3 * * 0"    # 每周日凌晨 3 点
    timezone: "Asia/Shanghai"

  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "/opt/nas-backup/nas-backup-backend/data/master.key"

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
  remote_name: "oss-crypt"

logging:
  level: "info"
  file_path: "/opt/nas-backup/nas-backup-backend/data/logs/nas-backup.log"
  max_size_mb: 100
  max_files: 30
```

### 3.3 生成加密密钥

```bash
# 生成 32 字节随机密钥
openssl rand -base64 32 > /opt/nas-backup/nas-backup-backend/data/master.key
```

### 3.4 配置 rclone

```bash
# 配置 rclone（交互式）
rclone config --config /opt/nas-backup/nas-backup-backend/data/rclone.conf
```

按照提示配置 OSS 加密远程存储（crypt）。

### 3.5 创建 Systemd 服务

```bash
sudo nano /etc/systemd/system/nas-backup.service
```

内容如下：

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

# 资源限制
LimitNOFILE=65536
MemoryMax=512M

[Install]
WantedBy=multi-user.target
```

### 3.6 启动服务

```bash
# 重载 systemd 并启动
sudo systemctl daemon-reload
sudo systemctl enable nas-backup
sudo systemctl start nas-backup

# 查看状态
sudo systemctl status nas-backup
sudo journalctl -u nas-backup -f
```

### 3.7 验证后端 API

```bash
# 从本机验证
curl http://127.0.0.1:8080/api/dashboard/stats

# 从局域网内其他机器验证（替换为服务器局域网 IP）
curl http://192.168.x.x:8080/api/dashboard/stats
```

应返回 JSON 格式的统计数据。

---

## 4. 前端部署

### 4.1 构建前端

```bash
cd /opt/nas-backup/nas-backup-frontend

# 安装依赖
npm ci

# 构建生产版本
npm run build
```

构建完成后，`dist/` 目录包含静态文件。

### 4.2 配置 Nginx

```bash
sudo nano /etc/nginx/sites-available/nas-backup
```

内容如下：

```nginx
server {
    listen 80;
    server_name _;  # 局域网部署，不限定域名，匹配所有请求

    # 前端静态文件
    location / {
        root /opt/nas-backup/nas-backup-frontend/dist;
        index index.html;
        try_files $uri $uri/ /index.html;
    }

    # API 反向代理到后端
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # 超时设置（备份操作可能耗时较长）
        proxy_connect_timeout 30s;
        proxy_send_timeout 120s;
        proxy_read_timeout 120s;
    }

    # Gzip 压缩
    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;
    gzip_min_length 1000;

    # 缓存静态资源
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2)$ {
        expires 30d;
        add_header Cache-Control "public";
    }
}
```

启用站点：

```bash
sudo ln -sf /etc/nginx/sites-available/nas-backup /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx
```

---

## 5. 访问方式

部署完成后，局域网内任意设备通过浏览器访问：

```
http://<服务器局域网IP>
```

例如：`http://192.168.1.100`

---

## 6. 监控与日志

### 6.1 查看服务日志

```bash
# 后端服务日志
sudo journalctl -u nas-backup -f

# 应用日志
tail -f /opt/nas-backup/nas-backup-backend/data/logs/nas-backup.log

# Nginx 日志
tail -f /var/log/nginx/access.log
tail -f /var/log/nginx/error.log
```

### 6.2 备份数据库

```bash
# 创建备份脚本
sudo nano /opt/nas-backup/backup-db.sh
```

```bash
#!/bin/bash
BACKUP_DIR="/opt/nas-backup/backups"
DB_PATH="/opt/nas-backup/nas-backup-backend/data/nas-backup.db"
DATE=$(date +%Y%m%d_%H%M%S)

mkdir -p "$BACKUP_DIR"
cp "$DB_PATH" "$BACKUP_DIR/nas-backup_$DATE.db"

# 保留最近 30 个备份
ls -t "$BACKUP_DIR"/nas-backup_*.db | tail -n +31 | xargs -r rm
```

```bash
chmod +x /opt/nas-backup/backup-db.sh

# 添加到 crontab
(sudo crontab -l 2>/dev/null; echo "0 2 * * * /opt/nas-backup/backup-db.sh") | sudo crontab -
```

---

## 7. 更新部署

### 7.1 更新后端

```bash
cd /opt/nas-backup/nas-backup-backend
git pull  # 或上传新代码

# 重新编译
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o nas-backup cmd/nas-backup/main.go

# 重启服务
sudo systemctl restart nas-backup
```

### 7.2 更新前端

```bash
cd /opt/nas-backup/nas-backup-frontend
git pull  # 或上传新代码

# 重新构建
npm ci --production=false
npm run build

# Nginx 自动 serving 新文件，无需重启
```

---

## 8. 故障排查

| 问题 | 排查方法 |
|------|----------|
| 后端无法启动 | `sudo journalctl -u nas-backup -n 50` |
| API 返回 502 | 检查后端是否运行：`curl http://127.0.0.1:8080/api/dashboard/stats` |
| 前端白屏 | 检查 `dist/` 是否存在，`nginx -t` 验证配置 |
| 局域网无法访问 | 确认服务器 IP 正确，检查 Nginx 是否监听 80 端口 |
| 备份失败 | 检查 rclone 配置和 OSS 凭证 |

---

## 9. 目录结构参考

```
/opt/nas-backup/
├── nas-backup-backend/
│   ├── nas-backup              # 编译后的二进制文件
│   ├── cmd/
│   ├── internal/
│   ├── data/
│   │   ├── nas-backup.db
│   │   ├── master.key
│   │   ├── rclone.conf
│   │   └── logs/
│   │       └── nas-backup.log
│   ├── config.yaml
│   └── go.mod
├── nas-backup-frontend/
│   ├── dist/                   # 构建后的静态文件
│   ├── src/
│   ├── index.html
│   └── package.json
└── backups/                    # 数据库备份
```

---

部署完成！局域网内通过 `http://<服务器IP>` 即可使用 NAS Backup 系统。
