import { BACKUP_STATUS_MAP } from '@/utils/constants';
import { cn } from '@/lib/utils';

interface StatusBadgeProps {
  status: string;
  pulse?: boolean;
}

export function StatusBadge({ status, pulse }: StatusBadgeProps) {
  const config = BACKUP_STATUS_MAP[status] || { label: status, color: 'text-slate-400' };

  return (
    <span className={cn('inline-flex items-center gap-1.5 text-sm font-medium', config.color)}>
      {pulse && status === 'running' && (
        <span className="relative flex h-2 w-2">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-brand-400 opacity-75" />
          <span className="relative inline-flex rounded-full h-2 w-2 bg-brand-400" />
        </span>
      )}
      {config.label}
    </span>
  );
}
