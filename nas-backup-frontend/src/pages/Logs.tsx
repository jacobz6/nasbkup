import { useState, useEffect, useCallback, Fragment } from 'react';
import { Search, Filter, ChevronDown, ChevronUp, Calendar } from 'lucide-react';
import { logApi, type LogRecord, type LogQueryParams } from '@/utils/api';
import { formatDateTime } from '@/utils/format';
import { LOG_LEVEL_MAP } from '@/utils/constants';
import { cn } from '@/lib/utils';
import { Pagination } from '@/components/ui/Pagination';
import { EmptyState } from '@/components/shared/EmptyState';
import { LoadingSkeleton } from '@/components/shared/LoadingSkeleton';

const LEVEL_OPTIONS = [
  { value: '', label: '全部' },
  { value: 'debug', label: 'debug' },
  { value: 'info', label: 'info' },
  { value: 'warn', label: 'warn' },
  { value: 'error', label: 'error' },
];

export function Logs() {
  const [data, setData] = useState<LogRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [pageSize] = useState(20);
  const [expandedIds, setExpandedIds] = useState<Set<number>>(new Set());

  // Filter state
  const [level, setLevel] = useState('');
  const [search, setSearch] = useState('');
  const [backupId, setBackupId] = useState('');
  const [startTime, setStartTime] = useState('');
  const [endTime, setEndTime] = useState('');

  // Applied filters (only update on search click)
  const [applied, setApplied] = useState<LogQueryParams>({});

  const fetchLogs = useCallback(async (params: LogQueryParams) => {
    setLoading(true);
    try {
      const res = await logApi.list({ ...params, page_size: pageSize });
      if (res.success) {
        setData(res.data ?? []);
        setTotal(res.total);
      } else {
        // Backend returned an error — show empty state gracefully
        setData([]);
        setTotal(0);
      }
    } catch {
      // Network error or JSON parse failure — show empty state gracefully
      setData([]);
      setTotal(0);
    } finally {
      setLoading(false);
    }
  }, [pageSize]);

  useEffect(() => {
    fetchLogs({ ...applied, page });
  }, [page, applied, fetchLogs]);

  const handleSearch = () => {
    const params: LogQueryParams = {};
    if (level) params.level = level;
    if (search) params.search = search;
    if (backupId) params.backup_id = Number(backupId);
    if (startTime) params.start_time = startTime;
    if (endTime) params.end_time = endTime;
    setApplied(params);
    setPage(1);
  };

  const handleReset = () => {
    setLevel('');
    setSearch('');
    setBackupId('');
    setStartTime('');
    setEndTime('');
    setApplied({});
    setPage(1);
  };

  const toggleExpand = (id: number) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className="space-y-4">
      {/* Filter Bar */}
      <div className="card p-4">
        <div className="flex items-center gap-3 flex-wrap">
          <div className="flex items-center gap-2">
            <Filter size={16} className="text-slate-500" />
            <span className="text-sm text-slate-400">级别</span>
            <select
              value={level}
              onChange={(e) => setLevel(e.target.value)}
              className="input-field w-28 text-sm"
            >
              {LEVEL_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>{opt.label}</option>
              ))}
            </select>
          </div>

          <div className="relative">
            <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-500" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="搜索日志..."
              className="input-field pl-9 w-48 text-sm"
            />
          </div>

          <input
            type="number"
            value={backupId}
            onChange={(e) => setBackupId(e.target.value)}
            placeholder="备份ID"
            className="input-field w-28 text-sm"
          />

          <div className="flex items-center gap-1">
            <Calendar size={14} className="text-slate-500" />
            <input
              type="datetime-local"
              value={startTime}
              onChange={(e) => setStartTime(e.target.value)}
              className="input-field text-sm"
            />
            <span className="text-slate-500 text-xs">至</span>
            <input
              type="datetime-local"
              value={endTime}
              onChange={(e) => setEndTime(e.target.value)}
              className="input-field text-sm"
            />
          </div>

          <button onClick={handleSearch} className="btn-primary text-sm flex items-center gap-1.5">
            <Search size={14} />
            搜索
          </button>
          <button onClick={handleReset} className="btn-secondary text-sm">
            重置
          </button>
        </div>
      </div>

      {/* Log Table */}
      <div className="card p-0">
        {loading ? (
          <div className="p-5">
            <LoadingSkeleton rows={8} />
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-surface-3">
                  <th className="text-left px-4 py-3 text-xs font-medium text-slate-500 uppercase tracking-wider">级别</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-slate-500 uppercase tracking-wider">备份ID</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-slate-500 uppercase tracking-wider">消息</th>
                  <th className="text-left px-4 py-3 text-xs font-medium text-slate-500 uppercase tracking-wider">时间</th>
                  <th className="text-right px-4 py-3 text-xs font-medium text-slate-500 uppercase tracking-wider">详情</th>
                </tr>
              </thead>
              <tbody>
                {data.length === 0 ? (
                  <tr>
                    <td colSpan={5}>
                      <EmptyState
                        message="暂无日志记录"
                        description="当前没有符合条件的日志，您可以尝试调整筛选条件或稍后再来查看"
                      />
                    </td>
                  </tr>
                ) : (
                  data.map((log) => {
                    const config = LOG_LEVEL_MAP[log.level];
                    const isExpanded = expandedIds.has(log.id);
                    return (
                      <Fragment key={log.id}>
                        <tr className="border-b border-surface-3/50 hover:bg-surface-2/30">
                          <td className="px-4 py-3">
                            {config && (
                              <span className={cn('px-2 py-0.5 rounded text-xs font-mono font-medium', config.color, config.bg)}>
                                {config.label}
                              </span>
                            )}
                          </td>
                          <td className="px-4 py-3 font-mono text-slate-300">
                            {log.backup_id ?? '-'}
                          </td>
                          <td className="px-4 py-3 max-w-md truncate text-slate-300" title={log.message}>
                            {log.message}
                          </td>
                          <td className="px-4 py-3 text-slate-400 text-xs font-mono whitespace-nowrap">
                            {formatDateTime(log.created_at)}
                          </td>
                          <td className="px-4 py-3 text-right">
                            {log.detail && (
                              <button
                                onClick={() => toggleExpand(log.id)}
                                className="text-slate-500 hover:text-slate-300 transition-colors"
                              >
                                {isExpanded ? <ChevronUp size={16} /> : <ChevronDown size={16} />}
                              </button>
                            )}
                          </td>
                        </tr>
                        {isExpanded && log.detail && (
                          <tr className="border-b border-surface-3/50">
                            <td colSpan={5} className="px-4 py-3 bg-surface-2/30">
                              <pre className="text-xs text-slate-400 whitespace-pre-wrap font-mono">{log.detail}</pre>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Pagination */}
      <Pagination page={page} size={pageSize} total={total} onChange={setPage} />
    </div>
  );
}
