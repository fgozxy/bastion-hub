import { useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate, Outlet } from 'react-router-dom';
import { useAuth } from './stores';
import { MainLayout } from './components/MainLayout';
import { LoginPage } from './pages/Login';
import { DashboardPage } from './pages/Dashboard';
import { NodesPage } from './pages/Nodes';
import { HealthPage } from './pages/Health';
import { CommandsPage } from './pages/Commands';
import { CredentialsPage } from './pages/Credentials';
import { ContainersPage } from './pages/Containers';
import { BackupPage } from './pages/Backup';
import { SettingsPage } from './pages/Settings';

function Protected() {
  const { authed, check } = useAuth();
  useEffect(() => {
    check();
  }, [check]);
  if (authed === null) return <div style={{ padding: 30 }}>…</div>;
  if (authed === false) return <Navigate to="/login" replace />;
  return <Outlet />;
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<Protected />}>
          <Route element={<MainLayout />}>
            <Route index element={<DashboardPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="health" element={<HealthPage />} />
            <Route path="commands" element={<CommandsPage />} />
            <Route path="credentials" element={<CredentialsPage />} />
            <Route path="containers" element={<ContainersPage />} />
            <Route path="backup" element={<BackupPage />} />
            <Route path="settings" element={<SettingsPage />} />
          </Route>
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
