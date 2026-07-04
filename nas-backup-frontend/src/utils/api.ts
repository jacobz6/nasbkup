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
  trigger: (type: 'full' | 'incremental') =>
    request<{ backup_id: number; status: string }>('/backup/trigger', {
      method: 'POST',
      body: JSON.stringify({ type }),
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
  update: (id: number, data: Partial<BackupDirectory>) =>
    request<BackupDirectory>(`/content/directories/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  delete: (id: number) =>
    request<{ status: string }>(`/content/directories/${id}`, {
      method: 'DELETE',
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

// Types
export interface DashboardStats {
  total_files: number;
  total_size: number;
  backed_up_files: number;
  backed_up_size: number;
  oss_storage_used: number;
  last_backup_time: string | null;
  last_backup_status: string | null;
  next_backup_time: string | null;
  saved_by_dedup: number;
  saved_by_compress: number;
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
