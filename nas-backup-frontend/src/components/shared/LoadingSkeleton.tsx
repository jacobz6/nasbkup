import { cn } from '@/lib/utils';

interface LoadingSkeletonProps {
  rows?: number;
  className?: string;
}

export function LoadingSkeleton({ rows = 3, className }: LoadingSkeletonProps) {
  return (
    <div className={cn('space-y-3 animate-pulse', className)}>
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="h-4 bg-surface-2 rounded-lg" style={{ width: `${60 + Math.random() * 40}%` }} />
      ))}
    </div>
  );
}

export function CardSkeleton() {
  return (
    <div className="card p-5 animate-pulse">
      <div className="flex items-start justify-between">
        <div className="space-y-2 flex-1">
          <div className="h-3 bg-surface-2 rounded w-16" />
          <div className="h-7 bg-surface-2 rounded w-24" />
        </div>
        <div className="h-10 w-10 bg-surface-2 rounded-lg" />
      </div>
    </div>
  );
}
