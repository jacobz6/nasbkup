# NAS Backup 恢复功能任务拆分 (TASKS)

## 模块依赖关系

```
M1(迁移+模型) → M2(Repo) → M3(ProgressBroker)
                                    ↓
M1 → M4(引擎增强) → M5(JobManager) → M6(API+Router) → M7(Config+Main)
                                                        ↓
                                                    M8(前端API) → M9(前端页面)
                                    
M10(文档) — 独立，可并行
M11(编译测试+git push) — 最后执行
```

## M1: 数据库迁移 + 模型类型
- **风险等级**: 低
- **前置依赖**: 无
- **文件**:
  - 新增 `db/migrations/003_add_restore_jobs.sql`
  - 修改 `internal/models/models.go` — 新增 RestoreJobStatus, RestoreJobRecord, RestoreProgressEvent, RestorableFile
- **验收标准**: 迁移文件语法正确；Go 模型编译通过

## M2: RestoreJobRepository
- **风险等级**: 低
- **前置依赖**: M1
- **文件**: 新增 `internal/db/restore_job_repo.go`, 修改 `internal/db/db.go`
- **验收标准**: CRUD 操作覆盖 Create/GetByID/UpdateStatus/UpdateProgress/UpdateCompleted/List/GetRunning/CleanupStaleRunning

## M3: RestoreProgressBroker
- **风险等级**: 低
- **前置依赖**: M1
- **文件**: 新增 `internal/backup/restore_progress.go`
- **验收标准**: Subscribe/Publish/Phase/Progress/File/Log/ClearHistory 方法完整，与现有 ProgressBroker 模式一致

## M4: 恢复引擎增强
- **风险等级**: 中
- **前置依赖**: M1
- **文件**: 修改 `internal/backup/restore.go`
- **验收标准**:
  - resolveFiles 支持目录路径递归展开
  - restoreFile 支持 conflict_strategy (overwrite/skip/rename)
  - Restore 接受 onFileProgress 回调
  - validateOutputDir 安全校验

## M5: RestoreJobManager
- **风险等级**: 高（核心调度逻辑）
- **前置依赖**: M2, M3, M4
- **文件**: 新增 `internal/backup/restore_job.go`
- **验收标准**:
  - CreateJob 创建任务记录
  - StartJob 异步执行恢复并通过 SSE 推送进度
  - CancelJob 取消运行中任务
  - 备份/恢复互斥检查

## M6: API Handler + Router
- **风险等级**: 中
- **前置依赖**: M5
- **文件**: 修改 `internal/api/restore_handler.go`, `internal/api/router.go`
- **验收标准**: 所有 7 个新路由正确注册和响应

## M7: Config + main.go 接线
- **风险等级**: 中
- **前置依赖**: M5
- **文件**: 修改 `internal/config/config.go`, `cmd/nas-backup/main.go`, `internal/backup/engine.go`
- **验收标准**: RestoreJobManager 正确注入到 Router；Config 新增 restore_base_dirs

## M8: 前端 API 类型 + restoreApi + SSE
- **风险等级**: 低
- **前置依赖**: 无（纯前端）
- **文件**: 修改 `nas-backup-frontend/src/utils/api.ts`, 新增 `nas-backup-frontend/src/hooks/useRestoreProgress.ts`
- **验收标准**: restoreApi, createRestoreProgressStream, 所有 TypeScript 类型定义正确

## M9: 前端 Restore 页面
- **风险等级**: 高（UI 复杂度高）
- **前置依赖**: M8
- **文件**: 新增 `nas-backup-frontend/src/pages/Restore.tsx`, 修改 `nas-backup-frontend/src/App.tsx`, `nas-backup-frontend/src/components/layout/Sidebar.tsx`
- **验收标准**:
  - 恢复页面正常渲染，文件列表可加载
  - 多选文件、备份版本选择、目标目录、冲突策略
  - 一键全盘恢复
  - 恢复进度实时展示
  - 恢复历史表格

## M10: 恢复操作文档
- **风险等级**: 低
- **前置依赖**: 无
- **文件**: 新增 `docs/RESTORE_GUIDE.md`
- **验收标准**: 覆盖三种恢复场景 + 必备文件清单 + CLI 和 Web UI 操作说明

## M11: 编译测试 + git push
- **风险等级**: 中
- **前置依赖**: M1-M9 全部完成
- **验收标准**: `go build` 通过，前端 `npm run build` 通过，`go test` 通过，git push 成功
