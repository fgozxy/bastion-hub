import { useEffect, useState } from 'react';
import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import {
  LayoutDashboard,
  Server,
  Activity,
  TerminalSquare,
  KeyRound,
  Boxes,
  Archive,
  Settings,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  Menu,
  Sun,
  Moon,
  RefreshCw,
} from 'lucide-react';
import { useAuth, useTheme } from '../stores';
import { useIsMobile } from '../hooks/useIsMobile';
import { Toasts } from './ui';

const nav = [
  { path: '/', label: '仪表盘', icon: LayoutDashboard, end: true },
  { path: '/nodes', label: '节点', icon: Server },
  { path: '/health', label: '健康监控', icon: Activity },
  { path: '/commands', label: '命令', icon: TerminalSquare },
  { path: '/credentials', label: '凭证', icon: KeyRound },
  { path: '/containers', label: '容器', icon: Boxes },
  { path: '/backup', label: '备份', icon: Archive },
  { path: '/settings', label: '设置', icon: Settings },
];

export function MainLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const isMobile = useIsMobile();
  const { username, logout } = useAuth();
  const { theme, setTheme } = useTheme();
  const nav2 = useNavigate();

  useEffect(() => {
    useTheme.getState().init();
  }, []);

  // Lock background scroll while the mobile drawer is open.
  useEffect(() => {
    if (!isMobile || !mobileOpen) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = prev;
    };
  }, [isMobile, mobileOpen]);

  const menuIcon = isMobile
    ? mobileOpen
      ? <PanelLeftClose size={18} />
      : <Menu size={18} />
    : collapsed
      ? <PanelLeftOpen size={18} />
      : <PanelLeftClose size={18} />;

  return (
    <div className="app-shell">
      <header className="main-header">
        <div className="left">
          <button
            className="icon-btn"
            onClick={() => {
              if (isMobile) {
                setMobileOpen((o) => !o);
                return;
              }
              setCollapsed((c) => !c);
            }}
            title="切换菜单"
            aria-expanded={isMobile ? mobileOpen : !collapsed}
          >
            {menuIcon}
          </button>
          <div className="brand">
            <span className="logo-dot">N</span>
            <span>NodePanel</span>
          </div>
        </div>
        <div className="right">
          <span className="badge muted" style={{ marginRight: 4 }}>
            {username || 'admin'}
          </span>
          <button
            className="icon-btn"
            title="切换主题"
            onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
          >
            {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
          </button>
          <button className="icon-btn" title="刷新" onClick={() => window.location.reload()}>
            <RefreshCw size={18} />
          </button>
          <button
            className="icon-btn"
            title="退出登录"
            onClick={async () => {
              await logout();
              nav2('/login');
            }}
          >
            <LogOut size={18} />
          </button>
        </div>
      </header>
      <div className="main-body">
        <aside className={`sidebar ${collapsed ? 'collapsed' : ''} ${mobileOpen ? 'mobile-open' : ''}`}>
          {nav.map((n) => (
            <NavLink
              key={n.path}
              to={n.path}
              end={n.end}
              className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}
              title={n.label}
              onClick={() => setMobileOpen(false)}
            >
              <n.icon size={18} className="nav-icon" />
              <span className="nav-label">{n.label}</span>
            </NavLink>
          ))}
        </aside>
        <div className="content">
          <Outlet />
        </div>
      </div>
      {mobileOpen && <div className="nav-backdrop" onClick={() => setMobileOpen(false)} />}
      <Toasts />
    </div>
  );
}
