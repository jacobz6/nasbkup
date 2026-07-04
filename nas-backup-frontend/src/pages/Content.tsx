import { useCallback, useEffect, useState } from 'react';
import {
  FolderOpen, Filter, Trash2, Edit2, ToggleLeft, ToggleRight,
  ChevronRight, Folder, File, ArrowUp, RefreshCw, Plus, Check, AlertCircle, ShieldCheck, ShieldOff,
} from 'lucide-react';
import { DataTable } from '@/components/ui/DataTable';
import { SlidePanel } from '@/components/ui/SlidePanel';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { LoadingSkeleton } from '@/components/shared/LoadingSkeleton';
import { useAppStore } from '@/store/useAppStore';
import {
  fsApi, directoryApi, exclusionApi,
  type BackupDirectory, type ExclusionRule, type FSEntry,
} from '@/utils/api';
import { EXCLUSION_TYPE_MAP } from '@/utils/constants';
import { formatFileSize, formatDateTime } from '@/utils/format';
import { cn } from '@/lib/utils';

const TYPE_COLORS: Record<string, string> = {
  extension: 'bg-brand-500/20 text-brand-400',
  directory: 'bg-violet-500/20 text-violet-400',
  pattern: 'bg-amber-500/20 text-amber-400',
  size_exceed: 'bg-rose-500/20 text-rose-400',
};

function EnabledBadge({ enabled }: { enabled: boolean }) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium',
      enabled ? 'bg-emerald-500/20 text-emerald-400' : 'bg-slate-500/20 text-slate-400'
    )}>
      {enabled ? <ToggleRight size={14} /> : <ToggleLeft size={14} />}
      {enabled ? '启用' : '禁用'}
    </span>
  );
}

// ── File Browser Component ──────────────────────────────────────────────

function FileBrowser() {
  const addToast = useAppStore((s) => s.addToast);
  const [currentPath, setCurrentPath] = useState('/');
  const [result, setResult] = useState<{ path: string; parent_path: string; entries: FSEntry[] } | null>(null);
  const [loading, setLoading] = useState(true);
  const [selectedEntry, setSelectedEntry] = useState<FSEntry | null>(null);
  const [backupDirs, setBackupDirs] = useState<BackupDirectory[]>([]);

  const fetchBrowse = useCallback(async (path: string) => {
    setLoading(true);
    setSelectedEntry(null);
    try {
      const res = await fsApi.browse(path);
      if (res.success && res.data) {
        setResult(res.data);
        setCurrentPath(res.data.path);
      } else {
        addToast({ type: 'error', message: res.error || '无法浏览目录' });
      }
    } catch (err) {
      addToast({ type: 'error', message: '网络错误，无法浏览目录' });
    } finally {
      setLoading(false);
    }
  }, [addToast]);

  const fetchDirs = useCallback(async () => {
    const res = await directoryApi.list();
    if (res.data) setBackupDirs(res.data);
  }, []);

  useEffect(() => { fetchBrowse('/'); fetchDirs(); }, [fetchBrowse, fetchDirs]);

  const navigateTo = (path: string) => fetchBrowse(path);

  // Breadcrumb
  const pathParts = currentPath === '/' ? ['/'] : currentPath.split('/').filter(Boolean);

  // Add directory as backup target
  const handleAddBackupDir = async (entry: FSEntry) => {
    const exists = backupDirs.some(d => d.path === entry.path);
    if (exists) {
      addToast({ type: 'info', message: '该目录已在备份列表中' });
      return;
    }
    const res = await directoryApi.create({
      path: entry.path,
      recursive: true,
      enabled: true,
      description: '',
    });
    if (res.success) {
      addToast({ type: 'success', message: `已将 ${entry.path} 添加为备份目录` });
      fetchDirs();
      fetchBrowse(currentPath);
    } else {
      addToast({ type: 'error', message: res.error || '添加失败' });
    }
  };

  // Remove directory from backup targets
  const handleRemoveBackupDir = async (entry: FSEntry) => {
    const dir = backupDirs.find(d => d.path === entry.path);
    if (!dir) return;
    const res = await directoryApi.delete(dir.id);
    if (res.success) {
      addToast({ type: 'success', message: `已将 ${entry.path} 从备份目录移除` });
      fetchDirs();
      fetchBrowse(currentPath);
    } else {
      addToast({ type: 'error', message: res.error || '移除失败' });
    }
  };

  // Toggle backup directory enabled
  const handleToggleBackupDir = async (entry: FSEntry) => {
    const dir = backupDirs.find(d => d.path === entry.path);
    if (!dir) return;
    const res = await directoryApi.update(dir.id, { ...dir, enabled: !dir.enabled });
    if (res.success) {
      addToast({ type: 'success', message: dir.enabled ? '已禁用备份' : '已启用备份' });
      fetchDirs();
      fetchBrowse(currentPath);
    } else {
      addToast({ type: 'error', message: res.error || '操作失败' });
    }
  };

  // Check if the selected entry is a direct backup target
  const isDirectBackupTarget = (entry: FSEntry) =>
    backupDirs.some(d => d.path === entry.path);

  const getBackupDirForEntry = (entry: FSEntry): BackupDirectory | undefined =>
    backupDirs.find(d => d.path === entry.path);

  return (
    <div className="card overflow-hidden">
      {/* Header with breadcrumb */}
      <div className="flex items-center justify-between px-5 py-3 border-b border-surface-3 bg-surface-2/30">
        <div className="flex items-center gap-2 text-white font-semibold">
          <FolderOpen size={18} />
          <span>文件浏览器</span>
        </div>
        <button
          onClick={() => fetchBrowse(currentPath)}
          className="p-1.5 rounded-lg hover:bg-surface-2 text-slate-400 hover:text-white transition-colors"
          title="刷新"
        >
          <RefreshCw size={16} />
        </button>
      </div>

      {/* Breadcrumb */}
      <div className="flex items-center gap-1 px-5 py-2.5 border-b border-surface-3/50 text-sm overflow-x-auto">
        {pathParts.map((part, i) => {
          const isRoot = part === '/';
          const fullPath = isRoot ? '/' : '/' + pathParts.slice(0, i + 1).join('/');
          const isLast = i === pathParts.length - 1;
          return (
            <span key={i} className="flex items-center gap-1 shrink-0">
              {i > 0 && <ChevronRight size={14} className="text-slate-600" />}
              <button
                onClick={() => !isLast && navigateTo(fullPath)}
                className={cn(
                  'px-1.5 py-0.5 rounded transition-colors',
                  isLast ? 'text-white font-medium' : 'text-slate-400 hover:text-white hover:bg-surface-2'
                )}
                disabled={isLast}
              >
                {isRoot ? '/' : part}
              </button>
            </span>
          );
        })}
      </div>

      {/* Main content: file list + detail panel */}
      <div className="flex" style={{ minHeight: 480 }}>
        {/* File list */}
        <div className="flex-1 overflow-y-auto" style={{ maxHeight: 560 }}>
          {loading ? (
            <div className="p-5"><LoadingSkeleton rows={10} /></div>
          ) : !result || result.entries.length === 0 ? (
            <div>
              {/* Parent directory link — keep navigation available in empty directories */}
              {result?.parent_path && (
                <table className="w-full text-sm">
                  <tbody>
                    <tr
                      onClick={() => navigateTo(result.parent_path)}
                      className="border-b border-surface-3/30 cursor-pointer hover:bg-surface-2/30 transition-colors"
                    >
                      <td className="py-2.5 px-5 flex items-center gap-2.5 text-slate-400">
                        <ArrowUp size={16} className="shrink-0" />
                        <span>..</span>
                      </td>
                      <td className="py-2.5 px-3 text-slate-500">—</td>
                      <td className="py-2.5 px-3"></td>
                      <td className="py-2.5 px-5"></td>
                    </tr>
                  </tbody>
                </table>
              )}
              <div className="flex flex-col items-center justify-center py-16 text-slate-500">
                <Folder size={40} strokeWidth={1} className="mb-3 text-slate-600" />
                <p>空目录</p>
              </div>
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-surface-3/50 text-xs text-slate-500 uppercase tracking-wider">
                  <th className="text-left py-2.5 px-5 font-medium">名称</th>
                  <th className="text-left py-2.5 px-3 font-medium w-24">大小</th>
                  <th className="text-left py-2.5 px-3 font-medium w-20">状态</th>
                  <th className="text-right py-2.5 px-5 font-medium w-10"></th>
                </tr>
              </thead>
              <tbody>
                {/* Parent directory link */}
                {result.parent_path && (
                  <tr
                    onClick={() => navigateTo(result.parent_path)}
                    className="border-b border-surface-3/30 cursor-pointer hover:bg-surface-2/30 transition-colors"
                  >
                    <td className="py-2.5 px-5 flex items-center gap-2.5 text-slate-400">
                      <ArrowUp size={16} className="shrink-0" />
                      <span>..</span>
                    </td>
                    <td className="py-2.5 px-3 text-slate-500">—</td>
                    <td className="py-2.5 px-3"></td>
                    <td className="py-2.5 px-5"></td>
                  </tr>
                )}
                {result.entries.map((entry) => {
                  const isSelected = selectedEntry?.path === entry.path;
                  return (
                    <tr
                      key={entry.path}
                      onClick={() => setSelectedEntry(entry)}
                      onDoubleClick={() => entry.is_dir && navigateTo(entry.path)}
                      className={cn(
                        'border-b border-surface-3/30 transition-colors cursor-pointer',
                        isSelected ? 'bg-brand-500/10' : 'hover:bg-surface-2/30'
                      )}
                    >
                      <td className="py-2.5 px-5">
                        <div className="flex items-center gap-2.5">
                          {entry.is_dir ? (
                            <Folder size={16} className="text-amber-400 shrink-0" />
                          ) : (
                            <File size={16} className="text-slate-500 shrink-0" />
                          )}
                          <span className={cn('truncate', entry.is_dir ? 'text-white font-medium' : 'text-slate-300')}>
                            {entry.name}
                          </span>
                          {entry.is_dir && entry.in_backup && (
                            entry.partial_backup ? (
                              <span title="部分备份范围内"><AlertCircle size={14} className="text-amber-400 shrink-0" /></span>
                            ) : (
                              <span title="备份范围内"><ShieldCheck size={14} className="text-emerald-400 shrink-0" /></span>
                            )
                          )}
                        </div>
                      </td>
                      <td className="py-2.5 px-3 text-slate-500 font-mono text-xs">
                        {entry.is_dir ? '—' : formatFileSize(entry.size)}
                      </td>
                      <td className="py-2.5 px-3">
                        {entry.in_backup && entry.partial_backup ? (
                          <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs bg-amber-500/15 text-amber-400">
                            <AlertCircle size={10} /> 部分已纳入
                          </span>
                        ) : entry.in_backup ? (
                          <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs bg-emerald-500/15 text-emerald-400">
                            <Check size={10} /> 已纳入
                          </span>
                        ) : (
                          <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs bg-slate-500/15 text-slate-500">
                            未纳入
                          </span>
                        )}
                      </td>
                      <td className="py-2.5 px-5 text-right">
                        {entry.is_dir && (
                          <ChevronRight size={16} className="text-slate-600" />
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>

        {/* Right detail panel */}
        {selectedEntry && (
          <div className="w-72 border-l border-surface-3 bg-surface-2/20 p-5 flex flex-col gap-5 overflow-y-auto">
            {/* Entry info */}
            <div>
              <div className="flex items-center gap-2 mb-3">
                {selectedEntry.is_dir ? (
                  <Folder size={20} className="text-amber-400" />
                ) : (
                  <File size={20} className="text-slate-400" />
                )}
                <h4 className="text-white font-semibold truncate" title={selectedEntry.name}>
                  {selectedEntry.name}
                </h4>
              </div>
              <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                  <span className="text-slate-500">路径</span>
                  <span className="text-slate-300 font-mono text-xs truncate ml-2 max-w-[160px]" title={selectedEntry.path}>
                    {selectedEntry.path}
                  </span>
                </div>
                {!selectedEntry.is_dir && (
                  <div className="flex justify-between">
                    <span className="text-slate-500">大小</span>
                    <span className="text-slate-300 font-mono text-xs">{formatFileSize(selectedEntry.size)}</span>
                  </div>
                )}
                <div className="flex justify-between">
                  <span className="text-slate-500">修改时间</span>
                  <span className="text-slate-300 text-xs">{formatDateTime(selectedEntry.mod_time)}</span>
                </div>
              </div>
            </div>

            {/* Backup status */}
            <div className="space-y-3">
              <h5 className="text-xs font-medium text-slate-500 uppercase tracking-wider">备份状态</h5>

              <div className="flex items-center justify-between">
                <span className="text-sm text-slate-400">备份范围内</span>
                {selectedEntry.in_backup && selectedEntry.partial_backup ? (
                  <span className="flex items-center gap-1 text-amber-400 text-sm"><AlertCircle size={14} /> 部分</span>
                ) : selectedEntry.in_backup ? (
                  <span className="flex items-center gap-1 text-emerald-400 text-sm"><ShieldCheck size={14} /> 是</span>
                ) : (
                  <span className="flex items-center gap-1 text-slate-500 text-sm"><ShieldOff size={14} /> 否</span>
                )}
              </div>

              {!selectedEntry.is_dir && (
                <div className="flex items-center justify-between">
                  <span className="text-sm text-slate-400">有更新</span>
                  {selectedEntry.has_update ? (
                    <span className="flex items-center gap-1 text-amber-400 text-sm"><AlertCircle size={14} /> 是</span>
                  ) : (
                    <span className="flex items-center gap-1 text-slate-500 text-sm"><Check size={14} /> 无</span>
                  )}
                </div>
              )}

              <div className="flex items-center justify-between">
                <span className="text-sm text-slate-400">下次备份纳入</span>
                {selectedEntry.will_backup ? (
                  <span className="flex items-center gap-1 text-brand-400 text-sm"><Check size={14} /> 是</span>
                ) : (
                  <span className="flex items-center gap-1 text-slate-500 text-sm"><ShieldOff size={14} /> 否</span>
                )}
              </div>
            </div>

            {/* Actions */}
            {selectedEntry.is_dir && (
              <div className="space-y-3">
                <h5 className="text-xs font-medium text-slate-500 uppercase tracking-wider">操作</h5>

                {isDirectBackupTarget(selectedEntry) ? (
                  <>
                    <button
                      onClick={() => handleToggleBackupDir(selectedEntry)}
                      className={cn(
                        'w-full flex items-center justify-center gap-2 px-3 py-2 rounded-lg text-sm font-medium transition-all',
                        getBackupDirForEntry(selectedEntry)?.enabled
                          ? 'btn-secondary'
                          : 'btn-primary'
                      )}
                    >
                      {getBackupDirForEntry(selectedEntry)?.enabled ? (
                        <><ToggleLeft size={16} /> 禁用备份</>
                      ) : (
                        <><ToggleRight size={16} /> 启用备份</>
                      )}
                    </button>
                    <button
                      onClick={() => handleRemoveBackupDir(selectedEntry)}
                      className="btn-danger w-full flex items-center justify-center gap-2"
                    >
                      <Trash2 size={16} /> 移除备份目录
                    </button>
                  </>
                ) : (
                  <button
                    onClick={() => handleAddBackupDir(selectedEntry)}
                    className="btn-primary w-full flex items-center justify-center gap-2"
                  >
                    <Plus size={16} /> 设为备份目录
                  </button>
                )}

                <button
                  onClick={() => navigateTo(selectedEntry.path)}
                  className="btn-secondary w-full flex items-center justify-center gap-2"
                >
                  <FolderOpen size={16} /> 进入目录
                </button>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Main Content Page ───────────────────────────────────────────────────

export function Content() {
  const addToast = useAppStore((s) => s.addToast);
  const [exclusions, setExclusions] = useState<ExclusionRule[]>([]);
  const [loading, setLoading] = useState(true);

  // Exclusion panel
  const [excPanel, setExcPanel] = useState<{ open: boolean; edit?: ExclusionRule }>({ open: false });
  const [excForm, setExcForm] = useState({ pattern: '', rule_type: 'extension' as ExclusionRule['rule_type'], enabled: true });

  // Confirm dialog
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; message: string; onConfirm: () => void }>({ open: false, title: '', message: '', onConfirm: () => {} });

  const fetchExcs = useCallback(() => exclusionApi.list().then((r) => { if (r.data) setExclusions(r.data); }), []);

  useEffect(() => {
    setLoading(true);
    fetchExcs().finally(() => setLoading(false));
  }, [fetchExcs]);

  // --- Exclusion handlers ---
  const openExcPanel = (edit?: ExclusionRule) => {
    if (edit) {
      setExcForm({ pattern: edit.pattern, rule_type: edit.rule_type, enabled: edit.enabled });
    } else {
      setExcForm({ pattern: '', rule_type: 'extension', enabled: true });
    }
    setExcPanel({ open: true, edit });
  };

  const saveExc = async () => {
    if (!excForm.pattern.trim()) return;
    const res = excPanel.edit
      ? await exclusionApi.update(excPanel.edit.id, excForm)
      : await exclusionApi.create(excForm);
    if (res.success) {
      addToast({ type: 'success', message: excPanel.edit ? '规则已更新' : '规则已添加' });
      setExcPanel({ open: false });
      fetchExcs();
    } else {
      addToast({ type: 'error', message: res.error || '操作失败' });
    }
  };

  const deleteExc = (e: ExclusionRule) => {
    setConfirm({
      open: true,
      title: '删除规则',
      message: `确定要删除规则 "${e.pattern}" 吗？`,
      onConfirm: async () => {
        const res = await exclusionApi.delete(e.id);
        if (res.success) { addToast({ type: 'success', message: '规则已删除' }); fetchExcs(); }
        else addToast({ type: 'error', message: res.error || '删除失败' });
        setConfirm((p) => ({ ...p, open: false }));
      },
    });
  };

  const toggleExc = async (e: ExclusionRule) => {
    const res = await exclusionApi.update(e.id, { enabled: !e.enabled });
    if (res.success) { addToast({ type: 'success', message: e.enabled ? '已禁用' : '已启用' }); fetchExcs(); }
    else addToast({ type: 'error', message: res.error || '操作失败' });
  };

  const excColumns = [
    { key: 'pattern', header: '模式', render: (r: ExclusionRule) => <span className="font-mono text-sm">{r.pattern}</span> },
    { key: 'rule_type', header: '类型', render: (r: ExclusionRule) => (
      <span className={cn('px-2 py-0.5 rounded-full text-xs font-medium', TYPE_COLORS[r.rule_type] || 'bg-slate-500/20 text-slate-400')}>
        {EXCLUSION_TYPE_MAP[r.rule_type]?.label || r.rule_type}
      </span>
    )},
    { key: 'enabled', header: '启用', render: (r: ExclusionRule) => <EnabledBadge enabled={r.enabled} /> },
    { key: 'actions', header: '操作', render: (r: ExclusionRule) => (
      <div className="flex items-center gap-1">
        <button onClick={() => openExcPanel(r)} className="p-1.5 rounded-lg hover:bg-surface-2 text-slate-400 hover:text-white transition-colors"><Edit2 size={15} /></button>
        <button onClick={() => toggleExc(r)} className="p-1.5 rounded-lg hover:bg-surface-2 text-slate-400 hover:text-white transition-colors"><ToggleLeft size={15} /></button>
        <button onClick={() => deleteExc(r)} className="p-1.5 rounded-lg hover:bg-surface-2 text-slate-400 hover:text-rose-400 transition-colors"><Trash2 size={15} /></button>
      </div>
    )},
  ];

  if (loading) return <div className="space-y-6"><LoadingSkeleton rows={5} /><LoadingSkeleton rows={5} /></div>;

  return (
    <div className="space-y-6">
      {/* File Browser */}
      <FileBrowser />

      {/* Exclusion Rules */}
      <div className="card">
        <div className="flex items-center justify-between p-5 border-b border-surface-3">
          <div className="flex items-center gap-2 text-white font-semibold"><Filter size={18} />排除规则</div>
          <button onClick={() => openExcPanel()} className="btn-primary flex items-center gap-1.5 text-sm"><Plus size={16} />添加规则</button>
        </div>
        <DataTable columns={excColumns} data={exclusions} rowKey={(r) => r.id} />
      </div>

      {/* Exclusion panel */}
      <SlidePanel open={excPanel.open} onClose={() => setExcPanel({ open: false })} title={excPanel.edit ? '编辑规则' : '添加规则'}>
        <div className="space-y-4">
          <div>
            <label className="block text-sm text-slate-400 mb-1.5">模式 <span className="text-rose-400">*</span></label>
            <input value={excForm.pattern} onChange={(e) => setExcForm((f) => ({ ...f, pattern: e.target.value }))} className="input-field" placeholder="*.tmp, node_modules/" />
          </div>
          <div>
            <label className="block text-sm text-slate-400 mb-1.5">类型</label>
            <select value={excForm.rule_type} onChange={(e) => setExcForm((f) => ({ ...f, rule_type: e.target.value as ExclusionRule['rule_type'] }))} className="input-field">
              {Object.entries(EXCLUSION_TYPE_MAP).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
            </select>
          </div>
          <div className="flex items-center justify-between">
            <label className="text-sm text-slate-400">启用</label>
            <button onClick={() => setExcForm((f) => ({ ...f, enabled: !f.enabled }))} className={cn('relative w-10 h-5 rounded-full transition-colors', excForm.enabled ? 'bg-brand-500' : 'bg-slate-600')}>
              <span className={cn('absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform', excForm.enabled ? 'left-5' : 'left-0.5')} />
            </button>
          </div>
          <button onClick={saveExc} className="btn-primary w-full mt-4">保存</button>
        </div>
      </SlidePanel>

      <ConfirmDialog open={confirm.open} onClose={() => setConfirm((p) => ({ ...p, open: false }))} onConfirm={confirm.onConfirm} title={confirm.title} message={confirm.message} />
    </div>
  );
}
