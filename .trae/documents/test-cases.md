# NAS Backup 系统测试用例

## 测试说明

本文档覆盖NAS Backup系统所有核心业务功能，包括：
- 仪表盘功能
- 备份操作（全量/增量/自动/取消）
- 内容管理（目录/排除规则/文件浏览）
- 策略配置（调度/压缩/上传/保留/加密）
- 日志查询
- 恢复功能
- 垃圾回收（GC）
- 数据对账（Reconcile）
- 存储健康检查
- SSE实时进度流

每个测试用例包含：用例ID、模块、测试场景、前置条件、测试步骤、预期结果、优先级。

---

## 1. 仪表盘模块 (Dashboard)

### TC-DASH-001: 获取仪表盘统计数据（空系统）
- **优先级**: P0
- **前置条件**: 系统初始化完成，无任何备份记录
- **测试步骤**:
  1. 调用 GET /api/dashboard/stats
- **预期结果**:
  - HTTP 200
  - success: true
  - total_files: 0
  - total_size: 0
  - oss_storage_used: 0
  - backup_count: 0
  - unique_hash_count: 0
  - active_backup_running: false
  - needs_reconcile: false
  - oss_info 字段完整（storage_class, endpoint, bucket, region, remote_name）

### TC-DASH-002: 获取仪表盘统计数据（有备份数据）
- **优先级**: P0
- **前置条件**: 已完成至少1次全量备份
- **测试步骤**:
  1. 执行1次全量备份
  2. 调用 GET /api/dashboard/stats
- **预期结果**:
  - HTTP 200
  - total_files > 0
  - total_size > 0
  - oss_storage_used > 0
  - backup_count >= 1
  - unique_hash_count > 0
  - last_backup_time 有值
  - last_backup_status = "completed"

### TC-DASH-003: 获取备份历史（默认分页）
- **优先级**: P0
- **前置条件**: 系统有备份记录
- **测试步骤**:
  1. 调用 GET /api/dashboard/history
- **预期结果**:
  - HTTP 200
  - success: true
  - page: 1
  - size: 20
  - total >= 0
  - data 为数组，按创建时间倒序排列
  - 每条记录包含 id, type, status, total_files, total_size, created_at 等字段

### TC-DASH-004: 获取备份历史（自定义分页）
- **优先级**: P1
- **前置条件**: 系统有至少30条备份记录
- **测试步骤**:
  1. 调用 GET /api/dashboard/history?page=2&size=10
- **预期结果**:
  - HTTP 200
  - page: 2
  - size: 10
  - data.length = 10
  - 返回第11-20条记录

### TC-DASH-005: 分页参数边界测试 - size上限
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/dashboard/history?size=500
- **预期结果**:
  - HTTP 200
  - size: 200（硬上限生效，防止内存压力）

### TC-DASH-006: 分页参数边界测试 - page<1
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/dashboard/history?page=0
  2. 调用 GET /api/dashboard/history?page=-1
- **预期结果**:
  - HTTP 200
  - page: 1（自动修正为第一页）

### TC-DASH-007: 分页参数边界测试 - size<1
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/dashboard/history?size=0
  2. 调用 GET /api/dashboard/history?size=-5
- **预期结果**:
  - HTTP 200
  - size: 20（自动修正为默认值）

### TC-DASH-008: 备份运行中状态显示
- **优先级**: P0
- **前置条件**: 触发一个长时间运行的备份
- **测试步骤**:
  1. 触发全量备份（选择含大量文件的目录）
  2. 备份运行中立即调用 GET /api/dashboard/stats
- **预期结果**:
  - active_backup_running: true
  - 备份完成后 active_backup_running: false

### TC-DASH-009: 调度器启用时下一次备份时间
- **优先级**: P1
- **前置条件**: 启用定时备份调度
- **测试步骤**:
  1. 配置并启用调度（如每天凌晨2点）
  2. 调用 GET /api/dashboard/stats
- **预期结果**:
  - next_backup_time 有值，为下一次计划执行时间

### TC-DASH-010: 数据不一致时显示对账告警
- **优先级**: P1
- **前置条件**: 人为制造数据不一致（如手动删除DB中的hash_index记录）
- **测试步骤**:
  1. 制造数据不一致场景
  2. 调用 GET /api/dashboard/stats
- **预期结果**:
  - needs_reconcile: true

---

## 2. 备份操作模块 (Backup)

### TC-BACKUP-001: 触发自动备份（无全量备份时）
- **优先级**: P0
- **前置条件**: 系统无成功完成的全量备份，配置了至少一个启用的备份目录
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: {"type": "auto"}
- **预期结果**:
  - HTTP 202 Accepted
  - success: true
  - data.backup_id > 0
  - data.status: "pending"
  - 系统执行全量备份（因无历史全量）
  - 备份最终状态为 completed

### TC-BACKUP-002: 触发自动备份（有近期全量备份时）
- **优先级**: P0
- **前置条件**: 最近7天内有成功完成的全量备份，full_reset_interval为30天
- **测试步骤**:
  1. 先完成1次全量备份
  2. 立即调用 POST /api/backup/trigger，body: {"type": "auto"}
- **预期结果**:
  - HTTP 202
  - 系统执行增量备份
  - 备份记录 type 为 "incremental"

### TC-BACKUP-003: 触发自动备份（全量备份超期时）
- **优先级**: P1
- **前置条件**: 修改full_reset_interval为0月（或模拟上次全量超过间隔）
- **测试步骤**:
  1. 完成1次全量备份
  2. 将 full_reset_interval 设为0
  3. 调用 POST /api/backup/trigger，body: {"type": "auto"}
- **预期结果**:
  - HTTP 202
  - 系统执行全量备份

### TC-BACKUP-004: 触发全量备份
- **优先级**: P0
- **前置条件**: 配置了至少一个启用的备份目录
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: {"type": "full"}
- **预期结果**:
  - HTTP 202
  - 备份记录 type = "full"
  - 备份完成后所有文件被处理
  - hash_index 中建立完整索引

### TC-BACKUP-005: 触发增量备份（无基础全量时）
- **优先级**: P1
- **前置条件**: 系统无任何备份记录
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: {"type": "incremental"}
- **预期结果**:
  - 系统应回退到全量备份或返回合理错误
  - （根据业务逻辑验证实际行为）

### TC-BACKUP-006: 触发备份 - type参数缺失（默认auto）
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: {}
- **预期结果**:
  - HTTP 202
  - 按type=auto处理

### TC-BACKUP-007: 触发备份 - 无效type参数
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: {"type": "invalid_type"}
- **预期结果**:
  - HTTP 400 Bad Request
  - success: false
  - error 信息提示 type 必须是 'full', 'incremental', 或 'auto'

### TC-BACKUP-008: 触发备份 - 请求体格式错误
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/backup/trigger，body: "not json"
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "invalid request body"

### TC-BACKUP-009: 并发备份触发冲突
- **优先级**: P0
- **前置条件**: 一个备份正在运行中
- **测试步骤**:
  1. 触发一个全量备份（含大文件，确保运行时间足够）
  2. 在备份运行期间，立即再次调用 POST /api/backup/trigger
- **预期结果**:
  - 第二次请求返回 HTTP 409 Conflict
  - error 信息提示备份已在运行中
  - 不会启动第二个备份进程（互斥锁+DB双重检查生效）

### TC-BACKUP-010: 取消正在运行的备份（通过backup_id）
- **优先级**: P0
- **前置条件**: 备份正在运行中
- **测试步骤**:
  1. 触发备份，获取backup_id
  2. 调用 POST /api/backup/cancel?backup_id={id}
- **预期结果**:
  - HTTP 200
  - data.status: "cancelled"
  - 备份记录状态变为 cancelled
  - 备份引擎停止工作
  - SSE 收到 cancelled 事件

### TC-BACKUP-011: 取消正在运行的备份（不指定backup_id）
- **优先级**: P1
- **前置条件**: 有且仅有一个备份正在运行
- **测试步骤**:
  1. 触发备份
  2. 调用 POST /api/backup/cancel（不带backup_id参数）
- **预期结果**:
  - HTTP 200
  - 自动找到运行中的备份并取消
  - data.status: "cancelled"

### TC-BACKUP-012: 取消备份 - 无运行中备份
- **优先级**: P2
- **前置条件**: 系统空闲，无备份运行
- **测试步骤**:
  1. 调用 POST /api/backup/cancel
- **预期结果**:
  - HTTP 404 Not Found
  - error: "no backup is currently running"

### TC-BACKUP-013: 取消备份 - 无效backup_id格式
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/backup/cancel?backup_id=abc
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "invalid backup_id"

### TC-BACKUP-014: 取消备份 - 崩溃恢复场景（清理stale记录）
- **优先级**: P0
- **前置条件**: 模拟进程崩溃：DB中有running状态记录但内存中无运行备份
- **测试步骤**:
  1. 手动在DB中插入一条status='running'的备份记录
  2. 重启服务（或直接调用cancel）
  3. 调用 POST /api/backup/cancel
- **预期结果**:
  - HTTP 200
  - data.status: "cancelled"
  - data.note 包含 "cleared stale running record"
  - DB中该记录状态更新为 failed

### TC-BACKUP-015: 获取备份状态 - 空闲状态
- **优先级**: P1
- **前置条件**: 无备份运行
- **测试步骤**:
  1. 调用 GET /api/backup/status
- **预期结果**:
  - HTTP 200
  - is_running: false
  - running_backup: null

### TC-BACKUP-016: 获取备份状态 - 运行中状态
- **优先级**: P1
- **前置条件**: 备份正在运行
- **测试步骤**:
  1. 触发备份
  2. 立即调用 GET /api/backup/status
- **预期结果**:
  - HTTP 200
  - is_running: true
  - running_backup 不为 null，包含完整的备份记录信息

### TC-BACKUP-017: 服务启动时清理残留运行状态
- **优先级**: P0
- **前置条件**: DB中有pending/running状态的备份记录（模拟进程崩溃）
- **测试步骤**:
  1. 停止服务
  2. 手动将DB中一个completed备份的状态改为running
  3. 启动服务
  4. 查询GET /api/backup/status
- **预期结果**:
  - 服务启动成功
  - 残留的running/pending记录被标记为failed
  - is_running: false
  - 日志记录stale备份清理信息

### TC-BACKUP-018: 增量备份 - 未变化文件跳过
- **优先级**: P0
- **前置条件**: 已完成1次全量备份
- **测试步骤**:
  1. 不修改任何文件
  2. 触发增量备份
- **预期结果**:
  - HTTP 202
  - 备份完成后 skipped_inc > 0（所有未变化文件被跳过）
  - uploaded_size 很小（无新文件上传）

### TC-BACKUP-019: 增量备份 - 新增/修改/删除文件处理
- **优先级**: P0
- **前置条件**: 已完成1次全量备份
- **测试步骤**:
  1. 新增1个文件
  2. 修改1个已有文件的内容
  3. 删除1个已有文件
  4. 触发增量备份
- **预期结果**:
  - 新文件被备份
  - 修改的文件产生新hash并上传
  - 删除的文件在files表中标记为deleted
  - 备份记录skipped_inc为未变化文件数

### TC-BACKUP-020: 全量备份重建hash索引
- **优先级**: P0
- **前置条件**: 存在ref_count漂移问题
- **测试步骤**:
  1. 人为制造ref_count不一致（如手动修改hash_index的ref_count）
  2. 执行全量备份
- **预期结果**:
  - 全量备份完成后，hash_index的ref_count被正确重建
  - active文件的hash都有正确的ref_count

---

## 3. SSE实时进度模块 (Progress Stream)

### TC-SSE-001: SSE连接建立与connected事件
- **优先级**: P0
- **前置条件**: 无
- **测试步骤**:
  1. 使用EventSource连接 GET /api/backup/progress/stream
- **预期结果**:
  - 连接成功建立
  - Content-Type: text/event-stream
  - Cache-Control: no-cache
  - Connection: keep-alive
  - X-Accel-Buffering: no
  - 第一个事件为 type: "connected"

### TC-SSE-002: SSE心跳保活
- **优先级**: P1
- **前置条件**: SSE连接已建立
- **测试步骤**:
  1. 建立SSE连接
  2. 保持空闲状态15秒以上
- **预期结果**:
  - 每15秒收到心跳注释 `: heartbeat\n\n`
  - 连接不会被Nginx/代理断开

### TC-SSE-003: SSE历史事件回放
- **优先级**: P0
- **前置条件**: 备份正在运行中
- **测试步骤**:
  1. 触发备份
  2. 等待进度产生一些事件后
  3. 新打开一个SSE连接
- **预期结果**:
  - connected事件后，回放所有历史进度事件
  - 新客户端能看到完整的进度状态（而不是从0开始）

### TC-SSE-004: SSE接收备份各阶段事件
- **优先级**: P0
- **前置条件**: 备份已触发
- **测试步骤**:
  1. 建立SSE连接
  2. 触发全量备份
  3. 观察接收到的事件
- **预期结果**:
  - 按顺序收到阶段切换事件：scanning → hashing → deduplicating → uploading → finalizing → completed
  - progress事件包含phase, current, total, percent字段
  - file事件显示当前处理的文件路径和大小
  - log事件输出实时日志

### TC-SSE-005: SSE进度百分比计算验证
- **优先级**: P1
- **前置条件**: 备份运行中
- **测试步骤**:
  1. 触发备份
  2. 跟踪SSE进度事件的percent字段
- **预期结果**:
  - scanning阶段: 0%
  - hashing阶段: 0% → 25%
  - deduplicating阶段: 25% → 30%
  - uploading阶段: 30% → 95%
  - finalizing阶段: 95% → 100%
  - completed时: 100%
  - percent单调递增，不回退

### TC-SSE-006: SSE连接断开后客户端重连
- **优先级**: P1
- **前置条件**: 备份运行中，SSE已连接
- **测试步骤**:
  1. 建立SSE连接
  2. 模拟网络断开（断开客户端网络）
  3. 恢复网络
- **预期结果**:
  - 客户端自动重连（EventSource默认行为）
  - 重连后通过历史回放恢复进度状态

### TC-SSE-007: SSE备份取消事件
- **优先级**: P1
- **前置条件**: 备份运行中
- **测试步骤**:
  1. 建立SSE连接
  2. 触发备份
  3. 取消备份
- **预期结果**:
  - SSE收到 type: "cancelled" 事件
  - 之后不再有进度事件

### TC-SSE-008: SSE备份失败事件
- **优先级**: P1
- **前置条件**: 制造备份失败场景（如OSS配置错误）
- **测试步骤**:
  1. 配置错误的OSS密钥
  2. 建立SSE连接
  3. 触发备份
- **预期结果**:
  - SSE收到 type: "failed" 事件
  - 事件包含error信息

### TC-SSE-009: SSE无WriteTimeout限制
- **优先级**: P2
- **前置条件**: 长时备份运行中
- **测试步骤**:
  1. 触发一个需要长时间运行的备份
  2. 保持SSE连接超过60秒（HTTP默认WriteTimeout）
- **预期结果**:
  - SSE连接不被服务器断开
  - 持续收到心跳和进度事件

---

## 4. 内容管理 - 备份目录模块 (Directories)

### TC-DIR-001: 获取备份目录列表（空）
- **优先级**: P1
- **前置条件**: 系统初始化完成，无备份目录配置
- **测试步骤**:
  1. 调用 GET /api/content/directories
- **预期结果**:
  - HTTP 200
  - success: true
  - data 为空数组 []

### TC-DIR-002: 添加备份目录 - 成功
- **优先级**: P0
- **前置条件**: 准备一个存在的绝对路径目录
- **测试步骤**:
  1. 创建测试目录 /tmp/test-backup-dir
  2. 调用 POST /api/content/directories，body:
     ```json
     {
       "path": "/tmp/test-backup-dir",
       "recursive": true,
       "enabled": true,
       "description": "测试目录"
     }
     ```
- **预期结果**:
  - HTTP 201 Created
  - data.id > 0
  - data.path 正确
  - data.recursive: true
  - data.enabled: true
  - 目录列表中能看到新添加的目录

### TC-DIR-003: 添加备份目录 - path为空
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/directories，body: {"path": ""}
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "path is required"

### TC-DIR-004: 添加备份目录 - 请求体格式错误
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/directories，body: "invalid"
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "invalid request body"

### TC-DIR-005: 更新备份目录 - 部分更新（PATCH语义）
- **优先级**: P0
- **前置条件**: 已存在一个备份目录
- **测试步骤**:
  1. 获取目录ID
  2. 调用 PATCH /api/content/directories/{id}，body: {"enabled": false}
- **预期结果**:
  - HTTP 200
  - 未提供的字段（path, recursive, description）保持原值
  - enabled 更新为 false
  - 符合PATCH部分更新语义（不覆盖未提供字段）

### TC-DIR-006: 更新备份目录 - 修改所有字段
- **优先级**: P1
- **前置条件**: 已存在一个备份目录
- **测试步骤**:
  1. 调用 PATCH /api/content/directories/{id}，body:
     ```json
     {
       "path": "/new/path",
       "recursive": false,
       "enabled": true,
       "description": "更新后的描述"
     }
     ```
- **预期结果**:
  - HTTP 200
  - 所有字段更新为新值

### TC-DIR-007: 更新备份目录 - 更新后path为空
- **优先级**: P2
- **前置条件**: 已存在一个备份目录
- **测试步骤**:
  1. 调用 PATCH /api/content/directories/{id}，body: {"path": ""}
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "path must not be empty"

### TC-DIR-008: 更新备份目录 - ID不存在
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PATCH /api/content/directories/99999，body: {"enabled": false}
- **预期结果**:
  - HTTP 404 Not Found
  - error: "directory not found"

### TC-DIR-009: 更新备份目录 - 无效ID格式
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PATCH /api/content/directories/abc
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "invalid directory ID"

### TC-DIR-010: 目录启用/禁用状态影响备份
- **优先级**: P0
- **前置条件**: 配置两个目录，一个启用一个禁用
- **测试步骤**:
  1. 添加目录A（enabled: true），放入一些文件
  2. 添加目录B（enabled: false），放入一些文件
  3. 触发备份
- **预期结果**:
  - 只备份目录A中的文件
  - 目录B中的文件被忽略

### TC-DIR-011: 非递归目录只备份直接子文件
- **优先级**: P1
- **前置条件**: 一个目录含子目录和文件
- **测试步骤**:
  1. 创建目录结构:
     - /tmp/non-recursive/file1.txt
     - /tmp/non-recursive/subdir/file2.txt
  2. 添加该目录，recursive: false
  3. 触发备份
- **预期结果**:
  - file1.txt 被备份
  - subdir/file2.txt 不被备份

---

## 5. 内容管理 - 排除规则模块 (Exclusions)

### TC-EXC-001: 获取排除规则列表（空）
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/content/exclusions
- **预期结果**:
  - HTTP 200
  - data 为空数组

### TC-EXC-002: 添加排除规则 - 扩展名类型
- **优先级**: P0
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body:
     ```json
     {
       "pattern": ".tmp",
       "rule_type": "extension",
       "enabled": true
     }
     ```
- **预期结果**:
  - HTTP 201 Created
  - data.id > 0
  - rule_type: "extension"

### TC-EXC-003: 添加排除规则 - 目录类型
- **优先级**: P0
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body:
     ```json
     {
       "pattern": "node_modules",
       "rule_type": "directory",
       "enabled": true
     }
     ```
- **预期结果**:
  - HTTP 201
  - rule_type: "directory"

### TC-EXC-004: 添加排除规则 - 模式类型
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body:
     ```json
     {
       "pattern": "*.log",
       "rule_type": "pattern",
       "enabled": true
     }
     ```
- **预期结果**:
  - HTTP 201
  - rule_type: "pattern"（默认值，未指定rule_type时也应为pattern）

### TC-EXC-005: 添加排除规则 - 大小超限类型
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body:
     ```json
     {
       "pattern": "1073741824",
       "rule_type": "size_exceed",
       "enabled": true
     }
     ```
- **预期结果**:
  - HTTP 201
  - rule_type: "size_exceed"
  - 备份时跳过大于1GB的文件

### TC-EXC-006: 添加排除规则 - pattern为空
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body: {"pattern": ""}
- **预期结果**:
  - HTTP 400
  - error: "pattern is required"

### TC-EXC-007: 添加排除规则 - 默认rule_type
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/content/exclusions，body: {"pattern": "*.bak"}
- **预期结果**:
  - HTTP 201
  - rule_type: "pattern"（未指定时默认）

### TC-EXC-008: 更新排除规则 - 部分更新
- **优先级**: P0
- **前置条件**: 已存在一个排除规则
- **测试步骤**:
  1. 调用 PUT /api/content/exclusions/{id}，body: {"enabled": false}
- **预期结果**:
  - HTTP 200
  - 未提供的字段保持原值
  - enabled: false

### TC-EXC-009: 更新排除规则 - ID不存在
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/content/exclusions/99999，body: {"pattern": "*.txt"}
- **预期结果**:
  - HTTP 404 Not Found
  - error: "exclusion rule not found"

### TC-EXC-010: 更新排除规则 - 无效ID格式
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/content/exclusions/invalid-id
- **预期结果**:
  - HTTP 400
  - error: "invalid exclusion ID"

### TC-EXC-011: 更新排除规则 - pattern清空
- **优先级**: P2
- **前置条件**: 已存在排除规则
- **测试步骤**:
  1. 调用 PUT /api/content/exclusions/{id}，body: {"pattern": ""}
- **预期结果**:
  - HTTP 400
  - error: "pattern must not be empty"

### TC-EXC-012: 删除排除规则 - 成功
- **优先级**: P1
- **前置条件**: 已存在排除规则
- **测试步骤**:
  1. 调用 DELETE /api/content/exclusions/{id}
- **预期结果**:
  - HTTP 200
  - data.status: "deleted"
  - 后续GET列表中不再有该规则

### TC-EXC-013: 删除排除规则 - ID不存在
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 DELETE /api/content/exclusions/99999
- **预期结果**:
  - HTTP 404 Not Found

### TC-EXC-014: 排除规则实际生效验证
- **优先级**: P0
- **前置条件**: 配置排除规则
- **测试步骤**:
  1. 在备份目录中创建：a.txt, b.tmp, c.log
  2. 添加排除规则：.tmp 扩展名, *.log 模式
  3. 触发全量备份
- **预期结果**:
  - a.txt 被备份
  - b.tmp 被排除，不备份
  - c.log 被排除，不备份
  - 备份记录的total_files = 1（仅a.txt）

### TC-EXC-015: 禁用排除规则不生效
- **优先级**: P1
- **前置条件**: 已添加一个enabled=false的排除规则
- **测试步骤**:
  1. 添加规则：*.tmp，enabled: false
  2. 备份目录中放test.tmp文件
  3. 触发备份
- **预期结果**:
  - test.tmp 文件被正常备份（禁用的规则不生效）

---

## 6. 文件系统浏览模块 (FS Browse)

### TC-FS-001: 浏览根目录
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/fs/browse?path=/
- **预期结果**:
  - HTTP 200
  - data.path: "/"
  - data.parent_path: "" (或"/")
  - data.entries 为数组，每个entry包含name, path, is_dir, size, mod_time
  - is_dir: true 的为目录项

### TC-FS-002: 浏览子目录
- **优先级**: P1
- **前置条件**: /tmp目录存在
- **测试步骤**:
  1. 调用 GET /api/fs/browse?path=/tmp
- **预期结果**:
  - HTTP 200
  - data.path: "/tmp"
  - data.parent_path: "/"
  - entries 显示/tmp下的文件和目录

### TC-FS-003: 浏览备份目录 - 备份状态标识
- **优先级**: P0
- **前置条件**: 已对某目录完成备份
- **测试步骤**:
  1. 先对/tmp/testdir完成全量备份
  2. 调用 GET /api/fs/browse?path=/tmp/testdir
- **预期结果**:
  - entries中已备份的文件 in_backup: true
  - 未备份的文件 in_backup: false
  - 目录可能显示 partial_backup: true/false

### TC-FS-004: 浏览不存在的路径
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/fs/browse?path=/nonexistent/path/12345
- **预期结果**:
  - HTTP 400 或合适的错误码
  - 返回错误信息

### TC-FS-005: 浏览路径为文件时的行为
- **优先级**: P2
- **前置条件**: /tmp/test.txt存在且是文件
- **测试步骤**:
  1. 调用 GET /api/fs/browse?path=/tmp/test.txt
- **预期结果**:
  - 返回错误或合理提示（只能浏览目录）

### TC-FS-006: 路径参数缺失
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/fs/browse（不带path参数）
- **预期结果**:
  - 浏览默认目录（如/或用户home）或返回错误

---

## 7. 策略配置 - 调度模块 (Schedule)

### TC-SCHED-001: 获取调度配置 - 默认值
- **优先级**: P1
- **前置条件**: 未修改过调度配置（使用config.yaml默认）
- **测试步骤**:
  1. 调用 GET /api/strategy/schedule
- **预期结果**:
  - HTTP 200
  - 返回默认配置（enabled, cron_expr, timezone来自config.yaml）
  - 例如cron_expr可能是"0 2 * * *"（每天凌晨2点）

### TC-SCHED-002: 更新调度配置 - 启用调度
- **优先级**: P0
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body:
     ```json
     {
       "enabled": true,
       "cron_expr": "0 3 * * *",
       "timezone": "Asia/Shanghai"
     }
     ```
- **预期结果**:
  - HTTP 200
  - 配置保存成功
  - 调度器启动
  - 按cron表达式定时触发备份

### TC-SCHED-003: 更新调度配置 - 禁用调度
- **优先级**: P1
- **前置条件**: 调度当前已启用
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body:
     ```json
     {
       "enabled": false,
       "cron_expr": "0 3 * * *",
       "timezone": "Asia/Shanghai"
     }
     ```
- **预期结果**:
  - HTTP 200
  - 调度器停止
  - 不会再定时触发备份

### TC-SCHED-004: 更新调度配置 - cron_expr为空
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body:
     ```json
     {
       "enabled": true,
       "cron_expr": "",
       "timezone": "Asia/Shanghai"
     }
     ```
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "cron_expr is required"

### TC-SCHED-005: 更新调度配置 - 无效cron表达式
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body:
     ```json
     {
       "enabled": true,
       "cron_expr": "invalid cron",
       "timezone": "Asia/Shanghai"
     }
     ```
- **预期结果**:
  - HTTP 500 或合适的错误
  - 调度不会启动
  - 返回错误信息

### TC-SCHED-006: 更新调度配置 - 请求体格式错误
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body: "bad json"
- **预期结果**:
  - HTTP 400
  - error: "invalid request body"

### TC-SCHED-007: 调度器运行中更新cron表达式
- **优先级**: P1
- **前置条件**: 调度已启用运行中
- **测试步骤**:
  1. 先用"0 2 * * *"启用调度
  2. 更新为"0 4 * * *"
- **预期结果**:
  - HTTP 200
  - 调度器不重启，直接更新cron表达式
  - 下次执行时间变为凌晨4点

### TC-SCHED-008: 启用调度为空timezone
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/schedule，body:
     ```json
     {
       "enabled": true,
       "cron_expr": "0 2 * * *",
       "timezone": ""
     }
     ```
- **预期结果**:
  - HTTP 200
  - 使用默认时区或UTC

### TC-SCHED-009: 禁用后重新启用调度
- **优先级**: P2
- **前置条件**: 调度之前已禁用
- **测试步骤**:
  1. 禁用调度（enabled: false）
  2. 等待一会儿
  3. 重新启用（enabled: true，相同cron_expr）
- **预期结果**:
  - 调度器重新启动
  - 按cron正常调度

---

## 8. 策略配置 - 压缩模块 (Compression)

### TC-COMP-001: 获取压缩配置 - 默认值
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/strategy/compression
- **预期结果**:
  - HTTP 200
  - 返回config.yaml中的默认压缩配置

### TC-COMP-002: 更新压缩配置 - 启用压缩
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/compression，body:
     ```json
     {
       "enabled": true,
       "algorithm": "zstd",
       "level": 3,
       "skip_types": [".zip", ".gz", ".7z"]
     }
     ```
- **预期结果**:
  - HTTP 200
  - 配置保存成功
  - 后续备份使用zstd压缩（已压缩格式跳过）

### TC-COMP-003: 更新压缩配置 - algorithm为空
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/compression，body:
     ```json
     {
       "enabled": true,
       "algorithm": "",
       "level": 3
     }
     ```
- **预期结果**:
  - HTTP 400
  - error: "algorithm is required"

### TC-COMP-004: 压缩级别边界值
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 分别测试level=1, level=22（zstd级别范围）
- **预期结果**:
  - HTTP 200
  - 级别保存成功
  - （注：业务逻辑应验证level范围，若无效应返回错误）

### TC-COMP-005: 压缩功能生效验证
- **优先级**: P0
- **前置条件**: 启用压缩，使用可压缩的文本文件
- **测试步骤**:
  1. 创建一个大文本文件（重复内容，高压缩率）
  2. 启用压缩，level=10
  3. 触发全量备份
- **预期结果**:
  - 备份记录 compress_saved > 0
  - backup_files中stored_size < original_size
  - 文件在OSS上存储的是压缩后大小

### TC-COMP-006: skip_types压缩跳过验证
- **优先级**: P1
- **前置条件**: 配置skip_types
- **测试步骤**:
  1. 创建test.zip文件（已压缩格式）
  2. 配置skip_types: [".zip"]
  3. 启用压缩
  4. 备份
- **预期结果**:
  - test.zip 不被压缩（compress_type为none）
  - 其他可压缩文件被正常压缩

### TC-COMP-007: 禁用压缩不压缩文件
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 设置 compression.enabled: false
  2. 备份文本文件
- **预期结果**:
  - compress_saved: 0
  - stored_size = original_size
  - compress_type: "none"

---

## 9. 策略配置 - 上传模块 (Upload)

### TC-UPLOAD-001: 获取上传配置 - 默认值
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/strategy/upload
- **预期结果**:
  - HTTP 200
  - 返回默认上传配置（storage_class, concurrency, chunk_size_mb等）

### TC-UPLOAD-002: 更新上传配置
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/upload，body:
     ```json
     {
       "storage_class": "Standard",
       "max_concurrency": 4,
       "chunk_size_mb": 8,
       "retry_count": 3,
       "retry_delay_sec": 5,
       "oss_quota_bytes": 107374182400
     }
     ```
- **预期结果**:
  - HTTP 200
  - 所有配置保存成功

### TC-UPLOAD-003: 并发数边界值测试
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 测试max_concurrency=1（最小）
  2. 测试max_concurrency=10（最大）
  3. 测试max_concurrency=0或<1
  4. 测试max_concurrency=20或>10
- **预期结果**:
  - 1-10范围内正常保存
  - 超出范围应返回错误或被修正（根据业务验证逻辑）

### TC-UPLOAD-004: 存储类型选择
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 分别测试storage_class为"Standard", "IA", "Archive", "ColdArchive"
- **预期结果**:
  - HTTP 200
  - 所有合法存储类型保存成功
  - 后续上传使用对应存储类型

### TC-UPLOAD-005: OSS配额显示在仪表盘
- **优先级**: P2
- **前置条件**: 设置了oss_quota_bytes
- **测试步骤**:
  1. 设置oss_quota_bytes=100GB
  2. 调用 GET /api/dashboard/stats
- **预期结果**:
  - oss_quota_bytes = 107374182400
  - 仪表盘容量仪表正确计算使用百分比

---

## 10. 策略配置 - 保留策略模块 (Retention)

### TC-RET-001: 获取保留策略 - 默认值
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/strategy/retention
- **预期结果**:
  - HTTP 200
  - 返回默认保留配置

### TC-RET-002: 更新保留策略
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/retention，body:
     ```json
     {
       "version_keep_count": 3,
       "orphan_grace_days": 180,
       "full_reset_interval": 1,
       "keep_deleted_days": 30
     }
     ```
- **预期结果**:
  - HTTP 200
  - 配置保存成功

### TC-RET-003: 孤儿宽限期影响GC
- **优先级**: P0
- **前置条件**: 了解GC逻辑
- **测试步骤**:
  1. 设置orphan_grace_days=0（立即清理）
  2. 创建孤儿hash（ref_count=0且orphaned_at已设置）
  3. 触发GC
- **预期结果**:
  - 孤儿hash_index被删除
  - 对应的OSS对象被删除（注意：需确认GC是否删除OSS对象）

### TC-RET-004: 全量重置间隔影响auto类型判断
- **优先级**: P0
- **前置条件**: 无
- **测试步骤**:
  1. 设置full_reset_interval=1（月）
  2. 完成一次全量备份
  3. 模拟时间过了1个月
  4. 触发type=auto备份
- **预期结果**:
  - auto判断执行全量备份而非增量

### TC-RET-005: 删除文件保留期
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 设置keep_deleted_days=7
  2. 备份一个文件后将其删除
  3. 执行增量备份
- **预期结果**:
  - files表中该文件标记为deleted
  - deleted_at时间被记录
  - GC在保留期过后才清理相关数据

---

## 11. 策略配置 - 加密模块 (Encryption)

### TC-ENC-001: 获取加密配置 - 默认值
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/strategy/encryption
- **预期结果**:
  - HTTP 200
  - 返回默认加密配置

### TC-ENC-002: 更新加密配置
- **优先级**: P1
- **前置条件**: 准备好AES密钥文件
- **测试步骤**:
  1. 调用 PUT /api/strategy/encryption，body:
     ```json
     {
       "algorithm": "AES-256-GCM",
       "key_file_path": "/path/to/key.file"
     }
     ```
- **预期结果**:
  - HTTP 200
  - 配置保存成功

### TC-ENC-003: algorithm为空
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 PUT /api/strategy/encryption，body:
     ```json
     {
       "algorithm": "",
       "key_file_path": "/path/to/key"
     }
     ```
- **预期结果**:
  - HTTP 400
  - error: "algorithm is required"

### TC-ENC-004: 加密功能生效验证
- **优先级**: P0
- **前置条件**: 启用加密，配置正确密钥文件
- **测试步骤**:
  1. 启用AES-256-GCM加密
  2. 备份一个文本文件
  3. 通过rclone直接查看OSS上的对象
- **预期结果**:
  - OSS上存储的是密文（不是明文）
  - backup_files表中记录encrypted_iv和auth_tag
  - 内容不可直接读

### TC-ENC-005: 加密文件恢复后明文正确
- **优先级**: P0
- **前置条件**: 加密备份已完成
- **测试步骤**:
  1. 加密备份一个已知内容的文件
  2. 执行恢复
  3. 比较恢复文件与原文件内容
- **预期结果**:
  - 恢复后的文件内容与原文件完全一致
  - 解密过程正确，无数据损坏

### TC-ENC-006: 密钥文件不存在
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 配置key_file_path指向不存在的文件
  2. 触发备份
- **预期结果**:
  - 备份失败，返回错误信息
  - 不上传损坏或未加密的数据

---

## 12. 日志模块 (Logs)

### TC-LOG-001: 获取日志列表（默认）
- **优先级**: P1
- **前置条件**: 系统有日志记录
- **测试步骤**:
  1. 调用 GET /api/logs
- **预期结果**:
  - HTTP 200
  - page: 1
  - page_size: 50（默认）
  - data按时间倒序排列
  - 每条日志包含id, level, message, created_at

### TC-LOG-002: 获取日志 - 自定义分页
- **优先级**: P1
- **前置条件**: 有足够日志
- **测试步骤**:
  1. 调用 GET /api/logs?page=1&page_size=10
- **预期结果**:
  - HTTP 200
  - page: 1
  - page_size: 10（注意：page_size应也有200上限）
  - data.length=10

### TC-LOG-003: 按日志级别过滤
- **优先级**: P0
- **前置条件**: 各级别日志都有
- **测试步骤**:
  1. 调用 GET /api/logs?level=error
  2. 调用 GET /api/logs?level=warn
  3. 调用 GET /api/logs?level=info
- **预期结果**:
  - 只返回对应级别的日志
  - 无其他级别日志混入

### TC-LOG-004: 按backup_id过滤
- **优先级**: P0
- **前置条件**: 已完成备份，有备份相关日志
- **测试步骤**:
  1. 获取一个已完成备份的ID
  2. 调用 GET /api/logs?backup_id={id}
- **预期结果**:
  - 只返回该备份相关的日志
  - 其他备份的日志不出现

### TC-LOG-005: 关键词搜索
- **优先级**: P1
- **前置条件**: 有包含特定关键词的日志
- **测试步骤**:
  1. 触发一次备份，产生包含"backup started"的日志
  2. 调用 GET /api/logs?search=backup started
- **预期结果**:
  - 返回包含该关键词的日志记录
  - 不相关日志不出现

### TC-LOG-006: 按时间范围过滤
- **优先级**: P1
- **前置条件**: 不同时间有日志
- **测试步骤**:
  1. 调用 GET /api/logs?start_time=2024-01-01T00:00:00Z&end_time=2024-12-31T23:59:59Z
- **预期结果**:
  - 只返回该时间范围内的日志
  - 范围外的日志不出现

### TC-LOG-007: 时间范围参数格式错误
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/logs?start_time=invalid-time
- **预期结果**:
  - 忽略无效时间参数或返回错误（不应崩溃）

### TC-LOG-008: 获取单条日志详情
- **优先级**: P2
- **前置条件**: 存在日志记录
- **测试步骤**:
  1. 获取一个日志ID
  2. 调用 GET /api/logs/{id}
- **预期结果**:
  - HTTP 200
  - 返回完整日志记录，包含detail字段

### TC-LOG-009: 获取单条日志 - ID不存在
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/logs/99999
- **预期结果**:
  - HTTP 404 Not Found
  - error: "log entry not found"

### TC-LOG-010: 获取单条日志 - 无效ID格式
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/logs/abc
- **预期结果**:
  - HTTP 400 Bad Request

### TC-LOG-011: 组合过滤条件
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/logs?level=error&backup_id=1&search=upload&page=1&page_size=20
- **预期结果**:
  - 所有过滤条件同时生效
  - 返回满足所有条件的日志

### TC-LOG-012: page_size上限验证
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 GET /api/logs?page_size=500
- **预期结果**:
  - page_size被限制为200（与其他列表一致，防止内存压力）

---

## 13. 恢复模块 (Restore)

### TC-RESTORE-001: 按路径恢复文件 - 成功
- **优先级**: P0
- **前置条件**: 已有备份完成，输出目录存在
- **测试步骤**:
  1. 创建空输出目录 /tmp/restore-output
  2. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/path/to/backed-up/file.txt"],
       "output_dir": "/tmp/restore-output"
     }
     ```
- **预期结果**:
  - HTTP 200
  - restored_files: 1
  - total_files: 1
  - failed_files: []
  - /tmp/restore-output下文件被正确恢复
  - 文件内容与原文件一致

### TC-RESTORE-002: 按模式匹配恢复
- **优先级**: P1
- **前置条件**: 备份目录中有多个文件
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "pattern": "*.txt",
       "output_dir": "/tmp/restore-output"
     }
     ```
- **预期结果**:
  - 所有.txt文件被恢复
  - 其他类型文件不恢复

### TC-RESTORE-003: 恢复到指定备份版本
- **优先级**: P1
- **前置条件**: 有多个历史备份版本
- **测试步骤**:
  1. 获取一个历史备份ID
  2. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/test/file.txt"],
       "backup_id": 1,
       "output_dir": "/tmp/restore-output"
     }
     ```
- **预期结果**:
  - 从指定备份版本恢复
  - 恢复该备份时点的文件内容

### TC-RESTORE-004: 恢复 - paths和pattern都未提供
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "output_dir": "/tmp/restore-output"
     }
     ```
- **预期结果**:
  - HTTP 400 Bad Request
  - error: "paths or pattern is required"

### TC-RESTORE-005: 恢复 - output_dir未提供
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/test/file.txt"]
     }
     ```
- **预期结果**:
  - HTTP 400
  - error: "output_dir is required"

### TC-RESTORE-006: 恢复 - output_dir不存在
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/test/file.txt"],
       "output_dir": "/nonexistent/dir/12345"
     }
     ```
- **预期结果**:
  - HTTP 400
  - error: "output_dir does not exist"

### TC-RESTORE-007: 恢复 - output_dir是文件不是目录
- **优先级**: P2
- **前置条件**: /tmp/test-file.txt是普通文件
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/test/file.txt"],
       "output_dir": "/tmp/test-file.txt"
     }
     ```
- **预期结果**:
  - HTTP 400
  - error: "output_dir must be a directory"

### TC-RESTORE-008: 恢复 - 请求体格式错误
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/restore，body: "invalid"
- **预期结果**:
  - HTTP 400
  - error: "invalid request body"

### TC-RESTORE-009: 加密文件恢复正确性
- **优先级**: P0
- **前置条件**: 备份时启用了加密
- **测试步骤**:
  1. 加密备份一个文件
  2. 恢复该文件
  3. 比较内容
- **预期结果**:
  - 恢复后文件内容与原文件完全一致
  - 解密过程正确，无数据损坏

### TC-RESTORE-010: 压缩文件恢复正确性
- **优先级**: P0
- **前置条件**: 备份时启用了压缩
- **测试步骤**:
  1. 压缩备份一个文件
  2. 恢复该文件
  3. 比较内容
- **预期结果**:
  - 恢复后文件内容与原文件完全一致
  - 解压过程正确

### TC-RESTORE-011: 加密+压缩文件恢复正确性
- **优先级**: P0
- **前置条件**: 同时启用加密和压缩
- **测试步骤**:
  1. 同时启用加密+压缩备份文件
  2. 恢复
  3. 比较内容
- **预期结果**:
  - 文件内容完全正确（先解密再解压顺序正确）

### TC-RESTORE-012: 恢复不存在的文件路径
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/restore，body:
     ```json
     {
       "paths": ["/never/backed/up/file.txt"],
       "output_dir": "/tmp/restore-output"
     }
     ```
- **预期结果**:
  - restored_files: 0
  - failed_files包含该路径
  - 返回部分失败或整体错误

### TC-RESTORE-013: 目录结构保持验证
- **优先级**: P0
- **前置条件**: 备份了多层目录结构
- **测试步骤**:
  1. 备份目录结构: a/b/c/file.txt
  2. 恢复到/tmp/restore
- **预期结果**:
  - 恢复后路径为 /tmp/restore/a/b/c/file.txt
  - 目录结构完整保持
  - 不丢失子目录（不使用filepath.Base导致的问题）

### TC-RESTORE-014: ColdArchive加急恢复
- **优先级**: P2
- **前置条件**: 文件存储在ColdArchive
- **测试步骤**:
  1. 使用expedited: true恢复
- **预期结果**:
  - 使用加急解冻模式
  - 恢复成功（或正确处理解冻等待）

### TC-RESTORE-015: 客户端断开不中断恢复
- **优先级**: P1
- **前置条件**: 长时恢复操作
- **测试步骤**:
  1. 发起一个需要较长时间的恢复
  2. 在恢复过程中取消HTTP请求（客户端断开）
- **预期结果**:
  - 恢复继续在后台运行（使用独立context，4h超时）
  - 不随HTTP请求上下文取消而中断

---

## 14. 垃圾回收模块 (GC)

### TC-GC-001: 触发GC - 异步执行
- **优先级**: P0
- **前置条件**: 无备份运行中
- **测试步骤**:
  1. 调用 POST /api/gc
- **预期结果**:
  - HTTP 202 Accepted
  - data.status: "started"
  - GC在后台goroutine异步执行
  - 接口立即返回，不阻塞

### TC-GC-002: GC清理过期孤儿hash
- **优先级**: P0
- **前置条件**: 存在orphaned_at超过宽限期的孤儿hash
- **测试步骤**:
  1. 制造孤儿hash（ref_count=0，orphaned_at为200天前，宽限期180天）
  2. 触发GC
  3. 等待GC完成
- **预期结果**:
  - 过期的孤儿hash从hash_index中删除
  - 对应的OSS对象被删除
  - 日志记录GC完成信息

### TC-GC-003: GC不删除未过期孤儿
- **优先级**: P0
- **前置条件**: 孤儿刚产生（在宽限期内）
- **测试步骤**:
  1. 制造孤儿hash（ref_count=0，orphaned_at为今天，宽限期180天）
  2. 触发GC
- **预期结果**:
  - 该孤儿hash保留
  - 不被删除
  - 宽限期机制生效

### TC-GC-004: GC宽限期计算基于orphaned_at而非created_at
- **优先级**: P0
- **前置条件**: 旧hash但刚变成孤儿
- **测试步骤**:
  1. 一个hash创建于1年前，但昨天才变成孤儿（ref_count=0）
  2. orphan_grace_days=180
  3. 触发GC
- **预期结果**:
  - hash不被删除（orphaned_at昨天，还在180天宽限期内）
  - 不按created_at计算宽限期

### TC-GC-005: GC执行前检查运行中备份
- **优先级**: P0
- **前置条件**: 备份正在运行
- **测试步骤**:
  1. 触发全量备份
  2. 备份运行中立即调用 POST /api/gc
- **预期结果**:
  - GC检查到备份运行中，不执行或等待/返回错误
  - 不与备份并发导致竞态
  - （验证实际并发安全行为）

### TC-GC-006: GC清理deleted文件超过保留期的数据
- **优先级**: P1
- **前置条件**: 有deleted状态文件超过keep_deleted_days
- **测试步骤**:
  1. 文件标记为deleted已超过保留期
  2. 触发GC
- **预期结果**:
  - 相关backup_files记录被清理
  - 对应hash的ref_count递减
  - 若ref_count变为0则标记为孤儿

### TC-GC-007: GC日志记录
- **优先级**: P2
- **前置条件**: GC执行完成
- **测试步骤**:
  1. 触发GC并等待完成
  2. 查询日志
- **预期结果**:
  - 成功时日志级别info，消息"garbage collection completed"
  - 失败时日志级别error，消息"garbage collection failed"

### TC-GC-008: GC使用独立context不被HTTP超时中断
- **优先级**: P1
- **前置条件**: 大量对象需清理
- **测试步骤**:
  1. 制造大量孤儿对象需要清理
  2. 触发GC
- **预期结果**:
  - GC使用30分钟独立context
  - 不受HTTP WriteTimeout(60s)限制
  - 能完整执行完成

---

## 15. 数据对账模块 (Reconcile)

### TC-REC-001: DryRun检查 - 无问题时返回空报告
- **优先级**: P0
- **前置条件**: 刚完成全量备份，数据一致
- **测试步骤**:
  1. 完成全量备份
  2. 调用 POST /api/reconcile?dry_run=true
- **预期结果**:
  - HTTP 200
  - dry_run: true
  - 所有问题列表为空
  - applied_fixes: []（DryRun不修复）
  - errors: []
  - duration_ms正常

### TC-REC-002: DryRun检查 - 检测悬空hash索引（ref=0）
- **优先级**: P0
- **前置条件**: 制造悬空索引
- **测试步骤**:
  1. 手动在backup_files中删除一条记录（但hash_index保留，ref_count变成0）
  2. 调用 POST /api/reconcile?dry_run=true
- **预期结果**:
  - dangling_hash_indexes_ref_zero 包含该hash
  - 数据不被修改（DryRun只读）

### TC-REC-003: DryRun检查 - 检测ref_count不匹配
- **优先级**: P0
- **前置条件**: 制造ref_count漂移
- **测试步骤**:
  1. 手动修改hash_index的ref_count使其与实际active引用数不符
  2. 调用DryRun
- **预期结果**:
  - ref_count_mismatches包含该hash
  - stored_in_db和actual_active显示正确和错误值

### TC-REC-004: DryRun检查 - 检测孤立backup_files
- **优先级**: P0
- **前置条件**: 制造孤立记录
- **测试步骤**:
  1. 手动删除一个files表中的记录，但保留backup_files记录
  2. 调用DryRun
- **预期结果**:
  - orphan_backup_files包含该记录

### TC-REC-005: DryRun检查 - 检测备份状态异常
- **优先级**: P1
- **前置条件**: 制造异常状态
- **测试步骤**:
  1. 一个failed状态的备份但有backup_files记录
  2. 一个completed状态的备份但没有backup_files记录
  3. 调用DryRun
- **预期结果**:
  - failed_backups_with_files包含前者
  - completed_backups_no_files包含后者

### TC-REC-006: DryRun检查 - 检测OSS孤儿对象
- **优先级**: P1
- **前置条件**: OSS上有hash_index中不存在的对象
- **测试步骤**:
  1. 直接通过rclone上传一个文件到OSS data/路径下，但不在DB中记录
  2. 调用DryRun
- **预期结果**:
  - oss_only_orphans包含该对象key
  - 注意：DryRun只报告，不删除OSS对象

### TC-REC-007: 执行修复（非DryRun）- 修复ref_count
- **优先级**: P0
- **前置条件**: 存在ref_count_mismatches
- **测试步骤**:
  1. 制造ref_count漂移
  2. 调用 POST /api/reconcile?dry_run=false
- **预期结果**:
  - HTTP 200
  - dry_run: false
  - applied_fixes包含修复记录
  - ref_count_mismatches为空（已修复）
  - DB中ref_count被修正为正确值
  - 修复操作使用事务包裹，原子性

### TC-REC-008: 执行修复 - 删除悬空索引
- **优先级**: P0
- **前置条件**: dangling_hash_indexes_ref_zero存在
- **测试步骤**:
  1. 制造悬空索引（ref=0且无backup_files引用）
  2. 执行修复（dry_run=false）
- **预期结果**:
  - 悬空hash_index记录被删除
  - 注意：OSS对象是否删除？根据设计OSS孤儿仅告警不自动删除，需确认

### TC-REC-009: 执行修复 - 清理孤立backup_files
- **优先级**: P0
- **前置条件**: orphan_backup_files存在
- **测试步骤**:
  1. 制造孤立backup_files
  2. 执行修复
- **预期结果**:
  - 孤立backup_files记录被删除
  - 对应hash的ref_count正确递减

### TC-REC-010: 执行修复 - 修正备份状态
- **优先级**: P1
- **前置条件**: 备份状态异常
- **测试步骤**:
  1. completed备份无文件、failed备份有文件
  2. 执行修复
- **预期结果**:
  - 异常备份状态被修正
  - applied_fixes记录修复动作

### TC-REC-011: 备份运行中对账返回409冲突
- **优先级**: P0
- **前置条件**: 备份正在运行
- **测试步骤**:
  1. 触发全量备份
  2. 备份运行中调用 POST /api/reconcile?dry_run=true
- **预期结果**:
  - HTTP 409 Conflict
  - error信息提示备份正在运行
  - 不执行对账（与备份互斥）

### TC-REC-012: dry_run参数无效值
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用 POST /api/reconcile?dry_run=invalid
- **预期结果**:
  - HTTP 400
  - error提示dry_run参数需为true/false

### TC-REC-013: 对账中途出错返回部分报告
- **优先级**: P1
- **前置条件**: 制造对账可能出错的场景（如OSS不可访问）
- **测试步骤**:
  1. 断开OSS网络或配置错误密钥
  2. 调用对账
- **预期结果**:
  - 即使有错误也返回partial report
  - HTTP 500但响应中包含已发现的问题在data字段
  - errors数组包含错误信息
  - 不会因为部分失败丢失所有检查结果

### TC-REC-014: 对账日志记录
- **优先级**: P2
- **前置条件**: 对账完成
- **测试步骤**:
  1. 执行对账（DryRun或修复）
  2. 查询日志
- **预期结果**:
  - 日志记录对账完成信息
  - 包含各类问题的数量统计

### TC-REC-015: 对账使用独立context
- **优先级**: P1
- **前置条件**: 大量对象需要列举检查
- **测试步骤**:
  1. OSS中有大量对象
  2. 执行对账
- **预期结果**:
  - 对账使用30分钟独立context
  - 不受HTTP WriteTimeout限制
  - 能完整完成

### TC-REC-016: 合成hash使用INSERT OR IGNORE（重复reconcile）
- **优先级**: P1
- **前置条件**: 重复执行对账修复
- **测试步骤**:
  1. 连续多次执行对账修复（dry_run=false）
- **预期结果**:
  - 不会因重复插入导致ref_count无限增长
  - 使用INSERT OR IGNORE处理合成hash
  - 多次执行后结果一致（幂等性）

### TC-REC-017: 检测backup_files有hash但OSS对象缺失
- **优先级**: P0
- **前置条件**: OSS上对象被手动删除
- **测试步骤**:
  1. 正常备份后，手动通过rclone删除OSS上的一个对象
  2. 执行对账
- **预期结果**:
  - backup_files_missing_hash_index_but_in_oss或对应分类包含该问题
  - 检测到DB有记录但OSS对象缺失的情况

### TC-REC-018: 修复操作事务原子性
- **优先级**: P0
- **前置条件**: 需要修复多项问题
- **测试步骤**:
  1. 制造多种不一致问题
  2. 在修复过程中模拟进程崩溃（可通过测试或代码review验证）
- **预期结果**:
  - 所有修复操作在事务中
  - 不会出现部分修复导致数据更不一致
  - 中途崩溃后DB保持一致性状态

---

## 16. 存储健康检查模块 (Storage Health)

### TC-HEALTH-001: 存储健康检查 - 正常
- **优先级**: P0
- **前置条件**: OSS配置正确，网络可达
- **测试步骤**:
  1. 调用 GET /api/storage/health
- **预期结果**:
  - HTTP 200
  - success: true
  - data.status: "ok"
  - data.latency_ms > 0（合理延迟）

### TC-HEALTH-002: 存储健康检查 - OSS不可达
- **优先级**: P1
- **前置条件**: OSS配置错误或网络断开
- **测试步骤**:
  1. 配置错误的OSS endpoint/密钥
  2. 或断开网络
  3. 调用 GET /api/storage/health
- **预期结果**:
  - HTTP 503 Service Unavailable
  - success: false
  - error包含"OSS unreachable"
  - data.latency_ms记录失败时的延迟

### TC-HEALTH-003: 健康检查延迟测量
- **优先级**: P2
- **前置条件**: 无
- **测试步骤**:
  1. 调用健康检查
  2. 观察latency_ms
- **预期结果**:
  - latency_ms是合理的数值（几ms到几百ms，取决于网络）
  - 不出现负数或异常值

---

## 17. 去重模块 (Dedup) - 业务逻辑验证

### TC-DEDUP-001: 相同内容文件去重（跨目录/位置）
- **优先级**: P0
- **前置条件**: 两个文件内容完全相同
- **测试步骤**:
  1. 在备份目录中创建两个内容完全相同但路径不同的文件
  2. 触发全量备份
- **预期结果**:
  - unique_hash_count = 1（相同内容只存一个hash）
  - skipped_dedup = 1（第二个文件通过去重跳过上传）
  - OSS上只有一个对象
  - 两个backup_files记录指向同一个storage_key
  - hash_index的ref_count = 2

### TC-DEDUP-002: OSS存在性检查fail-close策略
- **优先级**: P0
- **前置条件**: OSS检查失败场景
- **测试步骤**:
  1. 模拟OSS存在性检查返回错误（如网络问题、权限问题）
  2. 备份文件
- **预期结果**:
  - 检查失败视为对象缺失（fail-close）
  - 重新上传文件
  - 不因为检查错误而错误跳过上传（避免数据丢失）

### TC-DEDUP-003: OSS对象确实存在时跳过上传
- **优先级**: P0
- **前置条件**: hash对应对象已在OSS
- **测试步骤**:
  1. 备份一个文件A
  2. 在另一位置创建相同内容文件B
  3. 再次备份
- **预期结果**:
  - B通过去重，skipped_dedup+1
  - 不重复上传
  - Exists方法使用lsjson --files-only可靠检测

### TC-DEDUP-004: ExistsBatch并发检查性能
- **优先级**: P1
- **前置条件**: 大量文件需要去重检查
- **测试步骤**:
  1. 准备大量已备份hash的文件
  2. 触发增量备份
- **预期结果**:
  - OSS存在性检查并行执行（并发度8）
  - 比串行检查明显更快
  - 检查结果正确

### TC-DEDUP-005: ExistsBatch worker取消时fail-close
- **优先级**: P0
- **前置条件**: 备份过程中context被取消
- **测试步骤**:
  1. 触发备份
  2. 备份正在去重检查时取消备份
- **预期结果**:
  - 未处理的pending key正确标记为检查失败
  - 不会隐式当作"存在"而跳过
  - fail-close策略一致
  - dedup不遗留隐式fail-open窗口

### TC-DEDUP-006: 重新上传时保留原始storageKey
- **优先级**: P0
- **前置条件**: OSS对象丢失需要重新上传
- **测试步骤**:
  1. 备份文件A
  2. 手动删除OSS上该对象
  3. 再次触发全量备份
- **预期结果**:
  - 检测到OSS对象缺失
  - 重新上传文件
  - 使用原始storageKey（不生成新key）
  - 历史backup_files记录仍然指向正确的key
  - 不会因为新key导致旧备份记录悬空

### TC-DEDUP-007: 同批次去重（pendingByHash缓存）
- **优先级**: P1
- **前置条件**: 同批次备份中有重复文件
- **测试步骤**:
  1. 同一批次备份中包含多个相同内容的新文件
- **预期结果**:
  - 第一个文件上传后，后续同批次文件通过pendingByHash缓存去重
  - 不需要查DB
  - 不重复上传

---

## 18. 存储模块 (Storage) 边界测试

### TC-STOR-001: List操作prefix尾部斜杠
- **优先级**: P1
- **前置条件**: 无
- **测试步骤**:
  1. 调用List OSS对象功能
- **预期结果**:
  - prefix自动添加尾部斜杠
  - 不会出现key拼接错误（如data/abc和data/abcd混淆）

### TC-STOR-002: GetStorageUsage限定data/前缀
- **优先级**: P1
- **前置条件**: OSS上有其他路径的对象
- **测试步骤**:
  1. 在OSS非data/路径下放一些对象
  2. 调用存储使用量统计
- **预期结果**:
  - 只统计data/前缀下的对象
  - 不统计整个bucket
  - oss_storage_used数值正确

### TC-STOR-003: 存储操作支持context取消
- **优先级**: P1
- **前置条件**: 长时存储操作
- **测试步骤**:
  1. 触发大文件上传
  2. 取消备份（context取消）
- **预期结果**:
  - 上传操作响应取消
  - 不继续传输
  - 资源正确释放

### TC-STOR-004: withRetry支持context取消
- **优先级**: P1
- **前置条件**: 重试过程中context取消
- **测试步骤**:
  1. 上传失败触发重试
  2. 在重试等待期间取消context
- **预期结果**:
  - 重试立即中止
  - 不继续重试
  - 返回context取消错误

### TC-STOR-005: rclone lsjson退出码3处理
- **优先级**: P1
- **前置条件**: 使用crypt remote后端
- **测试步骤**:
  1. 查询不存在的对象
- **预期结果**:
  - 正确检测退出码3
  - 识别为对象不存在
  - 不误判为存在
  - 错误关键词匹配: 'does not exist', 'directory not found', ' 404', ' 404"'

---

## 19. 系统启动与崩溃恢复

### TC-SYS-001: 服务正常启动
- **优先级**: P0
- **前置条件**: 配置正确
- **测试步骤**:
  1. 启动nas-backup服务
- **预期结果**:
  - 服务启动成功，监听8080端口
  - 数据库迁移执行
  - CleanupStaleRunning执行，清理残留状态
  - 调度器初始化
  - HTTP日志记录启动信息

### TC-SYS-002: 数据库迁移正确执行
- **优先级**: P0
- **前置条件**: 旧版本数据库
- **测试步骤**:
  1. 使用仅001_init.sql的旧DB
  2. 启动新版本服务（含002_add_orphaned_at.sql）
- **预期结果**:
  - 002迁移自动执行
  - hash_index表新增orphaned_at列
  - 迁移不报错

### TC-SYS-003: 崩溃后备份状态恢复
- **优先级**: P0
- **前置条件**: 模拟进程崩溃
- **测试步骤**:
  1. 备份运行中kill -9进程
  2. 重启服务
- **预期结果**:
  - 启动时CleanupStaleRunning将running/pending备份标记为failed
  - 系统可以接受新的备份请求
  - 不会卡在"已有备份运行中"状态

### TC-SYS-004: 取消备份时内存+DB双重检查
- **优先级**: P0
- **前置条件**: 各种场景
- **测试步骤**:
  1. 正常运行中取消 → 内存中找到并取消
  2. 进程重启后（内存无但DB有running）→ 清理DB状态
  3. 无运行备份时取消 → 返回404
- **预期结果**:
  - 三种场景都正确处理
  - 内存状态和DB状态双重检查
  - 不遗漏stale记录

### TC-SYS-005: SSE Nginx超时配置
- **优先级**: P2
- **前置条件**: 通过Nginx反向代理访问
- **测试步骤**:
  1. 配置nginx.conf
  2. 建立SSE连接并保持1小时以上
- **预期结果**:
  - Nginx配置1小时超时
  - 禁用缓冲
  - SSE长连接不被代理断开

### TC-SYS-006: 备份启动互斥锁+DB双重检查防并发
- **优先级**: P0
- **前置条件**: 尝试并发触发
- **测试步骤**:
  1. 同时发送两个备份触发请求
- **预期结果**:
  - 只有一个备份成功启动
  - 另一个返回409
  - 互斥锁内完成状态检查和记录创建
  - 无竞态条件

---

## 20. CORS与HTTP中间件

### TC-CORS-001: CORS头设置正确
- **优先级**: P2
- **前置条件**: 跨域请求
- **测试步骤**:
  1. 从前端不同源发起API请求
- **预期结果**:
  - Access-Control-Allow-Origin: *
  - Access-Control-Allow-Methods包含GET/POST/PUT/DELETE/OPTIONS
  - Access-Control-Allow-Headers正确

### TC-CORS-002: OPTIONS预检请求处理
- **优先级**: P2
- **测试步骤**:
  1. 发送OPTIONS预检请求
- **预期结果**:
  - HTTP 204 No Content
  - 正确的CORS头

### TC-LOGGING-001: HTTP请求日志级别
- **优先级**: P2
- **测试步骤**:
  1. 发一个正常请求(2xx) → 日志级别INFO
  2. 发一个客户端错误请求(4xx) → 日志级别WARN
  3. 发一个服务端错误请求(5xx) → 日志级别ERROR
- **预期结果**:
  - 日志级别符合硬约束要求
  - 4xx不打ERROR，5xx不打WARN

---

## 测试优先级说明

| 优先级 | 定义 | 要求 |
|--------|------|------|
| P0 | 核心功能、数据安全、一致性 | 必须100%通过，阻塞发布 |
| P1 | 重要功能、边界条件 | 应全部通过，特殊情况需评估 |
| P2 | 次要功能、异常处理、UI相关 | 尽量通过，可后续修复 |

## 测试前置条件建议

1. 准备干净的测试环境（空SQLite DB）
2. 准备独立的测试OSS Bucket（避免影响生产数据）
3. 准备各类测试文件：小文件、大文件、可压缩文件、已压缩文件、空文件、特殊字符文件名、长路径文件
4. 准备rclone配置（含crypt remote测试场景）
5. 具备手动修改DB数据的能力（模拟不一致场景）
