import { useEffect, useState } from 'react';
import { api, getToken, setToken, setUnauthorizedHandler } from './api';
import { ToastHost } from './Toast';
import { AppModalHost } from './AppModal';
import LoginOverlay from './components/LoginOverlay';
import StatusPage from './components/StatusPage';
import DomainsPage from './components/DomainsPage';
import SpeedtestPage from './components/SpeedtestPage';
import InstancesPage from './components/InstancesPage';

const PAGES = [
  { id: 'status', label: '状态' },
  { id: 'domains', label: '域名' },
  { id: 'speedtest', label: '测速' },
  { id: 'instances', label: '实例' },
];

export default function App() {
  const [authed, setAuthed] = useState(false);
  const [checking, setChecking] = useState(true);
  const [page, setPage] = useState('status');

  useEffect(() => {
    setUnauthorizedHandler(() => setAuthed(false));
    (async () => {
      if (!getToken()) { setChecking(false); return; }
      try {
        await api('/api/status');
        setAuthed(true);
      } catch (e) { /* 401 已通过回调弹出登录 */ }
      setChecking(false);
    })();
  }, []);

  const logout = async () => {
    try {
      await fetch('/api/logout', {
        method: 'POST',
        headers: getToken() ? { Authorization: 'Bearer ' + getToken() } : {},
      });
    } catch (e) { /* 忽略网络错误，本地照常登出 */ }
    setToken('');
    setAuthed(false);
    setPage('status');
  };

  return (
    <>
      <LoginOverlay show={!authed} onLogin={() => { setAuthed(true); setPage('status'); }} />

      <nav className="nav">
        <div className="nav-inner">
          <div className="nav-title">
            <a href="https://github.com/neon9809/cf-dns-select" target="_blank" rel="noopener">Cloudflare节点智选</a>
          </div>
          <div className="nav-tabs">
            {PAGES.map(p => (
              <button
                key={p.id}
                className={'nav-tab' + (page === p.id ? ' active' : '')}
                onClick={() => setPage(p.id)}
              >
                {p.label}
              </button>
            ))}
          </div>
          <button className="nav-logout" onClick={logout}>退出登录</button>
        </div>
      </nav>

      <div className="container">
        {!checking && authed && (
          <>
            {page === 'status' && <StatusPage />}
            {page === 'domains' && <DomainsPage />}
            {page === 'speedtest' && <SpeedtestPage />}
            {page === 'instances' && <InstancesPage />}
          </>
        )}
      </div>

      <footer className="footer">
        <span className="sep">GitHub开源项目</span>
        <a href="https://github.com/neon9809/cf-dns-select" target="_blank" rel="noopener">cf-dns-select</a>
      </footer>

      <ToastHost />
      <AppModalHost />
    </>
  );
}
