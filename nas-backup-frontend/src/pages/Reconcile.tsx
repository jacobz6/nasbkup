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
  AlertOctagon,
  ArrowRightCircle,
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

// Recommendation captures a single advisory produced after a reconcile run.
// Ordered by level: info < warning < danger < critical. The UI sorts the
// list so the most severe issues surface first.
interface Recommendation {
  // level controls both color and ordering. critical always appears on top.
  level: 'info' | 'warning' | 'danger' | 'critical';
  title: string;
  // description explains what the inconsistency means and how it arose.
  description: string;
  // action is the suggested fix. For autoFixable=true items, this is what
  // "执行修复" will do. For autoFixable=false, it is a manual operation
  // (e.g. trigger a full backup).
  action: string;
  // autoFixable indicates whether the "执行修复" button can resolve this
  // automatically. false means manual intervention is required.
  autoFixable: boolean;
}

// levelOrder is used to sort recommendations from severe to light.
const levelOrder: Record<Recommendation['level'], number> = {
  critical: 0,
  danger: 1,
  warning: 2,
  info: 3,
};

// buildRecommendations maps a report into an ordered list of actionable
// suggestions. The list is sorted from most severe to least severe so the
// operator sees data-loss issues first.
function buildRecommendations(r: ReconcileReport): Recommendation[] {
  const recs: Recommendation[] = [];

  // ── Level 4 (critical): data loss requiring full backup ─────────────
  // ref_count > 0 但 OSS 对象不存在 = 这些文件的数据已永久丢失，
  // 但 hash_index 仍认为有备份。任何 restore 尝试都会失败。
  // 唯一出路是重新全量备份受影响文件，重建 hash → OSS 映射。
  if (r.dangling_hash_indexes_ref_nonzero.length > 0) {
    recs.push({
      level: 'critical',
      title: '数据已永久丢失：hash_index 引用的 OSS 对象不存在',
      description:
        `${r.dangling_hash_indexes_ref_nonzero.length} 个 hash_index 记录的 ref_count > 0 ` +
        `（仍有活跃文件依赖这些 hash），但对应的 OSS 对象已丢失。` +
        `这意味着这些文件的备份数据已无法恢复，restore 时会失败。`,
      action:
        '此问题无法通过对账自动修复（删除会破坏依赖关系）。' +
        '请立即触发一次全量备份，重新上传所有活跃文件，' +
        '重建 hash_index 与 OSS 的对应关系。全量备份后再次运行对账以确认修复。',
      autoFixable: false,
    });
  }

  // ── Level 3 (danger): partial data loss ────────────────────────────
  // backup_files 指向的 storage_key 在 OSS 和 hash_index 都不存在，
  // 这些 backup_files 永远无法 restore。
  if (r.orphan_backup_files.length > 0) {
    recs.push({
      level: 'danger',
      title: '孤儿 backup_files：对应数据已丢失',
      description:
        `${r.orphan_backup_files.length} 个 backup_files 引用的 storage_key 在 OSS 和 hash_index 中都不存在。` +
        `这些行对应的文件数据已永久丢失，相关备份的恢复能力已受损。`,
      action:
        '执行修复将删除这些 backup_files 行（清理引用）。' +
        '建议尽快触发一次全量备份以重建数据冗余。',
      autoFixable: true,
    });
  }

  // ── Level 2 (warning): drift, auto-fixable but review recommended ──
  if (r.oss_only_orphans.length > 0) {
    recs.push({
      level: 'warning',
      title: 'OSS 孤儿对象（占用存储空间）',
      description:
        `${r.oss_only_orphans.length} 个 OSS 对象在 hash_index 中无记录，` +
        `通常是上传成功后写 DB 前进程崩溃的遗留物。`,
      action:
        '执行修复将删除这些 OSS 对象，释放存储空间。无数据损失风险（DB 无引用）。',
      autoFixable: true,
    });
  }

  if (r.backup_files_missing_hash_index_but_in_oss.length > 0) {
    recs.push({
      level: 'warning',
      title: 'hash_index 缺失（可重建）',
      description:
        `${r.backup_files_missing_hash_index_but_in_oss.length} 个 backup_files 引用的 storage_key ` +
        `在 hash_index 中缺失，但 OSS 对象存在。restore 仍可工作，但 GC 可能误删这些对象。`,
      action:
        '执行修复将用合成 hash 重建 hash_index 行，恢复 backup_files ↔ hash_index 一致性。',
      autoFixable: true,
    });
  }

  if (r.ref_count_mismatches.length > 0) {
    recs.push({
      level: 'warning',
      title: 'ref_count 漂移',
      description:
        `${r.ref_count_mismatches.length} 个 hash 的 ref_count 与实际活跃文件数不一致。` +
        `这可能导致 GC 误删仍被引用的对象（ref_count 偏低），或漏删孤儿（ref_count 偏高）。`,
      action: '执行修复将根据 files 表重建 ref_count。',
      autoFixable: true,
    });
  }

  // ── Level 1 (info): trivial status corrections ─────────────────────
  if (r.dangling_hash_indexes_ref_zero.length > 0) {
    recs.push({
      level: 'info',
      title: '悬挂索引（ref_count=0，可清理）',
      description:
        `${r.dangling_hash_indexes_ref_zero.length} 条 hash_index 记录的 OSS 对象已丢失且 ref_count=0，` +
        `属于历史 GC 残留。`,
      action: '执行修复将自动删除这些 hash_index 行，无数据风险。',
      autoFixable: true,
    });
  }

  if (r.failed_backups_with_files.length > 0) {
    recs.push({
      level: 'info',
      title: '失败备份状态校正',
      description:
        `${r.failed_backups_with_files.length} 个备份标记为 failed 但实际已上传所有文件（已验证 OSS 存在），` +
        `属于状态更新失败的误标。`,
      action: '执行修复将把这些备份状态改为 completed。',
      autoFixable: true,
    });
  }

  if (r.completed_backups_no_files.length > 0) {
    recs.push({
      level: 'info',
      title: '空完成备份状态校正',
      description:
        `${r.completed_backups_no_files.length} 个备份标记为 completed 但没有任何 backup_files，` +
        `属于状态更新异常。`,
      action: '执行修复将把这些备份状态改为 failed，反映真实情况。',
      autoFixable: true,
    });
  }

  // Sort: most severe first.
  recs.sort((a, b) => levelOrder[a.level] - levelOrder[b.level]);
  return recs;
}

// needsFullBackup reports whether the inconsistencies imply that a full
// backup should be triggered to rebuild data redundancy. Returns the reason
// string when true, empty string when false.
function needsFullBackup(r: ReconcileReport): string {
  const reasons: string[] = [];
  if (r.dangling_hash_indexes_ref_nonzero.length > 0) {
    reasons.push(
      `${r.dangling_hash_indexes_ref_nonzero.length} 个 hash 的备份数据已永久丢失（ref_count > 0 但 OSS 对象不存在）`
    );
  }
  if (r.orphan_backup_files.length > 0) {
    reasons.push(
      `${r.orphan_backup_files.length} 个 backup_files 的对应数据已丢失，相关备份恢复能力受损`
    );
  }
  return reasons.join('；');
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

          {/* Health verdict banner */}
          <HealthBanner report={report} />

          {/* Recommendations */}
          <RecommendationsCard report={report} />

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

// ─── Recommendation UI ─────────────────────────────────────────────────────

// levelStyles maps a recommendation level to its visual treatment.
const levelStyles: Record<Recommendation['level'], {
  border: string;
  bg: string;
  text: string;
  icon: React.ReactNode;
  chip: string;
  label: string;
}> = {
  critical: {
    border: 'border-rose-500/40',
    bg: 'bg-rose-500/5',
    text: 'text-rose-300',
    chip: 'bg-rose-500/15 text-rose-300 border-rose-500/30',
    icon: <AlertOctagon size={18} />,
    label: '灾难性',
  },
  danger: {
    border: 'border-rose-500/25',
    bg: 'bg-rose-500/[0.03]',
    text: 'text-rose-300/90',
    chip: 'bg-rose-500/10 text-rose-300/90 border-rose-500/20',
    icon: <FileWarning size={18} />,
    label: '严重',
  },
  warning: {
    border: 'border-amber-500/25',
    bg: 'bg-amber-500/[0.03]',
    text: 'text-amber-300/90',
    chip: 'bg-amber-500/10 text-amber-300/90 border-amber-500/20',
    icon: <AlertTriangle size={18} />,
    label: '中等',
  },
  info: {
    border: 'border-surface-3',
    bg: 'bg-surface-2/40',
    text: 'text-slate-300',
    chip: 'bg-surface-3/60 text-slate-400 border-surface-3',
    icon: <Info size={18} />,
    label: '轻微',
  },
};

// HealthBanner shows a top-level verdict based on the report.
// It exists to give the operator a one-glance read on system health so they
// don't have to interpret 8 summary cards before knowing whether to act.
function HealthBanner({ report }: { report: ReconcileReport }) {
  const fullBackupReason = needsFullBackup(report);
  const totalIssues =
    report.oss_only_orphans.length +
    report.dangling_hash_indexes_ref_zero.length +
    report.dangling_hash_indexes_ref_nonzero.length +
    report.orphan_backup_files.length +
    report.backup_files_missing_hash_index_but_in_oss.length +
    report.ref_count_mismatches.length +
    report.failed_backups_with_files.length +
    report.completed_backups_no_files.length;

  // Healthy state: no issues at all.
  if (totalIssues === 0) {
    return (
      <div className="card p-5 border border-emerald-500/30 bg-emerald-500/5 flex items-center gap-3">
        <CheckCircle2 size={22} className="text-emerald-400 shrink-0" />
        <div>
          <div className="text-sm font-medium text-emerald-300">系统数据一致，无需操作</div>
          <div className="text-xs text-emerald-400/70 mt-0.5">
            OSS、hash_index、backup_files 三方数据源完全同步，未检测到任何不一致。
          </div>
        </div>
      </div>
    );
  }

  // Critical: data loss detected, full backup required.
  if (fullBackupReason) {
    return (
      <div className="card p-5 border-rose-500/40 bg-rose-500/10">
        <div className="flex items-start gap-3">
          <AlertOctagon size={22} className="text-rose-400 shrink-0 mt-0.5" />
          <div className="flex-1 min-w-0">
            <div className="text-sm font-semibold text-rose-300">
              检测到不可恢复的数据丢失，建议立即执行全量备份
            </div>
            <div className="text-xs text-rose-300/80 mt-1 leading-relaxed">
              {fullBackupReason}。这些数据无法通过对账自动恢复，重新全量备份可重建 hash_index 与
              OSS 的对应关系。建议步骤：先执行"对账修复"清理可修复项 → 触发全量备份 → 再次运行对账确认。
            </div>
          </div>
        </div>
      </div>
    );
  }

  // Has issues but no data loss: auto-fixable.
  const autoFixableCount = buildRecommendations(report).filter((r) => r.autoFixable).length;
  return (
    <div className="card p-5 border-amber-500/25 bg-amber-500/5 flex items-center gap-3">
      <AlertTriangle size={22} className="text-amber-400 shrink-0" />
      <div>
        <div className="text-sm font-medium text-amber-300">
          发现 {totalIssues} 项不一致，{autoFixableCount} 项可自动修复
        </div>
        <div className="text-xs text-amber-400/70 mt-0.5">
          未检测到数据丢失。建议先查看下方建议，确认无误后点击"执行修复"。
        </div>
      </div>
    </div>
  );
}

// RecommendationsCard renders the prioritized, actionable suggestion list.
// Each recommendation is one row with: level icon, title, description, action,
// and an autoFixable badge so the operator knows whether "执行修复" will handle it.
function RecommendationsCard({ report }: { report: ReconcileReport }) {
  const recs = buildRecommendations(report);
  if (recs.length === 0) return null;

  return (
    <div className="card p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="flex items-center gap-2 text-sm font-medium text-slate-200">
          <ArrowRightCircle size={16} className="text-brand-400" />
          修复建议
          <span className="text-xs font-normal text-slate-500">
            （按严重程度排序，从重到轻）
          </span>
        </h3>
      </div>

      <div className="space-y-3">
        {recs.map((rec, i) => {
          const s = levelStyles[rec.level];
          return (
            <div
              key={i}
              className={cn('rounded-lg border p-4', s.border, s.bg)}
            >
              <div className="flex items-start gap-3">
                <div className={cn('shrink-0 mt-0.5', s.text)}>{s.icon}</div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap mb-1.5">
                    <span className={cn('inline-flex items-center text-[10px] font-medium px-1.5 py-0.5 rounded border', s.chip)}>
                      {s.label}
                    </span>
                    <span className="text-sm font-medium text-slate-100">{rec.title}</span>
                  </div>
                  <p className="text-xs text-slate-400 leading-relaxed mb-2">
                    {rec.description}
                  </p>
                  <div className="flex items-start gap-1.5 text-xs">
                    <span className={cn('shrink-0 font-medium', s.text)}>处置：</span>
                    <span className="text-slate-300 leading-relaxed">{rec.action}</span>
                  </div>
                  <div className="mt-2 flex items-center gap-2">
                    {rec.autoFixable ? (
                      <span className="inline-flex items-center gap-1 text-[11px] text-emerald-400/80">
                        <CheckCircle2 size={11} /> 可通过"执行修复"自动处理
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 text-[11px] text-rose-400/80">
                        <AlertOctagon size={11} /> 需手动介入，无法自动修复
                      </span>
                    )}
                  </div>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
