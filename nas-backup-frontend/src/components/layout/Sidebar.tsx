import { NavLink, useLocation } from 'react-router-dom';
import { LayoutDashboard, FolderOpen, Settings, ScrollText, ChevronLeft, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useAppStore } from '@/store/useAppStore';

const navItems = [
  { to: '/', icon: LayoutDashboard, label: '全览' },
  { to: '/content', icon: FolderOpen, label: '内容选择' },
  { to: '/strategy', icon: Settings, label: '策略设置' },
  { to: '/logs', icon: ScrollText, label: '日志' },
];

export function Sidebar() {
  const { sidebarCollapsed, toggleSidebar } = useAppStore();
  const location = useLocation();

  return (
    <aside
      className={cn(
        'fixed left-0 top-0 h-full bg-surface-1 border-r border-surface-3 z-30 flex flex-col transition-all duration-300',
        sidebarCollapsed ? 'w-16' : 'w-56'
      )}
    >
      <div className={cn('flex items-center h-16 px-4 border-b border-surface-3', sidebarCollapsed ? 'justify-center' : 'gap-3')}>
        <img src="/favicon.png" alt="NAS Backup Logo" className="w-7 h-7 shrink-0 rounded" />
        {!sidebarCollapsed && (
          <div className="overflow-hidden">
            <h1 className="text-base font-mono font-bold text-white whitespace-nowrap">NAS Backup</h1>
          </div>
        )}
      </div>

      <nav className="flex-1 py-4 space-y-1 px-2">
        {navItems.map(({ to, icon: Icon, label }) => {
          const isActive = location.pathname === to;
          return (
            <NavLink
              key={to}
              to={to}
              className={cn(
                'flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all duration-200',
                isActive
                  ? 'bg-brand-500/10 text-brand-400 border border-brand-500/20'
                  : 'text-slate-400 hover:text-white hover:bg-surface-2 border border-transparent',
                sidebarCollapsed && 'justify-center px-2'
              )}
            >
              <Icon size={20} className="shrink-0" />
              {!sidebarCollapsed && <span className="whitespace-nowrap">{label}</span>}
            </NavLink>
          );
        })}
      </nav>

      <div className="p-2 border-t border-surface-3">
        <button
          onClick={toggleSidebar}
          className="w-full flex items-center justify-center p-2 rounded-lg text-slate-400 hover:text-white hover:bg-surface-2 transition-colors"
        >
          {sidebarCollapsed ? <ChevronRight size={18} /> : <ChevronLeft size={18} />}
        </button>
      </div>
    </aside>
  );
}
