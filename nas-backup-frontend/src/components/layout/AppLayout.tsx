import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { useAppStore } from '@/store/useAppStore';
import { cn } from '@/lib/utils';
import { useEffect } from 'react';
import { Toast } from '@/store/useAppStore';
import { CheckCircle, XCircle, Info, AlertTriangle, X } from 'lucide-react';

const toastIcons = {
  success: CheckCircle,
  error: XCircle,
  info: Info,
  warning: AlertTriangle,
};

const toastColors = {
  success: 'border-emerald-500/30 bg-emerald-500/10',
  error: 'border-rose-500/30 bg-rose-500/10',
  info: 'border-brand-500/30 bg-brand-500/10',
  warning: 'border-amber-500/30 bg-amber-500/10',
};

const toastIconColors = {
  success: 'text-emerald-400',
  error: 'text-rose-400',
  info: 'text-brand-400',
  warning: 'text-amber-400',
};

function ToastItem({ toast, onRemove }: { toast: Toast; onRemove: (id: string) => void }) {
  const Icon = toastIcons[toast.type];

  useEffect(() => {
    const timer = setTimeout(() => onRemove(toast.id), 4000);
    return () => clearTimeout(timer);
  }, [toast.id, onRemove]);

  return (
    <div className={`flex items-center gap-3 px-4 py-3 rounded-lg border shadow-lg animate-fade-in ${toastColors[toast.type]}`}>
      <Icon size={18} className={toastIconColors[toast.type]} />
      <span className="text-sm text-white flex-1">{toast.message}</span>
      <button onClick={() => onRemove(toast.id)} className="text-slate-400 hover:text-white">
        <X size={14} />
      </button>
    </div>
  );
}

export function AppLayout() {
  const { sidebarCollapsed, toasts, removeToast } = useAppStore();

  return (
    <div className="min-h-screen bg-surface-0">
      <Sidebar />
      <main
        className={cn(
          'transition-all duration-300 min-h-screen',
          sidebarCollapsed ? 'ml-16' : 'ml-56'
        )}
      >
        <div className="p-6 max-w-[1400px]">
          <Outlet />
        </div>
      </main>

      {toasts.length > 0 && (
        <div className="fixed top-4 right-4 z-50 space-y-2 w-80">
          {toasts.map((toast) => (
            <ToastItem key={toast.id} toast={toast} onRemove={removeToast} />
          ))}
        </div>
      )}
    </div>
  );
}
