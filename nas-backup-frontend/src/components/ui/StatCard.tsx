import { type LucideIcon } from 'lucide-react';
import { cn } from '@/lib/utils';

interface StatCardProps {
  icon: LucideIcon;
  label: string;
  value: string | number;
  subValue?: string;
  iconColor?: string;
  className?: string;
}

export function StatCard({ icon: Icon, label, value, subValue, iconColor = 'text-brand-400', className }: StatCardProps) {
  return (
    <div className={cn('card-hover p-5', className)}>
      <div className="flex items-start justify-between">
        <div className="space-y-2">
          <p className="text-sm text-slate-400">{label}</p>
          <p className="text-2xl font-mono font-bold text-white">{value}</p>
          {subValue && <p className="text-xs text-slate-500">{subValue}</p>}
        </div>
        <div className={cn('p-2.5 rounded-lg bg-surface-2', iconColor)}>
          <Icon size={20} />
        </div>
      </div>
    </div>
  );
}
