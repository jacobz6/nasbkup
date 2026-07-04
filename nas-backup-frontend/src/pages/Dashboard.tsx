import { useState, useCallback, useEffect, useRef } from 'react';
import {
  Play, FastForward, Square, Trash2, Files, HardDrive,
  FileCheck, ArrowDownToLine, RefreshCw, CheckCircle, Clock,
  Activity, Terminal, XCircle, AlertCircle, Info,
} from 'lucide-react';
import {
  dashboardApi, backupApi, gcApi, createProgressStream,
  type DashboardStats, type BackupRecord, type ProgressEvent, type ProgressPhase,
} from '@/utils/api';
import { StatusBadge } from '@/components/ui/StatusBadge';
import { GaugeChart } from '@/components/ui/GaugeChart';
import { StatCard } from '@/components/ui/StatCard';
import { DataTable, type Column } from '@/components/ui/DataTable';
import { Pagination } from '@/components/ui/Pagination';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { LoadingSkeleton, CardSkeleton } from '@/components/shared/LoadingSkeleton';
import { useAppStore } from '@/store/useAppStore';
import { formatFileSize, formatDateTime, formatRelativeTime } from '@/utils/format';
import { BACKUP_TYPE_MAP } from '@/utils/constants';
import { usePolling } from '@/hooks/usePolling';

const historyColumns: Column<BackupRecord>[] = [
  { key: 'id', header: 'ID', className: 'font-mono' },
  { key: 'type', header: '类型', render: (r) => {
    const t = BACKUP_TYPE_MAP[r.type];
    return <span className={t?.color || 'text-slate-400'}>{t?.label || r.type}</span>;
  }},
  { key: 'status', header: '状态', render: (r) => <StatusBadge status={r.status} pulse /> },
  { key: 'total_files', header: '文件数' },
  { key: 'total_size', header: '大小', render: (r) => formatFileSize(r.total_size) },
  { key: 'uploaded_size', header: '上传量', render: (r) => formatFileSize(r.uploaded_size) },
  { key: 'skipped_dedup', header: '去重跳过' },
  { key: 'started_at', header: '开始时间', render: (r) => formatDateTime(r.started_at) },
  { key: 'completed_at', header: '完成时间', render: (r) => formatDateTime(r.completed_at) },
];

const PHASE_COLORS: Record<ProgressPhase, string> = {
  scanning: 'text-sky-400',
  hashing: 'text-violet-400',
  deduplicating: 'text-amber-400',
  uploading: 'text-brand-400',
  finalizing: 'text-emerald-400',
  completed: 'text-emerald-400',
  failed: 'text-red-400',
  cancelled: 'text-slate-400',
};

const LOG_ICON: Record<string, typeof Info> = {
  debug: Info,
  info: Info,
  warn: AlertCircle,
  error: XCircle,
};

const LOG_COLOR: Record<string, string> = {
  debug: 'text-slate-500',
  info: 'text-slate-300',
  warn: 'text-amber-400',
  error: 'text-red-400',
};

interface LogEntry {
  id: number;
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
  detail?: string;
  timestamp: string;
}

interface ProgressState {
  isRunning: boolean;
  backupId: number | null;
  phase: ProgressPhase | null;
  phaseName: string;
  message: string;
  current: number;
  total: number;
  percent: number;
  currentFile: string;
}

const initialProgress: ProgressState = {
  isRunning: false,
  backupId: null,
  phase: null,
  phaseName: '',
  message: '',
  current: 0,
  total: 0,
  percent: 0,
  currentFile: '',
};

export function Dashboard() {
  const [stats, setStats] = useState<DashboardStats>({
    total_files: 0,
    total_size: 0,
    backed_up_files: 0,
    backed_up_size: 0,
    oss_storage_used: 0,
    saved_by_dedup: 0,
    saved_by_compress: 0,
    active_backup_running: false,
    last_backup_time: null,
    last_backup_status: '',
    next_backup_time: null,
  });
  const [history, setHistory] = useState<BackupRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; message: string; onConfirm: () => void }>({
    open: false, title: '', message: '', onConfirm: () => {},
  });
  const [progress, setProgress] = useState<ProgressState>(initialProgress);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const logCounter = useRef(0);
  const logsEndRef = useRef<HTMLDivElement>(null);
  const logsContainerRef = useRef<HTMLDivElement>(null);
  const addToast = useAppStore((s) => s.addToast);

  const fetchStats = useCallback(async () => {
    try {
      const res = await dashboardApi.getStats();
      if (res.success && res.data) {
        setStats(res.data);
        // 用函数式更新检查 prev.isRunning，避免闭包陷阱。
        if (!res.data.active_backup_running) {
          setProgress(prev => prev.isRunning ? { ...prev, isRunning: false } : prev);
        }
      }
    } catch (e) {
      console.error('Failed to fetch stats:', e);
    }
  }, []);

  const fetchHistory = useCallback(async (p = page) => {
    try {
      const res = await dashboardApi.getHistory(p, 10);
      if (res.data) {
        setHistory(res.data);
        setTotal(res.total);
      }
    } catch (e) {
      console.error('Failed to fetch history:', e);
    }
  }, [page]);

  const fetchAll = useCallback(async () => {
    try {
      await Promise.all([fetchStats(), fetchHistory()]);
    } catch (e) {
      console.error('Failed to fetch all:', e);
    } finally {
      setLoading(false);
    }
  }, [fetchStats, fetchHistory]);

  // 用 ref 持有最新的 fetchAll 引用，避免 SSE useEffect 依赖 fetchAll
  // 导致 progress.isRunning 变化时 SSE 连接断开重连。
  const fetchAllRef = useRef(fetchAll);
  fetchAllRef.current = fetchAll;

  usePolling(fetchAll, 5000, stats.active_backup_running || progress.isRunning);

  useEffect(() => { fetchAll(); }, []);

  // SSE 连接：仅在组件挂载时建立一次，避免因 fetchAll 重建而断开重连。
  useEffect(() => {
    const closeStream = createProgressStream((event: ProgressEvent) => {
      switch (event.type) {
        case 'connected':
          break;
        case 'phase': {
          const isEndPhase = event.phase === 'completed' || event.phase === 'failed' || event.phase === 'cancelled';
          setProgress(prev => ({
            ...prev,
            isRunning: !isEndPhase,
            backupId: event.backup_id || prev.backupId,
            phase: event.phase || null,
            phaseName: event.phase_name || '',
            message: event.message || '',
            percent: event.phase === 'completed' ? 100 : (event.percent ?? prev.percent),
            currentFile: '',
          }));
          if (isEndPhase) {
            setTimeout(() => fetchAllRef.current(), 1000);
          }
          break;
        }
        case 'progress':
          setProgress(prev => ({
            ...prev,
            isRunning: true,
            current: event.current ?? prev.current,
            total: event.total ?? prev.total,
            percent: event.percent ?? prev.percent,
          }));
          break;
        case 'file':
          if (event.file_path) {
            setProgress(prev => ({
              ...prev,
              isRunning: true,
              currentFile: event.file_path,
            }));
          }
          break;
        case 'log': {
          const entry: LogEntry = {
            id: ++logCounter.current,
            level: (event.level as LogEntry['level']) || 'info',
            message: event.message || '',
            detail: event.detail,
            timestamp: event.timestamp,
          };
          setLogs(prev => {
            const next = [...prev, entry];
            if (next.length > 500) return next.slice(-500);
            return next;
          });
          break;
        }
      }
    });
    return closeStream;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 智能滚动：仅当用户位于日志底部时自动滚动到最新日志。
  useEffect(() => {
    const container = logsContainerRef.current;
    if (!container) return;
    // 如果用户距底部超过 50px，说明在查看历史日志，不自动滚动。
    const distFromBottom = container.scrollHeight - container.scrollTop - container.clientHeight;
    if (distFromBottom < 50) {
      logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [logs]);

  const handleTrigger = async (type: 'full' | 'incremental') => {
    try {
      const res = await backupApi.trigger(type);
      if (res.success) {
        addToast({ type: 'success', message: `${type === 'full' ? '全量' : '增量'}备份已启动` });
        setLogs([]);
        setProgress({
          ...initialProgress,
          isRunning: true,
          backupId: res.data?.backup_id || null,
        });
        fetchAll();
      } else {
        addToast({ type: 'error', message: res.error || '启动备份失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    }
  };

  const handleCancel = async () => {
    try {
      const res = await backupApi.cancel();
      if (res.success) {
        addToast({ type: 'success', message: '备份已取消' });
        fetchAll();
      } else {
        addToast({ type: 'error', message: res.error || '取消备份失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    }
    setConfirm((c) => ({ ...c, open: false }));
  };

  const handleGC = async () => {
    try {
      const res = await gcApi.trigger();
      if (res.success) {
        addToast({ type: 'success', message: '垃圾回收已启动' });
        fetchAll();
      } else {
        addToast({ type: 'error', message: res.error || '启动垃圾回收失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    }
    setConfirm((c) => ({ ...c, open: false }));
  };

  const openConfirm = (title: string, message: string, onConfirm: () => void) =>
    setConfirm({ open: true, title, message, onConfirm });

  const displayIsRunning = stats.active_backup_running || progress.isRunning;

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="card p-6"><LoadingSkeleton rows={2} /></div>
        <div className="grid grid-cols-3 gap-4">{[1, 2, 3].map((i) => <CardSkeleton key={i} />)}</div>
        <div className="grid grid-cols-4 gap-4">{[1, 2, 3, 4].map((i) => <CardSkeleton key={i} />)}</div>
      </div>
    );
  }

  const pct = Math.min(100, Math.max(0, progress.percent || 0));
  const PhaseIcon = progress.phase === 'completed' ? CheckCircle
    : progress.phase === 'failed' ? XCircle
    : progress.phase === 'cancelled' ? Square
    : Activity;

  return (
    <div className="space-y-6">
      {/* Status Banner */}
      <div className="card p-6 bg-gradient-to-r from-brand-900/50 to-surface-1">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            {displayIsRunning ? (
              <>
                <span className="relative flex h-3 w-3">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-brand-400 opacity-75" />
                  <span className="relative inline-flex rounded-full h-3 w-3 bg-brand-400" />
                </span>
                <span className="text-lg font-semibold text-brand-400">备份运行中</span>
              </>
            ) : (
              <>
                <CheckCircle size={20} className="text-emerald-400" />
                <span className="text-lg font-semibold text-emerald-400">系统空闲</span>
              </>
            )}
          </div>
          <div className="flex items-center gap-6 text-sm text-slate-400">
            <span className="flex items-center gap-1.5">
              <Clock size={14} /> 上次: {formatRelativeTime(stats.last_backup_time)}
            </span>
            <span className="flex items-center gap-1.5">
              <RefreshCw size={14} /> 下次: {formatDateTime(stats.next_backup_time)}
            </span>
            {stats.last_backup_status && <StatusBadge status={stats.last_backup_status} />}
          </div>
        </div>
      </div>

      {/* Progress Bar Section - shown only during backup */}
      {displayIsRunning && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-3">
              <PhaseIcon size={20} className={PHASE_COLORS[progress.phase || 'uploading']} />
              <div>
                <div className={`font-semibold ${PHASE_COLORS[progress.phase || 'uploading']}`}>
                  {progress.phaseName || '备份进行中'}
                </div>
                <div className="text-sm text-slate-400">{progress.message || '处理中...'}</div>
              </div>
            </div>
            <div className="text-right">
              <div className="text-2xl font-bold text-white">{pct.toFixed(0)}%</div>
              {progress.total > 0 && (
                <div className="text-sm text-slate-400">
                  {progress.current} / {progress.total}
                </div>
              )}
            </div>
          </div>

          <div className="w-full h-3 bg-surface-2 rounded-full overflow-hidden mb-3">
            <div
              className="h-full bg-gradient-to-r from-brand-600 to-brand-400 rounded-full transition-all duration-300 ease-out"
              style={{ width: `${pct}%` }}
            />
          </div>

          {progress.currentFile && (
            <div className="flex items-center gap-2 text-sm text-slate-400 truncate">
              <Activity size={14} className="flex-shrink-0 animate-pulse" />
              <span className="truncate font-mono text-xs">{progress.currentFile}</span>
            </div>
          )}
        </div>
      )}

      {/* Real-time Logs Panel - shown during backup or if there are recent logs */}
      {(displayIsRunning || logs.length > 0) && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2">
              <Terminal size={18} className="text-brand-400" />
              <h2 className="text-lg font-semibold text-white">实时日志</h2>
              {displayIsRunning && (
                <span className="flex items-center gap-1 text-xs text-brand-400 bg-brand-400/10 px-2 py-0.5 rounded-full">
                  <span className="w-1.5 h-1.5 bg-brand-400 rounded-full animate-pulse" />
                  实时
                </span>
              )}
            </div>
            {logs.length > 0 && !displayIsRunning && (
              <button
                onClick={() => setLogs([])}
                className="text-xs text-slate-400 hover:text-white transition-colors"
              >
                清空
              </button>
            )}
          </div>
          <div ref={logsContainerRef} className="bg-surface-0 rounded-lg p-4 h-64 overflow-y-auto font-mono text-xs space-y-1">
            {logs.length === 0 ? (
              <div className="text-slate-500 italic">等待日志输出...</div>
            ) : (
              logs.map((log) => {
                const Icon = LOG_ICON[log.level] || Info;
                return (
                  <div key={log.id} className="flex items-start gap-2 leading-relaxed">
                    <Icon size={12} className={`flex-shrink-0 mt-0.5 ${LOG_COLOR[log.level]}`} />
                    <span className={`${LOG_COLOR[log.level]}`}>
                      <span className="text-slate-500">[{new Date(log.timestamp).toLocaleTimeString()}]</span>{' '}
                      {log.message}
                      {log.detail && <span className="text-slate-500"> ({log.detail})</span>}
                    </span>
                  </div>
                );
              })
            )}
            <div ref={logsEndRef} />
          </div>
        </div>
      )}

      {/* Gauge Charts */}
      <div className="grid grid-cols-3 gap-4">
        <div className="card p-6 flex items-center justify-center">
          <GaugeChart value={stats.oss_storage_used} max={stats.total_size} label="OSS 存储使用" color="#38bdf8" />
        </div>
        <div className="card p-6 flex items-center justify-center">
          <GaugeChart value={stats.saved_by_dedup} max={stats.total_size} label="去重节省" color="#34d399" />
        </div>
        <div className="card p-6 flex items-center justify-center">
          <GaugeChart value={stats.saved_by_compress} max={stats.total_size} label="压缩节省" color="#a78bfa" />
        </div>
      </div>

      {/* Stat Cards */}
      <div className="grid grid-cols-4 gap-4">
        <StatCard icon={Files} label="活跃文件" value={stats.total_files} iconColor="text-brand-400" />
        <StatCard icon={FileCheck} label="已备份文件" value={stats.backed_up_files} iconColor="text-emerald-400" />
        <StatCard icon={HardDrive} label="总文件大小" value={formatFileSize(stats.total_size)} iconColor="text-violet-400" />
        <StatCard icon={ArrowDownToLine} label="已备份大小" value={formatFileSize(stats.backed_up_size)} iconColor="text-amber-400" />
      </div>

      {/* Action Buttons */}
      <div className="flex items-center gap-3">
        <button className="btn-primary flex items-center gap-2" onClick={() => handleTrigger('full')}>
          <Play size={16} /> 全量备份
        </button>
        <button className="btn-secondary flex items-center gap-2" onClick={() => handleTrigger('incremental')}>
          <FastForward size={16} /> 增量备份
        </button>
        <button
          className="btn-danger flex items-center gap-2"
          disabled={!displayIsRunning}
          onClick={() => openConfirm('取消备份', '确定要取消当前正在运行的备份任务吗？', handleCancel)}
        >
          <Square size={16} /> 取消备份
        </button>
        <button
          className="btn-secondary flex items-center gap-2"
          onClick={() => openConfirm('垃圾回收', '确定要执行垃圾回收操作吗？', handleGC)}
        >
          <Trash2 size={16} /> 垃圾回收
        </button>
      </div>

      {/* Backup History */}
      <div className="card p-6">
        <h2 className="text-lg font-semibold text-white mb-4">备份历史</h2>
        <DataTable columns={historyColumns} data={history} rowKey={(r) => r.id} />
        <Pagination page={page} size={10} total={total} onChange={(p) => { setPage(p); fetchHistory(p); }} />
      </div>

      {/* Confirm Dialog */}
      <ConfirmDialog
        open={confirm.open}
        onClose={() => setConfirm((c) => ({ ...c, open: false }))}
        onConfirm={confirm.onConfirm}
        title={confirm.title}
        message={confirm.message}
      />
    </div>
  );
}
