import { Inbox } from 'lucide-react';

interface EmptyStateProps {
  message?: string;
  description?: string;
}

export function EmptyState({ message = '暂无数据', description }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-slate-500">
      <Inbox size={48} strokeWidth={1} className="mb-4 text-slate-600" />
      <p className="text-lg font-medium">{message}</p>
      {description && <p className="text-sm mt-1">{description}</p>}
    </div>
  );
}
