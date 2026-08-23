import { useEffect, useState } from 'react';
import { api } from '../api';
import { toast } from '../Toast';
import { appConfirm } from '../AppModal';

export default function DomainsPage() {
  const [list, setList] = useState([]);
  const [newDomain, setNewDomain] = useState('');
  const [includeSub, setIncludeSub] = useState(false);
  const [checked, setChecked] = useState(null); // 已通过检测的域名
  const [checking, setChecking] = useState(false);
  const [checkResult, setCheckResult] = useState(null); // {ok, colo, tls_ok, issuer, not_after, error}

  const loadList = async () => {
    try {
      const r = await api('/api/domains/list');
      setList(r.domains || []);
    } catch (e) { toast(e.message, true); }
  };

  useEffect(() => { loadList(); }, []);

  // 输入变化时重置检测状态
  const onNewDomainInput = v => {
    setNewDomain(v);
    setChecked(null);
    setCheckResult(null);
  };

  const precheck = async () => {
    const v = newDomain.trim();
    if (!v) { toast('请输入域名', true); return; }
    setChecking(true);
    setCheckResult({ pending: true });
    try {
      const r = await api('/api/domains/test', { method: 'POST', body: JSON.stringify({ domain: v }) });
      if (r.is_cloudflare) {
        setChecked(v);
        setCheckResult({ ok: true, colo: r.colo, tls_ok: r.tls_ok, issuer: r.issuer, not_after: r.not_after });
      } else {
        setChecked(null);
        setCheckResult({ ok: false, error: r.error });
      }
    } catch (e) {
      setChecked(null);
      setCheckResult({ ok: false, error: e.message });
    } finally {
      setChecking(false);
    }
  };

  const addDomain = async () => {
    const v = newDomain.trim();
    if (!v) return;
    if (checked !== v) { toast('请先通过 Cloudflare 检测', true); return; }
    try {
      // 统一存储规范：CustomDomains 只存裸域名，包含子域记录在元数据中
      const r = await api('/api/domains', { method: 'POST', body: JSON.stringify({ domain: v }) });
      if (includeSub) {
        await api('/api/domains/wildcard', { method: 'PUT', body: JSON.stringify({ domain: v, wildcard: true }) });
      }
      setNewDomain('');
      setChecked(null);
      setCheckResult(null);
      const cleaned = r.removed_manual
        ? '，已从实例自定义重写表清理 ' + r.removed_manual + ' 条同名规则'
        : '';
      toast((includeSub ? '已添加 ' + v + ' 并启用包含子域（写入 *.' + v + '）✓' : '域名已添加 ✓') + cleaned);
      await loadList();
    } catch (e) { toast(e.message, true); }
  };

  const removeDomain = async d => {
    const ok = await appConfirm('删除域名', '确定删除 ' + d + ' ？\n下次测速将不再写入该域名的重写规则。');
    if (!ok) return;
    try {
      await api('/api/domains/' + encodeURIComponent(d), { method: 'DELETE' });
      toast('域名已删除');
      await loadList();
    } catch (e) { toast(e.message, true); }
  };

  const toggleWildcard = async (domain, wildcard) => {
    try {
      await api('/api/domains/wildcard', {
        method: 'PUT',
        body: JSON.stringify({ domain, wildcard }),
      });
      toast(wildcard ? domain + ' 已启用包含子域（写入 *.' + domain + '）✓' : domain + ' 已关闭包含子域 ✓');
    } catch (e) {
      toast('修改失败: ' + e.message, true);
    }
    loadList(); // 刷新时间戳显示（失败时同时回滚开关状态）
  };

  return (
    <div className="page active" id="page-domains">
      <div className="page-header">
        <h1>域名</h1>
        <p>DNS 重写域名管理</p>
      </div>

      <div className="card">
        <h2>添加域名</h2>
        <div className="card-sub">添加时将验证是否由 Cloudflare 运营</div>
        <div style={{ display: 'flex', gap: 10 }}>
          <input
            type="text"
            placeholder="openrouter.ai"
            style={{ flex: 1 }}
            value={newDomain}
            onChange={e => onNewDomainInput(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') addDomain(); }}
          />
          <button className="btn-ghost" disabled={checking} onClick={precheck}>检测</button>
          <button className="btn-primary" disabled={checked !== newDomain.trim() || !newDomain.trim()} onClick={addDomain}>添加</button>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 14 }}>
          <label className="switch">
            <input type="checkbox" checked={includeSub} onChange={e => setIncludeSub(e.target.checked)} />
            <span className="slider"></span>
          </label>
          <span style={{ fontSize: 13.5 }}>
            包含子域
          </span>
        </div>
        <div style={{ marginTop: 12 }}>
          {checkResult && checkResult.pending && <span className="hint">检测中...</span>}
          {checkResult && !checkResult.pending && checkResult.ok && (
            <>
              <span className="badge ok">✓ 由 Cloudflare 服务{checkResult.colo ? ' · ' + checkResult.colo : ''}</span>{' '}
              {checkResult.tls_ok && (
                <span className="badge">TLS 正常 · {checkResult.issuer || ''} · 有效期至 {checkResult.not_after || ''}</span>
              )}
            </>
          )}
          {checkResult && !checkResult.pending && !checkResult.ok && (
            <>
              <span className="badge err">✗ 非 Cloudflare{checkResult.error ? ' · ' + checkResult.error : ''}</span>{' '}
              <span className="hint">无法添加</span>
            </>
          )}
        </div>
      </div>

      <div className="card">
        <h2>域名列表</h2>
        <div className="card-sub">由 Cloudflare 边缘节点提供服务</div>
        <table>
          <thead>
            <tr><th>域名</th><th>包含子域</th><th>更新时间</th><th>实例</th><th style={{ width: 90 }}>操作</th></tr>
          </thead>
          <tbody>
            {list.length ? list.map(d => (
              <tr key={d.domain}>
                <td>
                  {d.wildcard ? (
                    <>
                      <span className="mono" style={{ color: 'var(--ok)' }}>{d.domain}</span>{' '}
                      <span className="badge ok">含子域</span>
                    </>
                  ) : (
                    <span className="mono">{d.domain}</span>
                  )}
                </td>
                <td>
                  <label className="switch">
                    <input
                      type="checkbox"
                      checked={!!d.wildcard}
                      onChange={e => toggleWildcard(d.domain, e.target.checked)}
                    />
                    <span className="slider"></span>
                  </label>
                </td>
                  <td>{d.updated_at || <span className="hint">—</span>}</td>
                  <td>{(d.instances || []).length ? d.instances.join('、') : <span className="hint">无实例</span>}</td>
                  <td><button className="btn-danger btn-sm" onClick={() => removeDomain(d.domain)}>删除</button></td>
                </tr>
            )) : (
              <tr><td colSpan="5" className="t-empty">尚未配置任何域名</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
