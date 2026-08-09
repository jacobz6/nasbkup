<p align="center">
  <h1 align="center">☁️ NAS Backup System</h1>
  <p align="center">
    一个安全、高效、易用的 NAS 自动化云备份系统，将本地数据备份到阿里云 OSS 冷归档存储
  </p>
</p>

<p align="center">
  <a href="#-核心特性">核心特性</a> •
  <a href="#-技术栈">技术栈</a> •
  <a href="#-快速开始">快速开始</a> •
  <a href="#-文档">文档</a> •
  <a href="#-安全提醒">安全提醒</a> •
  <a href="#-许可证">许可证</a>
</p>

---

## 📖 项目简介

NAS Backup System 是一个面向家庭和小型企业 NAS 设备的自动化备份解决方案，采用 B/S 架构，提供直观的 Web 管理界面。系统将本地 NAS 文件加密、压缩、去重后备份到阿里云 OSS 冷归档存储，成本极低，同时支持完整的数据恢复能力。

```
┌─────────────┐     ┌─────────────┐     ┌─────────────────────┐
│  NAS 存储   │────▶│  备份引擎   │────▶│  阿里云 OSS 冷归档  │
│  (本地)     │     │  (本系统)   │     │  (云端加密存储)     │
└─────────────┘     └─────────────┘     └─────────────────────┘
                          │
                    ┌─────┴─────┐
                    │  Web UI   │
                    │ (浏览器)  │
                    └───────────┘
```

## ✨ 核心特性

| 特性 | 说明 |
|------|------|
| 🔐 **端到端加密** | AES-256-GCM 加密，HKDF 派生每文件密钥，数据上传前已加密 |
| 📦 **内容去重** | 基于 SHA-256 哈希的全局去重，相同内容只存储一份，大幅节省空间 |
| 🗜️ **智能压缩** | zstd 最高级别压缩，自动跳过已压缩文件类型（视频/图片/压缩包等） |
| ❄️ **冷归档支持** | 原生支持 OSS ColdArchive 存储类，存储成本极低（含标准/加急解冻） |
| 🔄 **三种备份模式** | 全量备份、增量备份、智能模式（自动判断） |
| ⏰ **定时调度** | Cron 表达式定时备份，支持时区配置 |
| 📊 **实时进度** | SSE 实时推送备份/恢复进度，阶段、百分比、当前文件、实时日志 |
| ✅ **数据一致性** | 三层数据对账（OSS ↔ 哈希索引 ↔ 备份文件），支持 DryRun 审计和自动修复 |
| 🛡️ **崩溃恢复** | 启动自动清理残留状态，全量备份重建映射，防并发锁保护 |
| 🔥 **灾难恢复** | 每次备份后自动加密上传数据库到 OSS，支持 `restore-cli bootstrap` 从零重建 |
| 🖥️ **Web UI 恢复** | 可视化文件浏览、多选恢复、冲突策略（跳过/覆盖/重命名）、恢复历史 |
| 💻 **CLI 工具** | 独立 `restore-cli` 命令行工具，支持 verify/restore/bootstrap 等全套命令 |
| 🐳 **Docker 部署** | 三阶段构建镜像，Docker Compose 一键启动，内含 Nginx + rclone + zstd |

## 🔧 技术栈

### 后端
- **Go 1.25** - 高性能后端服务
- **SQLite** (WAL 模式) - 轻量级元数据存储，无需额外数据库
- **阿里云 OSS SDK + rclone** - 可靠的云存储传输
- **AES-256-GCM + HKDF** - 行业标准加密
- **robfig/cron/v3** - 定时任务调度

### 前端
- **React 18 + TypeScript** - 现代化前端框架
- **Vite 6** - 极速构建工具
- **Tailwind CSS 3** - 实用优先的 CSS 框架
- **Zustand 5** - 轻量级状态管理
- **SSE (Server-Sent Events)** - 实时进度推送

### 部署
- **Docker** - 三阶段构建，单镜像包含前后端
- **Docker Compose** - 一键编排
- **Nginx** - 前端静态托管 + API 反向代理 + SSE 长连接配置
- **dumb-init** - 正确的 PID 1 信号处理

## 🚀 快速开始

### 使用 Docker Compose（推荐）

最快 5 分钟即可启动完整备份系统：

```bash
# 1. 克隆项目
git clone https://github.com/jacobz6/nasbkup.git nasbkup_system
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

### 首次配置

1. 访问 Web UI 后，进入「策略设置」→「上传」
2. 填写阿里云 OSS 配置（Endpoint、Bucket、AccessKey ID/Secret、区域）
3. 在「内容选择」中添加需要备份的目录
4. （可选）在「策略设置」中配置定时备份
5. 点击「立即备份」开始第一次备份

### ⚠️ 极其重要：备份密钥

首次启动后，容器会自动生成 `master.key` 和 `rclone.conf`，请**立即备份**这两个文件到安全位置：

```bash
# 密钥文件位置（Docker 环境）
ls -la ./data/
# - master.key     AES-256 主密钥（丢失则数据永远无法恢复！）
# - rclone.conf    OSS 凭证 + rclone crypt 密码
```

**建议采用至少两种方式异地备份**：加密 U 盘、另一台服务器、其他云存储。

## 📚 文档

完整文档请查看 [`docs/`](docs/INDEX.md) 目录：

| 文档 | 说明 |
|------|------|
| [📖 文档中心](docs/INDEX.md) | 所有文档的索引入口 |
| [🚀 部署指南](docs/DEPLOYMENT.md) | Docker / Debian 裸机 / 测试环境三种部署方式 |
| [💾 恢复操作指南](docs/RESTORE_GUIDE.md) | 数据恢复全流程（日常误删/灾难恢复/全盘迁移） |
| [🔧 故障排查](docs/TROUBLESHOOTING.md) | 环境/配置/代码/解冻/恢复各类问题排查 |
| [📚 代码百科 WIKI](docs/WIKI.md) | 完整代码架构详解（架构/API/Schema/Bug 修复记录） |
| [📜 脚本说明](docs/SCRIPTS.md) | 所有辅助脚本的用途和使用方法 |

## 📁 项目结构

```
nasbkup_system/
├── README.md                    # 本文件（GitHub 主页）
├── docker-compose.yml           # Docker Compose 编排
├── Dockerfile                   # Docker 三阶段构建
├── docker/                      # Docker 配置
│   ├── entrypoint.sh            # 容器启动脚本
│   ├── nginx.conf               # Nginx 配置（含 SSE）
│   └── config.docker.yaml       # 容器默认配置
├── nas-backup-backend/          # Go 后端服务
│   ├── cmd/
│   │   ├── nas-backup/          # HTTP 服务入口
│   │   └── restore-cli/         # CLI 恢复工具
│   ├── internal/                # 内部包
│   │   ├── api/                 # HTTP API 层
│   │   ├── backup/              # 备份引擎 + 恢复器 + 对账
│   │   ├── scanner/             # 文件扫描与哈希计算
│   │   ├── dedup/               # 内容去重
│   │   ├── compress/            # zstd 压缩
│   │   ├── crypto/              # AES-256-GCM 加密
│   │   ├── storage/             # OSS 存储管理
│   │   ├── db/                  # SQLite 数据访问层
│   │   ├── scheduler/           # 定时任务调度
│   │   └── config/              # 配置加载
│   ├── config.yaml.example      # 配置文件模板
│   └── scripts/                 # 后端辅助脚本
├── nas-backup-frontend/         # React 前端
│   ├── src/
│   │   ├── pages/               # 页面组件
│   │   ├── components/          # UI 组件
│   │   ├── store/               # Zustand 状态
│   │   └── utils/               # API 客户端等工具
│   └── package.json
├── scripts/                     # 部署与验证脚本
│   ├── deploy.sh                # 统一部署（macOS + Debian 自动适配）
│   ├── start.sh                 # 统一启停（macOS nohup / Debian systemd）
│   ├── update-debian.sh         # Debian 代码更新
│   ├── verify-e2e.sh            # 端到端验证脚本
│   ├── verify_e2e.py            # E2E 验证（Python）
│   ├── verify_cloud_archive.py  # 云端归档验证
│   ├── nas_file_generator.py    # 测试数据生成器
│   ├── generate_report.py       # 验收报告生成器
│   └── lib/                     # 脚本公共函数库
├── docs/                        # 文档中心
│   ├── INDEX.md                 # 文档索引
│   ├── WIKI.md                  # 代码百科
│   ├── DEPLOYMENT.md            # 部署指南（Docker/裸机/测试三合一）
│   ├── TROUBLESHOOTING.md       # 故障排查
│   ├── RESTORE_GUIDE.md         # 恢复操作指南
│   └── SCRIPTS.md               # 脚本说明
└── AGENTS.md                    # AI Agent 运行手册
```

## 🔒 安全设计

- **零信任加密**：所有文件在上传前已在本地加密，OSS 只存储加密后的密文
- **文件名加密**：通过 rclone crypt 加密文件名和目录名，OSS 侧无法看到原始文件结构
- **只读挂载**：备份源目录推荐只读挂载（`:ro`），防止备份系统误修改源文件
- **路径白名单**：恢复操作仅允许恢复到配置的白名单目录，防止路径遍历攻击
- **互斥锁保护**：备份和恢复操作互斥，防止并发操作导致数据不一致
- **权限最小化**：容器内密钥文件权限 600，仅所有者可读写

## 🛟 灾难恢复

即使本地 NAS 和服务器完全损坏，只要有 `master.key` 和 `rclone.conf` 两个文件，即可在新环境中恢复所有数据：

```bash
# 1. 在新环境部署 Docker 版本
# 2. 将备份的 master.key 和 rclone.conf 放到 ./data/ 目录
# 3. 启动容器
docker compose up -d

# 4. 进入容器执行 bootstrap 从 OSS 恢复数据库
docker compose exec nas-backup restore-cli -config /app/config.yaml bootstrap

# 5. 通过 Web UI 或 CLI 恢复文件
```

详细步骤请参考 [恢复操作指南](docs/RESTORE_GUIDE.md)。

## 🧪 开发

### 本地开发环境

```bash
# 后端
cd nas-backup-backend
go mod download
go run cmd/nas-backup/main.go -config config.yaml

# 前端（另一个终端）
cd nas-backup-frontend
npm install
npm run dev
```

### 运行测试

```bash
cd nas-backup-backend
export PATH="$HOME/go-sdk/go/bin:$PATH"
go vet ./...
CGO_ENABLED=1 go test -count=1 -short -timeout 300s ./...
```

### 生成测试数据

```bash
python3 scripts/nas_file_generator.py --output /tmp/test-files --count 500
```

## ⚠️ 免责声明

- 本项目按"原样"提供，不提供任何形式的保证。使用前请充分测试。
- 请务必妥善保管 `master.key`，丢失密钥等同于丢失所有备份数据。
- 建议定期进行恢复演练，确保备份可用。
- 冷归档存储的文件恢复需要先解冻，标准解冻最长等待 30 分钟。

## 📄 许可证

MIT License

---

<p align="center">
  如果这个项目对你有帮助，欢迎 Star ⭐
</p>
