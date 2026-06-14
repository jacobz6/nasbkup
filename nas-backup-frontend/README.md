# NAS Backup Frontend

NAS 备份系统前端界面，基于 React + TypeScript + Vite 开发。

## 技术栈

- React 18
- TypeScript
- Vite
- Tailwind CSS
- Zustand (状态管理)
- React Router
- Lucide React (图标)

## 项目结构

```
nas-backup-frontend/
├── src/
│   ├── components/     # 组件
│   │   ├── layout/     # 布局组件
│   │   ├── shared/     # 共享组件
│   │   └── ui/         # UI 组件
│   ├── hooks/          # 自定义 Hooks
│   ├── lib/            # 工具库
│   ├── pages/          # 页面
│   ├── store/          # 状态管理
│   ├── utils/          # 工具函数
│   ├── App.tsx         # 根组件
│   ├── main.tsx        # 入口
│   └── index.css       # 全局样式
├── public/             # 静态资源
└── index.html          # HTML 模板
```

## 快速开始

```bash
# 安装依赖
npm install

# 启动开发服务器
npm run dev

# 构建生产版本
npm run build
```

## 开发配置

开发时默认通过 Vite 代理访问后端 API：

```ts
// vite.config.ts
server: {
  proxy: {
    '/api': {
      target: 'http://localhost:8080',
      changeOrigin: true,
    },
  },
}
```

请确保后端服务运行在 `http://localhost:8080`。

## 页面说明

- **Dashboard** - 仪表盘，展示备份统计与历史
- **Content** - 内容管理，配置备份目录与排除规则
- **Strategy** - 策略配置，设置压缩、加密、定时任务等
- **Logs** - 日志查看
