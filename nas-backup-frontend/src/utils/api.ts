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

const METHODS_REQUIRING_BODY = ['POST', 'PUT', 'PATCH'];

async function request<T>(
  endpoint: string,
  options?: RequestInit
): Promise<APIResponse<T>> {
  const method = (options?.method || 'GET').toUpperCase();
  const needsBody = METHODS_REQUIRING_BODY.includes(method);
  const hasBody = options?.body !== undefined && options?.body !== null;

  const url = `${API_BASE}${endpoint}`;
  const res = await fetch(url, {
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
    ...options,
    ...(needsBody && !hasBody ? { body: '{}' } : {}),
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
  trigger: () =>
    request<{ backup_id: number; status: string }>('/backup/trigger', {
      method: 'POST',
      body: JSON.stringify({}),
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

// Storage status (OSS connection / oss.db parse / engine readiness)
export const storageApi = {
  getStatus: () => request<StorageStatus>('/storage/status'),
};

// Types
export interface OSSInfo {
  storage_class: string;
  endpoint: string;
  bucket: string;
  remote_name: string;
  region: string;
}

// Backend storage/OSS health reported by GET /api/storage/status.
export interface StorageStatus {
  oss_connected: boolean;
  oss_db_exists: boolean;
  oss_db_parsed: boolean;
  ready: boolean; // engine initialized → backup/restore usable
  oss_error?: string;
  db_error?: string;
}

export interface DashboardStats {
  total_files: number;
  total_size: number;
  oss_storage_used: number;
  oss_quota_bytes: number;
  backup_count: number;
  unique_hash_count: number;
  oss_info: OSSInfo;
  last_backup_time: string | null;
  last_backup_status: string | null;
  next_backup_time: string | null;
  active_backup_running: boolean;
}

export interface BackupRecord {
  id: number;
  status: string;
  total_files: number;
  total_size: number;
  uploaded_size: number;
  skipped_dedup: number;
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
  orphan_grace_days: number;
  keep_deleted_days: number;
  db_bkup_keep_count: number;
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
  last_backup_status?: 'success' | 'failed' | '';
  last_backup_error?: string;
  last_backup_at?: string;
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

// ── Restore ───────────────────────────────────────────────────────

export interface RestoreJobRecord {
  id: number;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
  paths: string[];
  pattern: string;
  backup_id: number | null;
  output_dir: string;
  conflict_strategy: string;
  total_files: number;
  restored_files: number;
  failed_files: string[];
  total_size: number;
  restored_size: number;
  elapsed_ms: number;
  error_message: string;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export interface RestoreRequest {
  paths: string[];
  pattern?: string;
  backup_id?: number;
  output_dir: string;
  restore_to_original?: boolean;
  conflict_strategy?: 'overwrite' | 'skip' | 'rename';
}

export interface RestoreCreateResponse {
  job_id: number;
  status: string;
  total_files: number;
  total_size: number;
}

export interface RestorableFile {
  id: number;
  path: string;
  size: number;
  mod_time: string;
  hash: string;
  status: string;
}

export interface RestoreProgressEvent {
  type: 'phase' | 'progress' | 'file' | 'log' | 'connected';
  job_id: number;
  phase?: string;
  phase_name?: string;
  current?: number;
  total?: number;
  percent?: number;
  message?: string;
  detail?: string;
  file_path?: string;
  file_size?: number;
  restored_size?: number;
  total_size?: number;
  level?: string;
  timestamp: string;
}

export const restoreApi = {
  trigger: (data: RestoreRequest) =>
    request<RestoreCreateResponse>('/restore', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  listFiles: (params?: { dir_path?: string; backup_id?: number; search?: string; page?: number; size?: number }) =>
    paginatedRequest<RestorableFile>('/restore/files', params),
  listJobs: (params?: { page?: number; size?: number; status?: string }) =>
    paginatedRequest<RestoreJobRecord>('/restore/jobs', params),
  getJob: (id: number) => request<RestoreJobRecord>(`/restore/jobs/${id}`),
  cancelJob: (id: number) =>
    request<{ status: string }>(`/restore/jobs/${id}/cancel`, { method: 'POST' }),
  listBackups: (page = 1, size = 50) =>
    paginatedRequest<BackupRecord>('/backups', { page, size }),
  // Pull the latest authoritative oss.db from OSS, replacing the local working
  // copy. Used by the restore page "更新" button to re-sync after another
  // machine performed a backup.
  refreshOssDb: () =>
    request<{ status: string; pulled: boolean; message: string }>(
      '/restore/refresh-oss-db',
      { method: 'POST' }
    ),
};

export function createRestoreProgressStream(
  onEvent: (event: RestoreProgressEvent) => void,
  onError?: (error: Event) => void
): () => void {
  if (typeof window === 'undefined') {
    return () => {};
  }

  const es = new EventSource(`${API_BASE}/restore/progress/stream`);

  const handleMessage = (e: MessageEvent) => {
    try {
      const event: RestoreProgressEvent = JSON.parse(e.data);
      onEvent(event);
    } catch (err) {
      console.error('Failed to parse restore progress event:', err);
    }
  };

  es.addEventListener('phase', handleMessage);
  es.addEventListener('progress', handleMessage);
  es.addEventListener('file', handleMessage);
  es.addEventListener('log', handleMessage);
  es.addEventListener('connected', handleMessage);

  es.onerror = (e) => {
    console.error('Restore progress stream error:', e);
    if (onError) onError(e);
  };

  return () => {
    es.removeEventListener('phase', handleMessage);
    es.removeEventListener('progress', handleMessage);
    es.removeEventListener('file', handleMessage);
    es.removeEventListener('log', handleMessage);
    es.removeEventListener('connected', handleMessage);
    es.close();
  };
}
