import { useState, useCallback, useEffect } from 'react';
import {
  Play, FastForward, Square, Trash2, Files, HardDrive,
  FileCheck, ArrowDownToLine, RefreshCw, CheckCircle, Clock,
} from 'lucide-react';
import { dashboardApi, backupApi, gcApi, type DashboardStats, type BackupRecord } from '@/utils/api';
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
  const addToast = useAppStore((s) => s.addToast);

  const fetchStats = useCallback(async () => {
    try {
      const res = await dashboardApi.getStats();
      if (res.success && res.data) {
        setStats(res.data);
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

  usePolling(fetchAll, 5000, stats.active_backup_running);

  useEffect(() => { fetchAll(); }, []);

  const handleTrigger = async (type: 'full' | 'incremental') => {
    try {
      const res = await backupApi.trigger(type);
      if (res.success) {
        addToast({ type: 'success', message: `${type === 'full' ? '全量' : '增量'}备份已启动` });
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

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="card p-6"><LoadingSkeleton rows={2} /></div>
        <div className="grid grid-cols-3 gap-4">{[1, 2, 3].map((i) => <CardSkeleton key={i} />)}</div>
        <div className="grid grid-cols-4 gap-4">{[1, 2, 3, 4].map((i) => <CardSkeleton key={i} />)}</div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Status Banner */}
      <div className="card p-6 bg-gradient-to-r from-brand-900/50 to-surface-1">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            {stats.active_backup_running ? (
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
          disabled={!stats.active_backup_running}
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
