# NAS Backup Backend

NAS 备份系统后端服务，基于 Go 开发。

## 技术栈

- Go 1.25+
- SQLite
- RESTful API

## 项目结构

```
nas-backup-backend/
├── cmd/nas-backup/     # 入口程序
├── internal/           # 内部模块
│   ├── api/            # HTTP API 路由与处理器
│   ├── backup/         # 备份引擎
│   ├── compress/       # 压缩模块
│   ├── config/         # 配置管理
│   ├── crypto/         # 加密模块
│   ├── db/             # 数据库与存储
│   ├── dedup/          # 去重模块
│   ├── logger/         # 日志模块
│   ├── models/         # 数据模型
│   ├── scanner/        # 文件扫描
│   ├── scheduler/      # 定时任务
│   └── storage/        # 云存储管理
├── data/               # 数据文件
├── scripts/            # 脚本工具
└── config.yaml         # 配置文件
```

## 快速开始

```bash
# 安装依赖
go mod download

# 运行服务
go run cmd/nas-backup/main.go

# 或使用配置文件
go run cmd/nas-backup/main.go -config ./config.yaml
```

## 配置说明

编辑 `config.yaml` 配置数据库路径、备份策略、云存储等参数。

## API 文档

所有 API 均以 `/api` 为前缀，支持 CORS 跨域访问。

主要接口：
- `GET /api/dashboard/stats` - 仪表盘统计
- `GET /api/dashboard/history` - 备份历史
- `POST /api/backup/trigger` - 触发备份
- `GET /api/fs/browse` - 浏览文件系统
- `GET /api/content/directories` - 管理备份目录
- `GET /api/strategy/schedule` - 备份策略管理
- `GET /api/logs` - 查看日志
