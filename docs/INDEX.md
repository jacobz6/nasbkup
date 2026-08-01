# NAS Backup System - 文档中心

欢迎使用 NAS Backup System！本文档中心包含了项目的所有文档，帮助你快速上手、部署和使用本系统。

---

## 🚀 快速开始

| 文档 | 说明 | 读者 |
|------|------|------|
| [**部署指南 - Docker（推荐）**](DEPLOYMENT_DOCKER.md) | Docker Compose 一键部署，最快 5 分钟启动 | 所有用户 |
| [部署指南 - 生产环境](DEPLOYMENT_PRODUCTION.md) | Debian 裸机部署，systemd + Nginx | 运维人员 |
| [部署指南 - 测试环境](DEPLOYMENT_TESTENV.md) | 开发测试环境部署，支持热重载 | 开发者 |
| [恢复操作指南](RESTORE_GUIDE.md) | 数据恢复全流程（日常误删/灾难恢复/全盘迁移） | 所有用户 |

---

## 📚 核心文档

| 文档 | 说明 | 读者 |
|------|------|------|
| [**代码百科 WIKI**](WIKI.md) | 完整代码架构详解（100+ KB）：模块、API、数据库、流程 | 开发者/AI |
| [脚本说明](SCRIPTS.md) | 所有辅助脚本的用途和使用方法 | 运维/开发者 |
| [验收报告](ACCEPTANCE_REPORT.md) | 生产可行性验收报告：云端闭环验证、Bug 修复记录、已知限制 | 运维/评估者 |

---

## 📋 设计与规划文档

这些文档是开发过程中的设计资料，供二次开发参考：

| 文档 | 说明 |
|------|------|
| [需求对齐](ALIGNMENT.md) | 恢复功能需求边界与验收标准 |
| [架构设计](DESIGN.md) | 恢复功能技术架构与模块设计 |
| [任务拆分](TASKS.md) | 开发任务拆分与进度追踪 |

---

## 🏗️ 项目结构

```
nasbkup_system/
├── README.md                    # 项目主页（你正在看的项目根目录）
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
│   ├── internal/                # 内部包（api/backup/scanner/dedup 等）
│   ├── scripts/                 # 后端脚本
│   └── data/                    # 运行时数据（DB/密钥/日志）
├── nas-backup-frontend/         # React 前端
│   ├── src/
│   │   ├── pages/               # 页面组件
│   │   ├── components/          # UI 组件
│   │   └── utils/               # 工具函数
│   └── dist/                    # 构建产物
├── docs/                        # 文档中心（本目录）
└── nas_file_generator.py        # 测试文件生成器
```

---

## 💡 核心功能一览

- **内容寻址去重**：SHA-256 哈希全局去重，相同内容只存一份
- **三种备份模式**：全量(full)、增量(incremental)、智能(auto)
- **端到端加密**：AES-256-GCM + HKDF 每文件密钥派生
- **zstd 压缩**：最高级别压缩，智能跳过已压缩文件
- **冷归档支持**：OSS ColdArchive 存储类，标准/加急解冻
- **定时调度**：cron 表达式定时备份，自动判断全量/增量
- **SSE 实时进度**：Server-Sent Events 推送备份/恢复实时进度
- **数据一致性对账**：三层校验（OSS ↔ hash_index ↔ backup_files）
- **崩溃恢复**：启动自动清理残留状态，全量备份重建映射
- **灾难恢复**：每次备份后自动加密上传 DB 到 OSS，支持 bootstrap 重建
- **Web UI 恢复**：异步恢复任务 + SSE 进度 + 历史记录 + 冲突策略
- **CLI 恢复工具**：restore-cli 独立二进制，支持 verify/restore/bootstrap

---

## 🔧 技术栈

### 后端
- Go 1.25
- SQLite (WAL 模式, CGO)
- 阿里云 OSS SDK + rclone
- AES-256-GCM 加密
- zstd 压缩
- robfig/cron/v3 调度

### 前端
- React 18 + TypeScript 5.8
- Vite 6
- Tailwind CSS 3.4
- Zustand 5
- react-router-dom 7
- lucide-react 图标

### 部署
- Docker 三阶段构建
- Docker Compose
- Nginx（静态托管 + API 反代 + SSE 配置）
- systemd（裸机部署）

---

## 📞 获取帮助

1. 首先阅读 [Docker 部署指南](DEPLOYMENT_DOCKER.md) 完成部署
2. 日常使用参考 [恢复操作指南](RESTORE_GUIDE.md)
3. 二次开发查阅 [代码百科 WIKI](WIKI.md)
4. 脚本问题查看 [脚本说明](SCRIPTS.md)

---

## ⚠️ 重要安全提醒

**`master.key` 和 `rclone.conf` 是恢复数据的唯一凭证！**

- `master.key` 丢失 → 所有云端加密数据永远无法解密
- `rclone.conf` 丢失 → 无法连接 OSS 访问备份数据

首次部署后请**立即备份**这两个文件到安全位置！
