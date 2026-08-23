import { useEffect, useRef, useState } from 'react';
import { setToken } from '../api';

export default function LoginOverlay({ show, onLogin }) {
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const inputRef = useRef(null);

  useEffect(() => {
    if (show) {
      setErr('');
      setTimeout(() => inputRef.current && inputRef.current.focus(), 50);
    }
  }, [show]);

  const doLogin = async () => {
    if (busy) return;
    setErr('');
    setBusy(true);
    try {
      const r = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: inputRef.current.value }),
      });
      const data = await r.json();
      if (!r.ok) { setErr(data.error || '登录失败'); return; }
      setToken(data.token);
      inputRef.current.value = '';
      onLogin();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div id="login-overlay" className={show ? 'show' : ''}>
      <div className="login-box">
        <h2>Cloudflare 节点智选</h2>
        <div id="login-error">{err}</div>
        <input
          type="password"
          ref={inputRef}
          placeholder="Dashboard 密码"
          onKeyDown={e => { if (e.key === 'Enter') doLogin(); }}
        />
        <button className="btn-primary" onClick={doLogin} disabled={busy}>登 录</button>
        <div className="hint" style={{ marginTop: 14, textAlign: 'center' }}>请查阅容器日志获取访问密码</div>
        <div className="hint" style={{ marginTop: 14, textAlign: 'center' }}>GitHub开源项目<a href="https://github.com/neon9809/cf-dns-select" target="_blank" rel="noopener">cf-dns-select</a></div>
      </div>
    </div>
  );
}
