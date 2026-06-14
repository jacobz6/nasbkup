export const BACKUP_STATUS_MAP: Record<string, { label: string; color: string }> = {
  pending: { label: '等待中', color: 'text-amber-400' },
  running: { label: '运行中', color: 'text-brand-400' },
  completed: { label: '已完成', color: 'text-emerald-400' },
  failed: { label: '失败', color: 'text-rose-400' },
  cancelled: { label: '已取消', color: 'text-slate-400' },
};

export const BACKUP_TYPE_MAP: Record<string, { label: string; color: string }> = {
  full: { label: '全量', color: 'text-brand-400' },
  incremental: { label: '增量', color: 'text-violet-400' },
};

export const LOG_LEVEL_MAP: Record<string, { label: string; color: string; bg: string }> = {
  debug: { label: 'DEBUG', color: 'text-slate-400', bg: 'bg-slate-500/20' },
  info: { label: 'INFO', color: 'text-brand-400', bg: 'bg-brand-500/20' },
  warn: { label: 'WARN', color: 'text-amber-400', bg: 'bg-amber-500/20' },
  error: { label: 'ERROR', color: 'text-rose-400', bg: 'bg-rose-500/20' },
};

export const EXCLUSION_TYPE_MAP: Record<string, { label: string }> = {
  extension: { label: '扩展名' },
  directory: { label: '目录' },
  pattern: { label: '模式' },
  size_exceed: { label: '大小超限' },
};

export const STORAGE_CLASS_MAP: Record<string, { label: string }> = {
  ColdArchive: { label: '冷归档' },
  Archive: { label: '归档' },
};

export const TIMEZONE_OPTIONS = [
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'America/New_York',
  'America/Los_Angeles',
  'Europe/London',
  'Europe/Berlin',
  'UTC',
];
