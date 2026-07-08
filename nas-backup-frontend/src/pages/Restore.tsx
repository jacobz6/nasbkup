import { useState, useCallback, useEffect, useRef } from 'react';
import {
  RotateCcw, Play, Square, Search, FolderOpen, Download, Inbox,
  CheckCircle, XCircle, Activity, Terminal, AlertCircle, Info,
  HardDrive, Clock, Zap, FileText, RefreshCw, Archive,
} from 'lucide-react';
import {
  restoreApi,
  type RestorableFile,
  type RestoreJobRecord,
  type BackupRecord,
} from '@/utils/api';
import { useRestoreProgress } from '@/hooks/useRestoreProgress';
import { DataTable, type Column } from '@/components/ui/DataTable';
import { Pagination } from '@/components/ui/Pagination';
import { StatusBadge } from '@/components/ui/StatusBadge';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { LoadingSkeleton } from '@/components/shared/LoadingSkeleton';
import { EmptyState } from '@/components/shared/EmptyState';
import { StatCard } from '@/components/ui/StatCard';
import { formatFileSize, formatDateTime, formatDuration } from '@/utils/format';
import { useAppStore } from '@/store/useAppStore';
import { cn } from '@/lib/utils';

// ── Restore Status Colors & Helpers ─────────────────────────────────

const RESTORE_STATUS_MAP: Record<string, { label: string; color: string }> = {
  pending: { label: '等待中', color: 'text-slate-400' },
  running: { label: '恢复中', color: 'text-brand-400' },
  completed: { label: '已完成', color: 'text-emerald-400' },
  failed: { label: '失败', color: 'text-red-400' },
  cancelled: { label: '已取消', color: 'text-slate-400' },
};

const PHASE_COLORS: Record<string, string> = {
  preparing: 'text-sky-400',
  thawing: 'text-amber-400',
  downloading: 'text-brand-400',
  restoring: 'text-violet-400',
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

// ── File List Columns ──────────────────────────────────────────────

const fileColumns: Column<RestorableFile>[] = [
  {
    key: 'path',
    header: '文件路径',
    render: (r) => (
      <div className="flex items-center gap-2">
        <FileText size={14} className="text-slate-500 shrink-0" />
        <span className="font-mono text-sm text-slate-200 truncate" title={r.path}>
          {r.path}
        </span>
      </div>
    ),
  },
  {
    key: 'size',
    header: '大小',
    className: 'w-24',
    render: (r) => (
      <span className="font-mono text-sm text-slate-400">{formatFileSize(r.size)}</span>
    ),
  },
  {
    key: 'mod_time',
    header: '修改时间',
    className: 'w-40',
    render: (r) => (
      <span className="text-sm text-slate-400">{formatDateTime(r.mod_time)}</span>
    ),
  },
  {
    key: 'hash',
    header: 'Hash',
    className: 'w-28',
    render: (r) => (
      <span className="font-mono text-xs text-slate-500 truncate block" title={r.hash}>
        {r.hash.length > 12 ? `${r.hash.slice(0, 8)}...` : r.hash}
      </span>
    ),
  },
];

// ── Job History Columns ────────────────────────────────────────────

const jobColumns: Column<RestoreJobRecord>[] = [
  { key: 'id', header: 'ID', className: 'font-mono w-16' },
  {
    key: 'status',
    header: '状态',
    className: 'w-24',
    render: (r) => {
      const cfg = RESTORE_STATUS_MAP[r.status] || { label: r.status, color: 'text-slate-400' };
      return (
        <span className={cn('inline-flex items-center gap-1.5 text-sm font-medium', cfg.color)}>
          {r.status === 'running' && (
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-brand-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-brand-400" />
            </span>
          )}
          {cfg.label}
        </span>
      );
    },
  },
  {
    key: 'total_files',
    header: '文件数',
    className: 'w-20',
    render: (r) => (
      <span className="text-sm text-slate-300">
        {r.restored_files}/{r.total_files}
      </span>
    ),
  },
  {
    key: 'total_size',
    header: '大小',
    className: 'w-24',
    render: (r) => (
      <span className="font-mono text-sm text-slate-400">
        {formatFileSize(r.restored_size)}/{formatFileSize(r.total_size)}
      </span>
    ),
  },
  {
    key: 'expedited',
    header: '加急',
    className: 'w-16',
    render: (r) =>
      r.expedited ? (
        <Zap size={14} className="text-amber-400" />
      ) : (
        <span className="text-slate-600">-</span>
      ),
  },
  {
    key: 'elapsed_ms',
    header: '耗时',
    className: 'w-20',
    render: (r) => (
      <span className="font-mono text-sm text-slate-400">
        {r.elapsed_ms > 0 ? formatDuration(r.elapsed_ms) : '-'}
      </span>
    ),
  },
  {
    key: 'created_at',
    header: '创建时间',
    className: 'w-40',
    render: (r) => (
      <span className="text-sm text-slate-400">{formatDateTime(r.created_at)}</span>
    ),
  },
];

// ── Main Restore Page ──────────────────────────────────────────────

export function Restore() {
  const addToast = useAppStore((s) => s.addToast);

  // ── File list state ───────────────────────────────────────────────
  const [files, setFiles] = useState<RestorableFile[]>([]);
  const [filesTotal, setFilesTotal] = useState(0);
  const [filesPage, setFilesPage] = useState(1);
  const [filesPageSize] = useState(20);
  const [filesLoading, setFilesLoading] = useState(true);
  const [searchQuery, setSearchQuery] = useState('');
  const [dirPath, setDirPath] = useState<string>(''); // current directory prefix for browsing

  // ── Selected files state ────────────────────────────────────────
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [selectedSize, setSelectedSize] = useState(0);

  // ── Backup version selector ───────────────────────────────────────
  const [backups, setBackups] = useState<BackupRecord[]>([]);
  const [selectedBackupId, setSelectedBackupId] = useState<number | undefined>(undefined);

  // ── Restore config ───────────────────────────────────────────────
  const [outputMode, setOutputMode] = useState<'original' | 'custom'>('original');
  const [outputDir, setOutputDir] = useState('');
  const [conflictStrategy, setConflictStrategy] = useState<'skip' | 'overwrite' | 'rename'>('skip');
  const [expedited, setExpedited] = useState(false);
  const [restoring, setRestoring] = useState(false);

  // ── Job history ──────────────────────────────────────────────────
  const [jobs, setJobs] = useState<RestoreJobRecord[]>([]);
  const [jobsTotal, setJobsTotal] = useState(0);
  const [jobsPage, setJobsPage] = useState(1);
  const [jobsPageSize] = useState(10);
  const [jobsLoading, setJobsLoading] = useState(true);

  // ── Confirm dialog ───────────────────────────────────────────────
  const [confirm, setConfirm] = useState<{
    open: boolean;
    title: string;
    message: string;
    onConfirm: () => void;
  }>({ open: false, title: '', message: '', onConfirm: () => {} });

  // ── Progress state (SSE) ───────────────────────────────────────
  const progress = useRestoreProgress();
  const logsContainerRef = useRef<HTMLDivElement>(null);
  const logsEndRef = useRef<HTMLDivElement>(null);

  // ── Fetch backups ────────────────────────────────────────────────
  const fetchBackups = useCallback(async () => {
    try {
      const res = await restoreApi.listBackups(1, 50);
      if (res.success && res.data) {
        setBackups(res.data);
      }
    } catch (e) {
      console.error('Failed to fetch backups:', e);
    }
  }, []);

  // ── Fetch restorable files ───────────────────────────────────────
  const fetchFiles = useCallback(async (p = filesPage) => {
    setFilesLoading(true);
    try {
      const params: Record<string, string | number | undefined> = {
        page: p,
        size: filesPageSize,
      };
      if (searchQuery) params.search = searchQuery;
      if (selectedBackupId !== undefined) params.backup_id = selectedBackupId;
      if (dirPath) params.dir_path = dirPath;

      const res = await restoreApi.listFiles(params);
      if (res.success) {
        setFiles(res.data ?? []);
        setFilesTotal(res.total);
      }
    } catch (e) {
      console.error('Failed to fetch restorable files:', e);
    } finally {
      setFilesLoading(false);
    }
  }, [filesPage, filesPageSize, searchQuery, selectedBackupId, dirPath]);

  // ── Fetch job history ────────────────────────────────────────────
  const fetchJobs = useCallback(async (p = jobsPage) => {
    setJobsLoading(true);
    try {
      const res = await restoreApi.listJobs({ page: p, size: jobsPageSize });
      if (res.success) {
        setJobs(res.data ?? []);
        setJobsTotal(res.total);
      }
    } catch (e) {
      console.error('Failed to fetch jobs:', e);
    } finally {
      setJobsLoading(false);
    }
  }, [jobsPage, jobsPageSize]);

  // ── Initial load ──────────────────────────────────────────────────
  useEffect(() => {
    fetchBackups();
    fetchFiles(1);
    fetchJobs(1);
  }, []);

  // Re-fetch files when page or filters change
  useEffect(() => {
    fetchFiles();
  }, [filesPage, searchQuery, selectedBackupId, dirPath]);

  // Re-fetch jobs when page changes
  useEffect(() => {
    fetchJobs();
  }, [jobsPage]);

  // ── Auto-scroll logs ─────────────────────────────────────────────
  useEffect(() => {
    const container = logsContainerRef.current;
    if (!container) return;
    const distFromBottom = container.scrollHeight - container.scrollTop - container.clientHeight;
    if (distFromBottom < 50) {
      logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [progress.logs]);

  // ── Compute selected file size ──────────────────────────────────
  useEffect(() => {
    const total = files
      .filter((f) => selectedIds.has(f.id))
      .reduce((sum, f) => sum + f.size, 0);
    setSelectedSize(total);
  }, [files, selectedIds]);

  // ── Toggle select single file ────────────────────────────────────
  const toggleSelectFile = (id: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // ── Toggle select all on current page ─────────────────────────────
  const toggleSelectAll = () => {
    const allSelected = files.every((f) => selectedIds.has(f.id));
    if (allSelected) {
      // Deselect all on this page
      setSelectedIds((prev) => {
        const next = new Set(prev);
        for (const f of files) next.delete(f.id);
        return next;
      });
    } else {
      // Select all on this page
      setSelectedIds((prev) => {
        const next = new Set(prev);
        for (const f of files) next.add(f.id);
        return next;
      });
    }
  };

  // ── Handle search ────────────────────────────────────────────────
  const handleSearch = () => {
    setFilesPage(1);
    setSelectedIds(new Set());
  };

  // ── Start restore (selected files) ───────────────────────────────
  const handleStartRestore = async () => {
    const selectedPaths = files
      .filter((f) => selectedIds.has(f.id))
      .map((f) => f.path);

    if (selectedPaths.length === 0) {
      addToast({ type: 'error', message: '请先选择要恢复的文件' });
      return;
    }

    const data = {
      paths: selectedPaths,
      backup_id: selectedBackupId,
      output_dir: outputMode === 'original' ? '' : outputDir,
      conflict_strategy: conflictStrategy,
      expedited,
    };

    setRestoring(true);
    try {
      const res = await restoreApi.trigger(data);
      if (res.success && res.data) {
        addToast({ type: 'success', message: `恢复任务已创建 (ID: ${res.data.job_id})，共 ${res.data.total_files} 个文件` });
        // Connect SSE for progress
        progress.reset();
        progress.connect();
        fetchJobs(1);
      } else {
        addToast({ type: 'error', message: res.error || '启动恢复失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    } finally {
      setRestoring(false);
      setConfirm((c) => ({ ...c, open: false }));
    }
  };

  // ── Full restore (all files) ─────────────────────────────────────
  const handleFullRestore = async () => {
    const data = {
      paths: [],
      pattern: '*',
      backup_id: selectedBackupId,
      output_dir: outputMode === 'original' ? '' : outputDir,
      conflict_strategy: conflictStrategy,
      expedited,
    };

    setRestoring(true);
    try {
      const res = await restoreApi.trigger(data);
      if (res.success && res.data) {
        addToast({ type: 'success', message: `全盘恢复任务已创建 (ID: ${res.data.job_id})，共 ${res.data.total_files} 个文件` });
        progress.reset();
        progress.connect();
        fetchJobs(1);
      } else {
        addToast({ type: 'error', message: res.error || '启动全盘恢复失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    } finally {
      setRestoring(false);
      setConfirm((c) => ({ ...c, open: false }));
    }
  };

  // ── Cancel restore ──────────────────────────────────────────────
  const handleCancelRestore = async () => {
    if (!progress.jobId) return;
    try {
      const res = await restoreApi.cancelJob(progress.jobId);
      if (res.success) {
        addToast({ type: 'success', message: '恢复任务已取消' });
        progress.reset();
        fetchJobs(1);
      } else {
        addToast({ type: 'error', message: res.error || '取消恢复失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误' });
    }
    setConfirm((c) => ({ ...c, open: false }));
  };

  // ── File columns with checkbox ───────────────────────────────────
  const columnsWithSelect: Column<RestorableFile>[] = [
    {
      key: '_checkbox',
      header: '',
      className: 'w-10',
      render: (r) => (
        <input
          type="checkbox"
          checked={selectedIds.has(r.id)}
          onChange={() => toggleSelectFile(r.id)}
          className="rounded border-surface-4 bg-surface-2 text-brand-400 focus:ring-brand-400"
        />
      ),
    },
    {
      key: 'path',
      header: '文件路径',
      render: (r) => {
        const dir = r.path.substring(0, r.path.lastIndexOf('/'));
        return (
          <div className="flex items-center gap-2 group">
            <FileText size={14} className="text-slate-500 shrink-0" />
            <span className="font-mono text-sm text-slate-200 truncate" title={r.path}>
              {r.path}
            </span>
            {dir && (
              <button
                onClick={() => { setDirPath(dir); setSearchQuery(''); setFilesPage(1); setSelectedIds(new Set()); }}
                className="opacity-0 group-hover:opacity-100 text-slate-500 hover:text-brand-400 transition-opacity shrink-0"
                title={`浏览目录: ${dir}`}
              >
                <FolderOpen size={12} />
              </button>
            )}
          </div>
        );
      },
    },
    {
      key: 'size',
      header: '大小',
      className: 'w-24',
      render: (r) => (
        <span className="font-mono text-sm text-slate-400">{formatFileSize(r.size)}</span>
      ),
    },
    {
      key: 'mod_time',
      header: '修改时间',
      className: 'w-40',
      render: (r) => (
        <span className="text-sm text-slate-400">{formatDateTime(r.mod_time)}</span>
      ),
    },
    {
      key: 'hash',
      header: 'Hash',
      className: 'w-28',
      render: (r) => (
        <span className="font-mono text-xs text-slate-500 truncate block" title={r.hash}>
          {r.hash.length > 12 ? `${r.hash.slice(0, 8)}...` : r.hash}
        </span>
      ),
    },
  ];

  // ── Render ────────────────────────────────────────────────────────
  const pct = Math.min(100, Math.max(0, progress.percent || 0));
  const PhaseIcon =
    progress.phase === 'completed' ? CheckCircle
    : progress.phase === 'failed' ? XCircle
    : progress.phase === 'cancelled' ? Square
    : Activity;

  return (
    <div className="space-y-6">
      {/* ── Header ─────────────────────────────────────────────────── */}
      <div className="card p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="p-2.5 rounded-lg bg-brand-500/10 text-brand-400">
              <RotateCcw size={22} />
            </div>
            <div>
              <h2 className="text-lg font-semibold text-white">文件恢复</h2>
              <p className="text-sm text-slate-400 mt-0.5">
                从 OSS 归档存储中恢复备份文件到本地
              </p>
            </div>
          </div>
        </div>
        <div className="mt-4 flex items-start gap-2 text-xs text-slate-500 bg-surface-2/50 border border-surface-3 rounded-lg p-3">
          <Info size={14} className="shrink-0 mt-0.5 text-slate-400" />
          <div>
            选择要恢复的文件，配置恢复参数后点击"开始恢复"。归档存储（ColdArchive）的文件需要先解冻才能下载，开启"加急解冻"可加速此过程。
            也可使用"一键全盘恢复"恢复所有备份文件。
          </div>
        </div>
      </div>

      {/* ── Progress Panel (shown during restore) ────────────────────── */}
      {progress.isRunning && (
        <div className="card p-6 bg-gradient-to-r from-brand-900/50 to-surface-1">
          {/* Status line */}
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-3">
              <span className="relative flex h-3 w-3">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-brand-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-3 w-3 bg-brand-400" />
              </span>
              <div className="flex items-center gap-2">
                <PhaseIcon size={20} className={PHASE_COLORS[progress.phase] || 'text-brand-400'} />
                <div>
                  <div className={`font-semibold ${PHASE_COLORS[progress.phase] || 'text-brand-400'}`}>
                    {progress.phaseName || '恢复进行中'}
                  </div>
                  <div className="text-sm text-slate-400">{progress.message || '处理中...'}</div>
                </div>
              </div>
            </div>
            <div className="flex items-center gap-4">
              <button
                onClick={() =>
                  setConfirm({
                    open: true,
                    title: '取消恢复',
                    message: '确定要取消当前正在运行的恢复任务吗？已恢复的文件将保留。',
                    onConfirm: handleCancelRestore,
                  })
                }
                className="btn-danger flex items-center gap-2"
              >
                <Square size={16} /> 取消恢复
              </button>
              <div className="text-right">
                <div className="text-2xl font-bold text-white">{pct.toFixed(0)}%</div>
                {progress.total > 0 && (
                  <div className="text-sm text-slate-400">
                    {progress.current}/{progress.total}
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* Progress bar */}
          <div className="w-full h-3 bg-surface-2 rounded-full overflow-hidden mb-3">
            <div
              className="h-full bg-gradient-to-r from-brand-600 to-brand-400 rounded-full transition-all duration-300 ease-out"
              style={{ width: `${pct}%` }}
            />
          </div>

          {/* Current file + size info */}
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2 text-sm text-slate-400 truncate">
              <Activity size={14} className="flex-shrink-0 animate-pulse" />
              <span className="truncate font-mono text-xs">{progress.filePath || '准备中...'}</span>
            </div>
            <div className="flex items-center gap-4 text-sm text-slate-400">
              {progress.restoredSize > 0 && (
                <span className="flex items-center gap-1">
                  <Download size={14} />
                  {formatFileSize(progress.restoredSize)}
                  {progress.totalSize > 0 && ` / ${formatFileSize(progress.totalSize)}`}
                </span>
              )}
            </div>
          </div>

          {/* Log area (collapsible) */}
          {progress.logs.length > 0 && (
            <div className="mt-4">
              <div className="flex items-center justify-between mb-2">
                <div className="flex items-center gap-2">
                  <Terminal size={16} className="text-brand-400" />
                  <span className="text-sm font-medium text-slate-200">恢复日志</span>
                  <span className="flex items-center gap-1 text-xs text-brand-400 bg-brand-400/10 px-2 py-0.5 rounded-full">
                    <span className="w-1.5 h-1.5 bg-brand-400 rounded-full animate-pulse" />
                    实时
                  </span>
                </div>
              </div>
              <div
                ref={logsContainerRef}
                className="bg-surface-0 rounded-lg p-4 h-48 overflow-y-auto font-mono text-xs space-y-1"
              >
                {progress.logs.map((log, i) => {
                  const Icon = LOG_ICON[log.level] || Info;
                  return (
                    <div key={i} className="flex items-start gap-2 leading-relaxed">
                      <Icon size={12} className={`flex-shrink-0 mt-0.5 ${LOG_COLOR[log.level] || 'text-slate-400'}`} />
                      <span className={LOG_COLOR[log.level] || 'text-slate-300'}>
                        <span className="text-slate-500">[{new Date(log.timestamp).toLocaleTimeString()}]</span>{' '}
                        {log.message}
                      </span>
                    </div>
                  );
                })}
                <div ref={logsEndRef} />
              </div>
            </div>
          )}

          {/* Completion summary */}
          {(progress.phase === 'completed' || progress.phase === 'failed') && (
            <div className="mt-4 flex items-center gap-3">
              {progress.phase === 'completed' ? (
                <>
                  <CheckCircle size={18} className="text-emerald-400" />
                  <span className="text-sm text-emerald-400 font-medium">恢复已完成</span>
                </>
              ) : (
                <>
                  <XCircle size={18} className="text-red-400" />
                  <span className="text-sm text-red-400 font-medium">恢复失败</span>
                </>
              )}
              <button
                onClick={() => {
                  progress.reset();
                  fetchJobs(1);
                }}
                className="btn-secondary text-xs ml-2"
              >
                关闭
              </button>
            </div>
          )}
        </div>
      )}

      {/* ── Main Content: Two-column layout ─────────────────────────── */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Left column: File list */}
        <div className="lg:col-span-2 space-y-4">
          {/* Filter bar */}
          <div className="card p-4">
            <div className="flex items-center gap-3 flex-wrap">
              {/* Search */}
              <div className="relative">
                <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-500" />
                <input
                  type="text"
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
                  placeholder="搜索文件路径..."
                  className="input-field pl-9 w-56 text-sm"
                />
              </div>

              {/* Backup version selector */}
              <div className="flex items-center gap-2">
                <Archive size={14} className="text-slate-500" />
                <span className="text-sm text-slate-400">备份版本</span>
                <select
                  value={selectedBackupId ?? ''}
                  onChange={(e) => {
                    setSelectedBackupId(e.target.value ? Number(e.target.value) : undefined);
                    setFilesPage(1);
                    setSelectedIds(new Set());
                  }}
                  className="input-field w-40 text-sm"
                >
                  <option value="">全部版本</option>
                  {backups.map((b) => (
                    <option key={b.id} value={b.id}>
                      #{b.id} - {formatDateTime(b.created_at)} ({b.type})
                    </option>
                  ))}
                </select>
              </div>

              <button onClick={handleSearch} className="btn-primary text-sm flex items-center gap-1.5">
                <Search size={14} />
                搜索
              </button>

              <button
                onClick={() => {
                  setSearchQuery('');
                  setDirPath('');
                  setSelectedBackupId(undefined);
                  setFilesPage(1);
                  setSelectedIds(new Set());
                }}
                className="btn-secondary text-sm"
              >
                重置
              </button>

              <div className="ml-auto flex items-center gap-2">
                <button
                  onClick={toggleSelectAll}
                  className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1"
                >
                  {files.length > 0 && files.every((f) => selectedIds.has(f.id)) ? '取消全选' : '全选当前页'}
                </button>
                {selectedIds.size > 0 && (
                  <span className="text-xs text-slate-500">已选 {selectedIds.size} 项</span>
                )}
              </div>
            </div>
          </div>

          {/* File list table */}
          <div className="card p-0 overflow-hidden">
            {/* Breadcrumb / path navigation */}
            {(dirPath || searchQuery) && (
              <div className="flex items-center gap-2 px-4 py-2.5 border-b border-slate-700/50 bg-slate-800/30">
                <button
                  onClick={() => { setDirPath(''); setSearchQuery(''); setFilesPage(1); }}
                  className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1"
                >
                  <FolderOpen size={12} />
                  根目录
                </button>
                {dirPath && dirPath.split('/').filter(Boolean).reduce((acc: JSX.Element[], seg, i, arr) => {
                  const path = '/' + arr.slice(0, i + 1).join('/');
                  acc.push(<span key={i} className="text-slate-600 text-xs">/</span>);
                  acc.push(
                    <button
                      key={`p-${i}`}
                      onClick={() => { setDirPath(path); setFilesPage(1); }}
                      className={`text-xs transition-colors ${i === arr.length - 1 ? 'text-slate-300 font-medium' : 'text-brand-400 hover:text-brand-300'}`}
                    >
                      {seg}
                    </button>
                  );
                  return acc;
                }, [] as JSX.Element[])}
                {searchQuery && (
                  <span className="ml-auto text-xs text-slate-500">
                    搜索: "<span className="text-slate-300">{searchQuery}</span>"
                    <button onClick={() => setSearchQuery('')} className="ml-1 text-slate-500 hover:text-red-400">×</button>
                  </span>
                )}
              </div>
            )}
            {filesLoading ? (
              <div className="p-5">
                <LoadingSkeleton rows={8} />
              </div>
            ) : files.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16 text-slate-500">
                <Inbox size={48} strokeWidth={1} className="mb-4 text-slate-600" />
                <p className="text-lg font-medium text-slate-400">
                  {searchQuery ? '未找到匹配的文件' : dirPath ? '该目录下暂无可恢复文件' : '暂无可恢复文件'}
                </p>
                <p className="text-sm mt-2 text-slate-600 max-w-md text-center px-4">
                  {searchQuery
                    ? '尝试使用更短的关键词搜索，或检查路径是否正确。'
                    : dirPath
                    ? '该目录下没有已备份的文件记录。'
                    : '请先在「备份内容」中配置备份目录，并执行一次成功的备份。文件备份完成后会在此处显示。'
                  }
                </p>
                {(dirPath || searchQuery) && (
                  <button
                    onClick={() => { setDirPath(''); setSearchQuery(''); setFilesPage(1); }}
                    className="mt-4 btn-secondary text-xs"
                  >
                    返回根目录
                  </button>
                )}
              </div>
            ) : (
              <>
                <DataTable columns={columnsWithSelect} data={files} rowKey={(r) => r.id} />
                <Pagination
                  page={filesPage}
                  size={filesPageSize}
                  total={filesTotal}
                  onChange={(p) => {
                    setFilesPage(p);
                  }}
                />
              </>
            )}
          </div>
        </div>

        {/* Right column: Restore config panel */}
        <div className="space-y-4">
          {/* Selection summary */}
          <div className="card p-5">
            <h3 className="text-sm font-medium text-white mb-3 flex items-center gap-2">
              <FolderOpen size={16} className="text-brand-400" />
              已选文件
            </h3>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-sm text-slate-400">文件数</span>
                <span className="font-mono text-lg font-bold text-white">{selectedIds.size}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-sm text-slate-400">总大小</span>
                <span className="font-mono text-lg font-bold text-white">{formatFileSize(selectedSize)}</span>
              </div>
            </div>
          </div>

          {/* Restore configuration */}
          <div className="card p-5">
            <h3 className="text-sm font-medium text-white mb-4 flex items-center gap-2">
              <HardDrive size={16} className="text-brand-400" />
              恢复配置
            </h3>

            <div className="space-y-4">
              {/* Output directory mode */}
              <div>
                <label className="block text-sm text-slate-400 mb-2">恢复目标</label>
                <div className="space-y-2">
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      name="outputMode"
                      checked={outputMode === 'original'}
                      onChange={() => setOutputMode('original')}
                      className="text-brand-400 focus:ring-brand-400"
                    />
                    <span className="text-sm text-slate-300">恢复到原路径</span>
                  </label>
                  <label className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="radio"
                      name="outputMode"
                      checked={outputMode === 'custom'}
                      onChange={() => setOutputMode('custom')}
                      className="text-brand-400 focus:ring-brand-400"
                    />
                    <span className="text-sm text-slate-300">恢复到指定目录</span>
                  </label>
                </div>
              </div>

              {/* Custom output directory input */}
              {outputMode === 'custom' && (
                <div>
                  <label className="block text-sm text-slate-400 mb-1.5">目标目录</label>
                  <input
                    type="text"
                    value={outputDir}
                    onChange={(e) => setOutputDir(e.target.value)}
                    placeholder="/path/to/restore"
                    className="input-field text-sm"
                  />
                </div>
              )}

              {/* Conflict strategy */}
              <div>
                <label className="block text-sm text-slate-400 mb-1.5">冲突策略</label>
                <select
                  value={conflictStrategy}
                  onChange={(e) => setConflictStrategy(e.target.value as 'skip' | 'overwrite' | 'rename')}
                  className="input-field text-sm"
                >
                  <option value="skip">跳过已存在文件 (skip)</option>
                  <option value="overwrite">覆盖已存在文件 (overwrite)</option>
                  <option value="rename">重命名冲突文件 (rename)</option>
                </select>
              </div>

              {/* Expedited thawing */}
              <div className="flex items-center justify-between">
                <div>
                  <label className="text-sm text-slate-300">加急解冻</label>
                  <p className="text-xs text-slate-500 mt-0.5">加速归档文件的解冻过程</p>
                </div>
                <button
                  onClick={() => setExpedited(!expedited)}
                  className={cn(
                    'relative w-10 h-5 rounded-full transition-colors',
                    expedited ? 'bg-brand-500' : 'bg-slate-600'
                  )}
                >
                  <span
                    className={cn(
                      'absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform',
                      expedited ? 'left-5' : 'left-0.5'
                    )}
                  />
                </button>
              </div>
            </div>
          </div>

          {/* Action buttons */}
          <div className="space-y-2">
            <button
              className="btn-primary w-full flex items-center justify-center gap-2"
              disabled={selectedIds.size === 0 || restoring || progress.isRunning}
              onClick={() =>
                setConfirm({
                  open: true,
                  title: '确认恢复',
                  message: `即将恢复 ${selectedIds.size} 个文件（共 ${formatFileSize(selectedSize)}）。${
                    expedited ? '已开启加急解冻。' : ''
                  }冲突策略：${conflictStrategy}。是否继续？`,
                  onConfirm: handleStartRestore,
                })
              }
            >
              {restoring ? (
                <RefreshCw size={16} className="animate-spin" />
              ) : (
                <Play size={16} />
              )}
              开始恢复
            </button>

            <button
              className="btn-secondary w-full flex items-center justify-center gap-2"
              disabled={restoring || progress.isRunning}
              onClick={() =>
                setConfirm({
                  open: true,
                  title: '一键全盘恢复',
                  message: '即将恢复所有备份文件到本地。此操作可能需要较长时间，取决于文件数量和大小。是否继续？',
                  onConfirm: handleFullRestore,
                })
              }
            >
              <RotateCcw size={16} />
              一键全盘恢复
            </button>
          </div>
        </div>
      </div>

      {/* ── Restore Job History ──────────────────────────────────────── */}
      <div className="card p-6">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <Clock size={18} className="text-brand-400" />
            <h2 className="text-lg font-semibold text-white">恢复历史</h2>
          </div>
          <button
            onClick={() => fetchJobs()}
            className="p-1.5 rounded-lg hover:bg-surface-2 text-slate-400 hover:text-white transition-colors"
            title="刷新"
          >
            <RefreshCw size={16} />
          </button>
        </div>

        {jobsLoading ? (
          <LoadingSkeleton rows={5} />
        ) : jobs.length === 0 ? (
          <EmptyState
            message="暂无恢复记录"
            description="还没有执行过恢复任务。"
          />
        ) : (
          <>
            <DataTable columns={jobColumns} data={jobs} rowKey={(r) => r.id} />
            <Pagination
              page={jobsPage}
              size={jobsPageSize}
              total={jobsTotal}
              onChange={setJobsPage}
            />
          </>
        )}
      </div>

      {/* ── Confirm Dialog ───────────────────────────────────────────── */}
      <ConfirmDialog
        open={confirm.open}
        onClose={() => setConfirm((c) => ({ ...c, open: false }))}
        onConfirm={confirm.onConfirm}
        title={confirm.title}
        message={confirm.message}
        variant="warning"
        loading={restoring}
      />
    </div>
  );
}
