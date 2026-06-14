import { useState, useEffect, useCallback } from 'react';
import { Clock, FileArchive, Upload, Shield, Key, Save, Plus, X } from 'lucide-react';
import { strategyApi, type ScheduleConfig, type CompressionConfig, type UploadConfig, type RetentionConfig, type EncryptionConfig } from '@/utils/api';
import { useAppStore } from '@/store/useAppStore';
import { TIMEZONE_OPTIONS, STORAGE_CLASS_MAP } from '@/utils/constants';
import { LoadingSkeleton } from '@/components/shared/LoadingSkeleton';
import { cn } from '@/lib/utils';

export function Strategy() {
  const addToast = useAppStore((s) => s.addToast);
  const [loading, setLoading] = useState(true);
  const [schedule, setSchedule] = useState<ScheduleConfig | null>(null);
  const [compression, setCompression] = useState<CompressionConfig | null>(null);
  const [upload, setUpload] = useState<UploadConfig | null>(null);
  const [retention, setRetention] = useState<RetentionConfig | null>(null);
  const [encryption, setEncryption] = useState<EncryptionConfig | null>(null);
  const [newSkipType, setNewSkipType] = useState('');

  useEffect(() => {
    Promise.all([
      strategyApi.getSchedule(),
      strategyApi.getCompression(),
      strategyApi.getUpload(),
      strategyApi.getRetention(),
      strategyApi.getEncryption(),
    ]).then(([s, c, u, r, e]) => {
      if (s.data) setSchedule(s.data);
      if (c.data) setCompression(c.data);
      if (u.data) setUpload(u.data);
      if (r.data) setRetention(r.data);
      if (e.data) setEncryption(e.data);
      setLoading(false);
    });
  }, []);

  const save = useCallback(async <T,>(apiCall: () => Promise<{ success: boolean; error?: string }>, label: string) => {
    const res = await apiCall();
    addToast({ type: res.success ? 'success' : 'error', message: res.success ? `${label}保存成功` : `${label}保存失败: ${res.error}` });
  }, [addToast]);

  if (loading) return <div className="space-y-4"><LoadingSkeleton rows={4} /><LoadingSkeleton rows={4} /></div>;

  return (
    <div className="space-y-4">
      {/* 调度配置 */}
      {schedule && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-200 font-medium"><Clock size={18} />调度配置</div>
            <button className="btn-primary flex items-center gap-1 text-sm" onClick={() => save(() => strategyApi.updateSchedule(schedule), '调度配置')}><Save size={14} />保存</button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <label className="flex items-center justify-between gap-2">
              <span className="text-sm text-slate-400">启用定时任务</span>
              <button type="button" onClick={() => setSchedule(d => ({ ...d, enabled: !d.enabled }))} className={cn("relative inline-flex h-6 w-11 items-center rounded-full transition-colors", schedule.enabled ? "bg-brand-500" : "bg-surface-3")}>
                <span className={cn("inline-block h-4 w-4 rounded-full bg-white transition-transform", schedule.enabled ? "translate-x-5" : "translate-x-0.5")} />
              </button>
            </label>
            <label className="space-y-1"><span className="text-sm text-slate-400">Cron 表达式</span><input className="input-field" placeholder="0 3 1 * *" value={schedule.cron_expr} onChange={e => setSchedule(d => ({ ...d, cron_expr: e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">时区</span><select className="input-field" value={schedule.timezone} onChange={e => setSchedule(d => ({ ...d, timezone: e.target.value }))}>{TIMEZONE_OPTIONS.map(tz => <option key={tz} value={tz}>{tz}</option>)}</select></label>
          </div>
        </div>
      )}

      {/* 压缩配置 */}
      {compression && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-200 font-medium"><FileArchive size={18} />压缩配置</div>
            <button className="btn-primary flex items-center gap-1 text-sm" onClick={() => save(() => strategyApi.updateCompression(compression), '压缩配置')}><Save size={14} />保存</button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <label className="flex items-center justify-between gap-2">
              <span className="text-sm text-slate-400">启用压缩</span>
              <button type="button" onClick={() => setCompression(d => ({ ...d, enabled: !d.enabled }))} className={cn("relative inline-flex h-6 w-11 items-center rounded-full transition-colors", compression.enabled ? "bg-brand-500" : "bg-surface-3")}>
                <span className={cn("inline-block h-4 w-4 rounded-full bg-white transition-transform", compression.enabled ? "translate-x-5" : "translate-x-0.5")} />
              </button>
            </label>
            <label className="space-y-1"><span className="text-sm text-slate-400">算法</span><input className="input-field bg-surface-2" readOnly value={compression.algorithm} /></label>
            <label className="space-y-1 md:col-span-2">
              <span className="text-sm text-slate-400">压缩级别 <span className="text-brand-400 font-mono">{compression.level}</span></span>
              <input type="range" min={1} max={22} value={compression.level} onChange={e => setCompression(d => ({ ...d, level: +e.target.value }))} className="w-full appearance-none h-2 bg-surface-3 rounded-full accent-brand-400" />
            </label>
            <div className="md:col-span-2 space-y-2">
              <span className="text-sm text-slate-400">跳过类型</span>
              <div className="flex flex-wrap gap-1.5">
                {compression.skip_types.map(t => (
                  <span key={t} className="px-2 py-1 rounded-md bg-surface-2 text-sm text-slate-300 flex items-center gap-1">{t}<button onClick={() => setCompression(d => ({ ...d, skip_types: d.skip_types.filter(x => x !== t) }))}><X size={12} className="text-slate-500 hover:text-slate-300" /></button></span>
                ))}
                <div className="flex items-center gap-1">
                  <input className="input-field py-1 text-sm w-28" placeholder="添加类型" value={newSkipType} onChange={e => setNewSkipType(e.target.value)} />
                  <button className="p-1.5 rounded-md bg-surface-2 hover:bg-surface-3 text-slate-400" onClick={() => { if (newSkipType.trim()) { setCompression(d => ({ ...d, skip_types: [...d.skip_types, newSkipType.trim()] })); setNewSkipType(''); } }}><Plus size={14} /></button>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* 上传配置 */}
      {upload && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-200 font-medium"><Upload size={18} />上传配置</div>
            <button className="btn-primary flex items-center gap-1 text-sm" onClick={() => save(() => strategyApi.updateUpload(upload), '上传配置')}><Save size={14} />保存</button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            <label className="space-y-1"><span className="text-sm text-slate-400">存储类型</span><select className="input-field" value={upload.storage_class} onChange={e => setUpload(d => ({ ...d, storage_class: e.target.value as UploadConfig['storage_class'] }))}>{Object.entries(STORAGE_CLASS_MAP).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}</select></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">并发数</span><input type="number" className="input-field" value={upload.max_concurrency} onChange={e => setUpload(d => ({ ...d, max_concurrency: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">分块大小(MB)</span><input type="number" className="input-field" value={upload.chunk_size_mb} onChange={e => setUpload(d => ({ ...d, chunk_size_mb: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">重试次数</span><input type="number" className="input-field" value={upload.retry_count} onChange={e => setUpload(d => ({ ...d, retry_count: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">重试延迟(秒)</span><input type="number" className="input-field" value={upload.retry_delay_sec} onChange={e => setUpload(d => ({ ...d, retry_delay_sec: +e.target.value }))} /></label>
          </div>
        </div>
      )}

      {/* 保留策略 */}
      {retention && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-200 font-medium"><Shield size={18} />保留策略</div>
            <button className="btn-primary flex items-center gap-1 text-sm" onClick={() => save(() => strategyApi.updateRetention(retention), '保留策略')}><Save size={14} />保存</button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
            <label className="space-y-1"><span className="text-sm text-slate-400">版本保留数 <span className="text-slate-600">(1=仅最新)</span></span><input type="number" className="input-field" value={retention.version_keep_count} onChange={e => setRetention(d => ({ ...d, version_keep_count: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">孤儿数据清理天数</span><input type="number" className="input-field" value={retention.orphan_grace_days} onChange={e => setRetention(d => ({ ...d, orphan_grace_days: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">全量备份间隔(月)</span><input type="number" className="input-field" value={retention.full_reset_interval} onChange={e => setRetention(d => ({ ...d, full_reset_interval: +e.target.value }))} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">已删除文件保留天数</span><input type="number" className="input-field" value={retention.keep_deleted_days} onChange={e => setRetention(d => ({ ...d, keep_deleted_days: +e.target.value }))} /></label>
          </div>
        </div>
      )}

      {/* 加密配置 */}
      {encryption && (
        <div className="card p-6">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2 text-slate-200 font-medium"><Key size={18} />加密配置</div>
            <button className="btn-primary flex items-center gap-1 text-sm" onClick={() => save(() => strategyApi.updateEncryption(encryption), '加密配置')}><Save size={14} />保存</button>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <label className="space-y-1"><span className="text-sm text-slate-400">加密算法</span><input className="input-field bg-surface-2" readOnly value={encryption.algorithm} /></label>
            <label className="space-y-1"><span className="text-sm text-slate-400">密钥文件路径</span><input className="input-field" value={encryption.key_file_path} onChange={e => setEncryption(d => ({ ...d, key_file_path: e.target.value }))} /></label>
          </div>
        </div>
      )}
    </div>
  );
}
