import { useEffect, useState } from 'react';

// 命令式确认/信息弹窗：appConfirm / appAlert 返回 Promise，
// 由 App 挂载的 AppModalHost 渲染（对应旧版 appConfirm/appAlert）。
let modalFn = null;

export function appConfirm(title, msg, detail) {
  return new Promise(resolve => {
    if (!modalFn) { resolve(false); return; }
    modalFn({ kind: 'confirm', title, msg, detail, resolve });
  });
}

export function appAlert(title, msg, detail, isError) {
  return new Promise(resolve => {
    if (!modalFn) { resolve(); return; }
    modalFn({ kind: 'alert', title, msg, detail, isError, resolve });
  });
}

// appChoice 多选项弹窗：options 形如 [{label, value, className}]，
// cancelLabel 提供时显示取消按钮；取消/Esc 返回 null。
export function appChoice(title, msg, detail, options, cancelLabel) {
  return new Promise(resolve => {
    if (!modalFn) { resolve(null); return; }
    modalFn({ kind: 'choice', title, msg, detail, options, cancelLabel, resolve });
  });
}

export function AppModalHost() {
  const [st, setSt] = useState(null);

  useEffect(() => {
    modalFn = setSt;
    return () => { modalFn = null; };
  }, []);

  useEffect(() => {
    const onKey = e => {
      if (e.key === 'Escape' && st) close(false);
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });

  const close = v => {
    if (st) { st.resolve(v); setSt(null); }
  };

  if (!st) return null;
  return (
    <div
      className="app-modal-scrim show"
      onClick={e => { if (e.target === e.currentTarget) close(false); }}
    >
      <div className="app-modal">
        <h3>{st.title}</h3>
        <div className="app-msg">{st.msg}</div>
        <div className="app-detail" style={st.detail ? undefined : { display: 'none' }}>{st.detail || ''}</div>
        <div className="app-actions">
          {st.kind === 'confirm' && (
            <button className="btn-ghost" onClick={() => close(false)}>取消</button>
          )}
          {st.kind === 'choice' ? (
            <>
              {(st.options || []).map((o, i) => (
                <button
                  key={i}
                  className={o.className || 'btn-primary'}
                  onClick={() => close(o.value)}
                >
                  {o.label}
                </button>
              ))}
              {st.cancelLabel && (
                <button className="btn-ghost" onClick={() => close(null)}>{st.cancelLabel}</button>
              )}
            </>
          ) : (
            <button
              className={st.kind === 'alert' && st.isError ? 'btn-danger' : 'btn-primary'}
              onClick={() => close(true)}
            >
              {st.kind === 'confirm' ? '确定' : '知道了'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
