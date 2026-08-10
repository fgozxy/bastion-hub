import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAuth, notify } from '../stores';

export function LoginPage() {
  const [u, setU] = useState('');
  const [p, setP] = useState('');
  const [busy, setBusy] = useState(false);
  const { login } = useAuth();
  const nav = useNavigate();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    const ok = await login(u, p);
    setBusy(false);
    if (ok) {
      nav('/');
    } else {
      notify('用户名或密码错误', 'error');
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        <div className="login-title">
          <div className="logo-dot">N</div>
          <h1>NodePanel</h1>
          <p>多服务器管理面板</p>
        </div>
        <div className="field">
          <label>用户名</label>
          <input className="input" value={u} onChange={(e) => setU(e.target.value)} autoFocus />
        </div>
        <div className="field">
          <label>密码</label>
          <input
            className="input"
            type="password"
            value={p}
            onChange={(e) => setP(e.target.value)}
          />
        </div>
        <button className="btn primary" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? '登录中…' : '登录'}
        </button>
        <p className="page-subtitle apk-download-hint" style={{ marginTop: 14, marginBottom: 0, textAlign: 'center' }}>
          <a href="/downloads/NodePanel.apk">下载 Android 安装包</a>
          <span style={{ opacity: 0.65 }}> · 允许未知来源后安装</span>
        </p>
      </form>
    </div>
  );
}
