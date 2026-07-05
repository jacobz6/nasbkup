import { useState } from 'react';
import {
  RefreshCw,
  ShieldCheck,
  AlertTriangle,
  CheckCircle2,
  Info,
  Loader2,
  FileWarning,
  Database,
  CloudOff,
  Hash,
  ClipboardCheck,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useAppStore } from '@/store/useAppStore';
import { reconcileApi, type ReconcileReport } from '@/utils/api';
import { LoadingSkeleton } from '@/components/shared/LoadingSkeleton';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';

// summary describes one bucket of inconsistencies found by the reconciler.
interface SummaryItem {
  key: string;
  label: string;
  count: number;
  // severity controls the card accent color.
  //   danger  → red   (data loss / unrecoverable)
  //   warning → amber (drift, fixable)
  //   info    → slate (informational)
  severity: 'danger' | 'warning' | 'info';
  // hint shown when count is 0.
  hint: string;
}

// buildSummaries maps a report into the cards shown at the top of the page.
function buildSummaries(r: ReconcileReport): SummaryItem[] {
  return [
    {
      key: 'oss_only',
      label: 'OSS 孤儿对象',
      count: r.oss_only_orphans?.length ?? 0,
      severity: 'warning',
      hint: 'OSS 有对象但 DB 无 hash_index 记录（上传后崩溃遗留）',
    },
    {
      key: 'dangling_ref0',
      label: '悬挂索引（可清理）',
      count: r.dangling_hash_indexes_ref_zero?.length ?? 0,
      severity: 'info',
      hint: 'hash_index.storage_key 在 OSS 已丢失且 ref_count=0',
    },
    {
      key: 'dangling_refn',
      label: '悬挂索引（数据丢失）',
      count: r.dangling_hash_indexes_ref_nonzero?.length ?? 0,
      severity: 'danger',
      hint: 'hash_index 引用但 OSS 对象不存在，无法自动修复',
    },
    {
      key: 'orphan_bf',
      label: '孤儿 backup_files',
      count: r.orphan_backup_files?.length ?? 0,
      severity: 'warning',
      hint: 'backup_files 引用的 storage_key 在 OSS 和 hash_index 中都不存在',
    },
    {
      key: 'bf_missing_in_oss',
      label: '索引缺失（可修复）',
      count: r.backup_files_missing_hash_index_but_in_oss?.length ?? 0,
      severity: 'warning',
      hint: 'backup_files 缺 hash_index 但 OSS 对象存在，可重建索引',
    },
    {
      key: 'ref_drift',
      label: 'ref_count 漂移',
      count: r.ref_count_mismatches?.length ?? 0,
      severity: 'warning',
      hint: 'hash_index.ref_count 与实际活跃文件数不一致',
    },
    {
      key: 'failed_with_files',
      label: '失败备份但已上传',
      count: r.failed_backups_with_files?.length ?? 0,
      severity: 'info',
      hint: '备份状态为 failed 但 backup_files 全部存在于 OSS',
    },
    {
      key: 'completed_no_files',
      label: '完成备份但无文件',
      count: r.completed_backups_no_files?.length ?? 0,
      severity: 'info',
      hint: '备份状态为 completed 但没有任何 backup_files',
    },
  ];
}

const severityStyles = {
  danger: 'border-rose-500/30 bg-rose-500/5',
  warning: 'border-amber-500/30 bg-amber-500/5',
  info: 'border-surface-3 bg-surface-2/50',
};

const severityDot = {
  danger: 'bg-rose-400',
  warning: 'bg-amber-400',
  info: 'bg-slate-400',
};

export function Reconcile() {
  const addToast = useAppStore((s) => s.addToast);
  const [report, setReport] = useState<ReconcileReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [confirm, setConfirm] = useState<{
    open: boolean;
    title: string;
    message: string;
    onConfirm: () => void;
    variant: 'danger' | 'warning';
  }>({ open: false, title: '', message: '', onConfirm: () => {}, variant: 'warning' });

  const runReconcile = async (dryRun: boolean) => {
    setLoading(true);
    try {
      const res = await reconcileApi.run(dryRun);
      if (res.success && res.data) {
        // Normalize nullable arrays: Go nil slices serialize as JSON null,
        // which would crash .length / .map accesses below. Coalesce to [].
        const r = res.data;
        r.oss_only_orphans ??= [];
        r.dangling_hash_indexes_ref_zero ??= [];
        r.dangling_hash_indexes_ref_nonzero ??= [];
        r.orphan_backup_files ??= [];
        r.backup_files_missing_hash_index_but_in_oss ??= [];
        r.ref_count_mismatches ??= [];
        r.failed_backups_with_files ??= [];
        r.completed_backups_no_files ??= [];
        r.applied_fixes ??= [];
        r.skipped_fixes ??= [];
        r.errors ??= [];
        setReport(r);
        if (dryRun) {
          addToast({ type: 'success', message: '对账扫描完成（预览模式，未做任何修改）' });
        } else {
          addToast({ type: 'success', message: `对账完成：应用 ${r.applied_fixes.length} 项修复` });
        }
      } else {
        addToast({ type: 'error', message: res.error || '对账失败' });
      }
    } catch (e) {
      addToast({ type: 'error', message: '网络错误，请确保后端服务已启动' });
    } finally {
      setLoading(false);
      setConfirm((c) => ({ ...c, open: false }));
    }
  };

  const openDryRun = () => runReconcile(true);

  const openApplyConfirm = () => {
    // If there's no current report yet, ask the user to run a dry-run first.
    if (!report) {
      runReconcile(false);
      return;
    }
    setConfirm({
      open: true,
      title: '确认执行对账修复',
      message:
        '将根据当前报告实际修改数据库与 OSS 对象。建议先运行预览确认无误后再执行。是否继续？',
      onConfirm: () => runReconcile(false),
      variant: 'danger',
    });
  };

  return (
    <div className="space-y-6">
      {/* Header card */}
      <div className="card p-6">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="p-2.5 rounded-lg bg-brand-500/10 text-brand-400">
              <ShieldCheck size={22} />
            </div>
            <div>
              <h2 className="text-lg font-semibold text-white">系统对账</h2>
              <p className="text-sm text-slate-400 mt-0.5">
                检测并修复 OSS 对象 / hash_index / backup_files 之间的不一致
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              className="btn-secondary flex items-center gap-2"
              onClick={openDryRun}
              disabled={loading}
            >
              {loading ? <Loader2 size={16} className="animate-spin" /> : <RefreshCw size={16} />}
              预览扫描
            </button>
            <button
              className="btn-danger flex items-center gap-2"
              onClick={openApplyConfirm}
              disabled={loading}
            >
              <ShieldCheck size={16} />
              执行修复
            </button>
          </div>
        </div>
        <div className="mt-4 flex items-start gap-2 text-xs text-slate-500 bg-surface-2/50 border border-surface-3 rounded-lg p-3">
          <Info size={14} className="shrink-0 mt-0.5 text-slate-400" />
          <div>
            预览模式仅扫描并报告不一致，不修改任何数据；执行修复会实际删除孤儿对象、纠正 ref_count 和备份状态。
            若有备份正在运行，对账会返回 409，请稍后重试。
          </div>
        </div>
      </div>

      {/* Loading skeleton */}
      {loading && !report && (
        <div className="card p-6">
          <LoadingSkeleton rows={4} />
        </div>
      )}

      {/* Report */}
      {report && (
        <>
          {/* Summary cards */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            {buildSummaries(report).map((item) => (
              <div
                key={item.key}
                className={cn(
                  'card p-4 border',
                  severityStyles[item.severity],
                  item.count === 0 && 'opacity-60'
                )}
              >
                <div className="flex items-center gap-2 mb-2">
                  <span className={cn('h-2 w-2 rounded-full', severityDot[item.severity])} />
                  <span className="text-xs text-slate-400">{item.label}</span>
                </div>
                <div className="text-2xl font-mono font-bold text-white">
                  {item.count}
                </div>
                <div className="text-xs text-slate-500 mt-1.5 leading-snug">{item.hint}</div>
              </div>
            ))}
          </div>

          {/* Run metadata */}
          <div className="card p-4 flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
            <div className="flex items-center gap-2">
              {report.dry_run ? (
                <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-amber-500/10 text-amber-400 text-xs font-medium">
                  <AlertTriangle size={12} /> 预览模式
                </span>
              ) : (
                <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-emerald-500/10 text-emerald-400 text-xs font-medium">
                  <CheckCircle2 size={12} /> 已应用修复
                </span>
              )}
            </div>
            <div className="text-slate-400">
              用时 <span className="font-mono text-slate-200">{report.duration || '-'}</span>
            </div>
            {report.errors && report.errors.length > 0 && (
              <div className="text-rose-400 flex items-center gap-1.5">
                <AlertTriangle size={14} />
                {report.errors.length} 个错误
              </div>
            )}
          </div>

          {/* Detailed sections */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <DetailList
              title="OSS 孤儿对象"
              icon={<CloudOff size={16} />}
              items={report.oss_only_orphans}
              accent="warning"
              emptyText="没有孤儿对象，OSS 与 hash_index 一致"
            />
            <DetailList
              title="悬挂索引（ref_count=0，可清理）"
              icon={<Database size={16} />}
              items={report.dangling_hash_indexes_ref_zero}
              accent="info"
              emptyText="没有可清理的悬挂索引"
            />
            <DetailList
              title="悬挂索引（ref_count>0，数据丢失）"
              icon={<FileWarning size={16} />}
              items={report.dangling_hash_indexes_ref_nonzero}
              accent="danger"
              emptyText="没有不可修复的悬挂索引"
            />
            <DetailList
              title="孤儿 backup_files（OSS 与 hash_index 均无）"
              icon={<FileWarning size={16} />}
              items={report.orphan_backup_files}
              accent="warning"
              emptyText="backup_files 与索引完全一致"
            />
            <DetailList
              title="缺失 hash_index（OSS 存在，可重建）"
              icon={<Hash size={16} />}
              items={report.backup_files_missing_hash_index_but_in_oss}
              accent="warning"
              emptyText="所有 backup_files 均有对应 hash_index"
            />
            <RefCountTable items={report.ref_count_mismatches} />
            <StatusFixTable
              title="失败备份（实际已上传）"
              icon={<ClipboardCheck size={16} />}
              items={report.failed_backups_with_files}
            />
            <StatusFixTable
              title="完成备份（无文件）"
              icon={<ClipboardCheck size={16} />}
              items={report.completed_backups_no_files}
            />
          </div>

          {/* Outcome log */}
          {(report.applied_fixes.length > 0 || report.skipped_fixes.length > 0) && (
            <div className="card p-6">
              <h3 className="flex items-center gap-2 text-sm font-medium text-slate-200 mb-3">
                <ClipboardCheck size={16} />
                {report.dry_run ? '将执行的修改（预览）' : '已执行的修改'}
              </h3>
              {report.applied_fixes.length > 0 && (
                <LogList
                  title={`已应用 (${report.applied_fixes.length})`}
                  items={report.applied_fixes}
                  icon={<CheckCircle2 size={14} className="text-emerald-400" />}
                />
              )}
              {report.skipped_fixes.length > 0 && (
                <LogList
                  title={`已跳过 (${report.skipped_fixes.length})`}
                  items={report.skipped_fixes}
                  icon={<Info size={14} className="text-slate-400" />}
                />
              )}
            </div>
          )}

          {/* Errors */}
          {report.errors.length > 0 && (
            <div className="card p-6 border-rose-500/30">
              <h3 className="flex items-center gap-2 text-sm font-medium text-rose-300 mb-3">
                <AlertTriangle size={16} />
                对账错误 ({report.errors.length})
              </h3>
              <ul className="space-y-1.5">
                {report.errors.map((e, i) => (
                  <li key={i} className="text-xs font-mono text-rose-300/90 bg-rose-500/5 rounded p-2 break-all">
                    {e}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}

      {/* Empty state */}
      {!loading && !report && (
        <div className="card p-12 flex flex-col items-center justify-center text-center">
          <div className="p-4 rounded-full bg-surface-2 mb-4">
            <ShieldCheck size={32} className="text-slate-500" />
          </div>
          <h3 className="text-base font-medium text-slate-300 mb-2">尚未执行对账</h3>
          <p className="text-sm text-slate-500 max-w-md">
            点击上方"预览扫描"以检测 OSS、hash_index、backup_files 三方数据源之间的不一致。
            预览不修改任何数据，可安全运行。
          </p>
        </div>
      )}

      <ConfirmDialog
        open={confirm.open}
        onClose={() => setConfirm((c) => ({ ...c, open: false }))}
        onConfirm={confirm.onConfirm}
        title={confirm.title}
        message={confirm.message}
        variant={confirm.variant}
        confirmText="执行修复"
        loading={loading}
      />
    </div>
  );
}

// DetailList renders a list of storage_key strings inside a card.
function DetailList({
  title,
  icon,
  items,
  accent,
  emptyText,
}: {
  title: string;
  icon: React.ReactNode;
  items: string[] | undefined;
  accent: 'danger' | 'warning' | 'info';
  emptyText: string;
}) {
  const list = items ?? [];
  const accentBorder = {
    danger: 'border-rose-500/20',
    warning: 'border-amber-500/20',
    info: 'border-surface-3',
  }[accent];

  return (
    <div className={cn('card p-5 border', accentBorder)}>
      <div className="flex items-center justify-between mb-3">
        <h3 className="flex items-center gap-2 text-sm font-medium text-slate-200">
          <span className="text-slate-400">{icon}</span>
          {title}
        </h3>
        <span className="text-xs font-mono text-slate-400">{list.length}</span>
      </div>
      {list.length === 0 ? (
        <p className="text-xs text-slate-500 py-3">{emptyText}</p>
      ) : (
        <ul className="space-y-1 max-h-60 overflow-y-auto pr-1">
          {list.map((k, i) => (
            <li
              key={i}
              className="text-xs font-mono text-slate-300 bg-surface-2/60 rounded px-2 py-1.5 truncate"
              title={k}
            >
              {k}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// RefCountTable renders ref_count drift as a small table.
function RefCountTable({ items }: { items: { hash: string; stored_in_db: number; actual_active: number }[] | undefined }) {
  const list = items ?? [];
  return (
    <div className="card p-5 border border-amber-500/20">
      <div className="flex items-center justify-between mb-3">
        <h3 className="flex items-center gap-2 text-sm font-medium text-slate-200">
          <span className="text-slate-400"><Hash size={16} /></span>
          ref_count 漂移
        </h3>
        <span className="text-xs font-mono text-slate-400">{list.length}</span>
      </div>
      {list.length === 0 ? (
        <p className="text-xs text-slate-500 py-3">所有 hash 的 ref_count 与实际一致</p>
      ) : (
        <div className="overflow-x-auto max-h-60">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-left text-slate-500 border-b border-surface-3">
                <th className="pb-1.5 font-medium">Hash</th>
                <th className="pb-1.5 font-medium text-right">DB</th>
                <th className="pb-1.5 font-medium text-right">实际</th>
              </tr>
            </thead>
            <tbody>
              {list.map((m, i) => (
                <tr key={i} className="border-b border-surface-3/50 last:border-0">
                  <td className="py-1.5 font-mono text-slate-300 truncate max-w-[160px]" title={m.hash}>
                    {m.hash.length > 16 ? `${m.hash.slice(0, 8)}…${m.hash.slice(-6)}` : m.hash}
                  </td>
                  <td className="py-1.5 text-right font-mono text-rose-300">{m.stored_in_db}</td>
                  <td className="py-1.5 text-right font-mono text-emerald-300">{m.actual_active}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// StatusFixTable renders proposed backup status corrections.
function StatusFixTable({
  title,
  icon,
  items,
}: {
  title: string;
  icon: React.ReactNode;
  items: { backup_id: number; from: string; to: string; reason: string }[] | undefined;
}) {
  const list = items ?? [];
  return (
    <div className="card p-5 border border-surface-3">
      <div className="flex items-center justify-between mb-3">
        <h3 className="flex items-center gap-2 text-sm font-medium text-slate-200">
          <span className="text-slate-400">{icon}</span>
          {title}
        </h3>
        <span className="text-xs font-mono text-slate-400">{list.length}</span>
      </div>
      {list.length === 0 ? (
        <p className="text-xs text-slate-500 py-3">无备份状态需要校正</p>
      ) : (
        <ul className="space-y-2 max-h-60 overflow-y-auto pr-1">
          {list.map((f, i) => (
            <li key={i} className="bg-surface-2/60 rounded p-2 text-xs">
              <div className="flex items-center gap-2 mb-1">
                <span className="font-mono text-slate-300">#{f.backup_id}</span>
                <span className="font-mono text-rose-300">{f.from}</span>
                <span className="text-slate-500">→</span>
                <span className="font-mono text-emerald-300">{f.to}</span>
              </div>
              <div className="text-slate-400 leading-snug">{f.reason}</div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// LogList renders a list of strings with an icon, used for applied/skipped fixes.
function LogList({
  title,
  items,
  icon,
}: {
  title: string;
  items: string[];
  icon: React.ReactNode;
}) {
  return (
    <div className="mb-4 last:mb-0">
      <div className="flex items-center gap-2 mb-2 text-xs font-medium text-slate-400">
        {icon}
        {title}
      </div>
      <ul className="space-y-1 max-h-48 overflow-y-auto">
        {items.map((line, i) => (
          <li key={i} className="text-xs font-mono text-slate-300 bg-surface-2/40 rounded px-2 py-1 break-all">
            {line}
          </li>
        ))}
      </ul>
    </div>
  );
}
