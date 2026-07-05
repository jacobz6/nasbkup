const API_BASE = '/api';

export interface APIResponse<T> {
  success: boolean;
  data?: T;
  error?: string;
}

export interface PaginatedResponse<T> {
  success: boolean;
  data: T[];
  total: number;
  page: number;
  size: number;
}

async function request<T>(
  endpoint: string,
  options?: RequestInit
): Promise<APIResponse<T>> {
  const url = `${API_BASE}${endpoint}`;
  const res = await fetch(url, {
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
    ...options,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    return { success: false, error: text || `HTTP ${res.status}` };
  }
  return res.json();
}

async function paginatedRequest<T>(
  endpoint: string,
  params?: Record<string, string | number | undefined>
): Promise<PaginatedResponse<T>> {
  const searchParams = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([key, value]) => {
      if (value !== undefined && value !== '') {
        searchParams.set(key, String(value));
      }
    });
  }
  const query = searchParams.toString();
  const url = `${API_BASE}${endpoint}${query ? `?${query}` : ''}`;
  const res = await fetch(url);
  if (!res.ok) {
    return { success: false, data: [], total: 0, page: 1, size: 0 };
  }
  const json = await res.json();
  // Ensure data is always an array (backend may return null for empty results)
  return { ...json, data: json.data ?? [] };
}

// Dashboard
export const dashboardApi = {
  getStats: () => request<DashboardStats>('/dashboard/stats'),
  getHistory: (page = 1, size = 10) =>
    paginatedRequest<BackupRecord>('/dashboard/history', { page, size }),
};

// Backup
export const backupApi = {
  trigger: (type?: 'full' | 'incremental' | 'auto') =>
    request<{ backup_id: number; status: string }>('/backup/trigger', {
      method: 'POST',
      body: JSON.stringify(type ? { type } : {}),
    }),
  cancel: (backupId?: number) =>
    request<{ status: string }>(
      `/backup/cancel${backupId ? `?backup_id=${backupId}` : ''}`,
      { method: 'POST' }
    ),
  getStatus: () => request<BackupStatus>('/backup/status'),
};

// Content - File System Browse
export const fsApi = {
  browse: (path: string = '/') =>
    request<FSBrowseResult>(`/fs/browse?path=${encodeURIComponent(path)}`),
};

// Content - Directories
export const directoryApi = {
  list: () => request<BackupDirectory[]>('/content/directories'),
  create: (data: Omit<BackupDirectory, 'id'>) =>
    request<BackupDirectory>('/content/directories', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  // PATCH semantics: pass only the fields to update.
  // To enable/disable a directory, pass { enabled: true/false }.
  update: (id: number, data: Partial<BackupDirectory>) =>
    request<BackupDirectory>(`/content/directories/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),
};

// Content - Exclusions
export const exclusionApi = {
  list: () => request<ExclusionRule[]>('/content/exclusions'),
  create: (data: Omit<ExclusionRule, 'id'>) =>
    request<ExclusionRule>('/content/exclusions', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  update: (id: number, data: Partial<ExclusionRule>) =>
    request<ExclusionRule>(`/content/exclusions/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  delete: (id: number) =>
    request<{ status: string }>(`/content/exclusions/${id}`, {
      method: 'DELETE',
    }),
};

// Strategy
export const strategyApi = {
  getSchedule: () => request<ScheduleConfig>('/strategy/schedule'),
  updateSchedule: (data: ScheduleConfig) =>
    request<ScheduleConfig>('/strategy/schedule', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getCompression: () => request<CompressionConfig>('/strategy/compression'),
  updateCompression: (data: CompressionConfig) =>
    request<CompressionConfig>('/strategy/compression', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getUpload: () => request<UploadConfig>('/strategy/upload'),
  updateUpload: (data: UploadConfig) =>
    request<UploadConfig>('/strategy/upload', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getRetention: () => request<RetentionConfig>('/strategy/retention'),
  updateRetention: (data: RetentionConfig) =>
    request<RetentionConfig>('/strategy/retention', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getEncryption: () => request<EncryptionConfig>('/strategy/encryption'),
  updateEncryption: (data: EncryptionConfig) =>
    request<EncryptionConfig>('/strategy/encryption', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
};

// Logs
export const logApi = {
  list: (params?: LogQueryParams) =>
    paginatedRequest<LogRecord>('/logs', params as Record<string, string | number | undefined>),
  get: (id: number) => request<LogRecord>(`/logs/${id}`),
};

// GC
export const gcApi = {
  trigger: () =>
    request<{ status: string }>('/gc', { method: 'POST' }),
};

// Reconcile (system sync)
export const reconcileApi = {
  // dry_run query param controls whether fixes are actually applied.
  // When omitted, the backend uses cfg.Reconcile.DryRun (default true).
  run: (dryRun: boolean) =>
    request<ReconcileReport>(
      `/reconcile?dry_run=${dryRun ? 'true' : 'false'}`,
      { method: 'POST' }
    ),
};

// Types
export interface OSSInfo {
  storage_class: string;
  endpoint: string;
  bucket: string;
  remote_name: string;
  region: string;
}

export interface DashboardStats {
  total_files: number;
  total_size: number;
  oss_storage_used: number;
  oss_quota_bytes: number;
  backup_count: number;
  unique_hash_count: number;
  needs_reconcile: boolean;
  oss_info: OSSInfo;
  last_backup_time: string | null;
  last_backup_status: string | null;
  next_backup_time: string | null;
  active_backup_running: boolean;
}

export interface BackupRecord {
  id: number;
  type: string;
  status: string;
  base_backup_id: number | null;
  total_files: number;
  total_size: number;
  uploaded_size: number;
  skipped_dedup: number;
  skipped_inc: number;
  compress_saved: number;
  started_at: string | null;
  completed_at: string | null;
  error_message: string | null;
  created_at: string;
}

export interface BackupStatus {
  is_running: boolean;
  running_backup: BackupRecord | null;
}

export interface BackupDirectory {
  id: number;
  path: string;
  recursive: boolean;
  enabled: boolean;
  description: string;
}

export interface ExclusionRule {
  id: number;
  pattern: string;
  rule_type: 'extension' | 'directory' | 'pattern' | 'size_exceed';
  enabled: boolean;
}

export interface ScheduleConfig {
  enabled: boolean;
  cron_expr: string;
  timezone: string;
}

export interface CompressionConfig {
  enabled: boolean;
  algorithm: string;
  level: number;
  skip_types: string[];
}

export interface UploadConfig {
  storage_class: 'ColdArchive' | 'Archive';
  max_concurrency: number;
  chunk_size_mb: number;
  retry_count: number;
  retry_delay_sec: number;
  oss_quota_bytes: number;
}

export interface RetentionConfig {
  version_keep_count: number;
  orphan_grace_days: number;
  full_reset_interval: number;
  keep_deleted_days: number;
}

export interface EncryptionConfig {
  algorithm: string;
  key_file_path: string;
}

export interface LogRecord {
  id: number;
  backup_id: number | null;
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
  detail: string;
  created_at: string;
}

export interface LogQueryParams {
  backup_id?: number;
  level?: string;
  search?: string;
  start_time?: string;
  end_time?: string;
  page?: number;
  page_size?: number;
}

export interface FSEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mod_time: string;
  in_backup: boolean;
  partial_backup: boolean;
  has_update: boolean;
  will_backup: boolean;
}

export interface FSBrowseResult {
  path: string;
  parent_path: string;
  entries: FSEntry[];
}

// Backup Progress SSE types
export type ProgressPhase =
  | 'scanning'
  | 'hashing'
  | 'deduplicating'
  | 'uploading'
  | 'finalizing'
  | 'completed'
  | 'failed'
  | 'cancelled';

export interface ProgressEvent {
  type: 'phase' | 'progress' | 'log' | 'file' | 'connected';
  backup_id: number;
  phase?: ProgressPhase;
  phase_name?: string;
  current?: number;
  total?: number;
  percent?: number;
  message?: string;
  detail?: string;
  level?: 'debug' | 'info' | 'warn' | 'error';
  file_path?: string;
  file_size?: number;
  timestamp: string;
}

export interface BackupProgress {
  isRunning: boolean;
  backupId: number | null;
  phase: ProgressPhase | null;
  phaseName: string;
  message: string;
  current: number;
  total: number;
  percent: number;
  currentFile: string;
  logs: Array<{
    id: number;
    level: 'debug' | 'info' | 'warn' | 'error';
    message: string;
    detail?: string;
    timestamp: string;
  }>;
}

export function createProgressStream(
  onEvent: (event: ProgressEvent) => void,
  onError?: (error: Event) => void
): () => void {
  if (typeof window === 'undefined') {
    return () => {};
  }

  const es = new EventSource(`${API_BASE}/backup/progress/stream`);

  const handleMessage = (e: MessageEvent) => {
    try {
      const event: ProgressEvent = JSON.parse(e.data);
      onEvent(event);
    } catch (err) {
      console.error('Failed to parse progress event:', err);
    }
  };

  es.addEventListener('phase', handleMessage);
  es.addEventListener('progress', handleMessage);
  es.addEventListener('log', handleMessage);
  es.addEventListener('file', handleMessage);
  es.addEventListener('connected', handleMessage);

  es.onerror = (e) => {
    console.error('Progress stream error:', e);
    if (onError) onError(e);
  };

  return () => {
    es.removeEventListener('phase', handleMessage);
    es.removeEventListener('progress', handleMessage);
    es.removeEventListener('log', handleMessage);
    es.removeEventListener('file', handleMessage);
    es.removeEventListener('connected', handleMessage);
    es.close();
  };
}

// ── Reconcile (system sync) ───────────────────────────────────────────────
export interface RefCountMismatch {
  hash: string;
  stored_in_db: number;
  actual_active: number;
}

export interface BackupStatusFix {
  backup_id: number;
  from: string;
  to: string;
  reason: string;
}

export interface ReconcileReport {
  started_at: string;
  finished_at: string;
  duration: string;
  dry_run: boolean;
  // OSS ↔ hash_index
  oss_only_orphans: string[];
  dangling_hash_indexes_ref_zero: string[];
  dangling_hash_indexes_ref_nonzero: string[];
  // hash_index ↔ backup_files
  orphan_backup_files: string[];
  backup_files_missing_hash_index_but_in_oss: string[];
  // ref_count drift
  ref_count_mismatches: RefCountMismatch[];
  // backup status corrections
  failed_backups_with_files: BackupStatusFix[];
  completed_backups_no_files: BackupStatusFix[];
  // outcome
  applied_fixes: string[];
  skipped_fixes: string[];
  errors: string[];
}
