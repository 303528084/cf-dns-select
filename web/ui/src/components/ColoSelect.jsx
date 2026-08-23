import { useEffect, useRef, useState } from 'react';
import { COLO_REGIONS } from '../coloRegions';

// Cloudflare 节点区域多选下拉：value 为逗号分隔的地区码字符串（如 "HKG,NRT"）
export default function ColoSelect({ value, onChange }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const rootRef = useRef(null);
  const searchRef = useRef(null);

  const selected = new Set((value || '').split(',').map(s => s.trim().toUpperCase()).filter(Boolean));

  useEffect(() => {
    const onClick = e => {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener('click', onClick);
    return () => document.removeEventListener('click', onClick);
  }, []);

  const togglePanel = () => {
    const next = !open;
    setOpen(next);
    if (next) { setQ(''); setTimeout(() => searchRef.current && searchRef.current.focus(), 0); }
  };

  const toggle = (code, on) => {
    const set = new Set(selected);
    if (on) set.add(code); else set.delete(code);
    onChange([...set].sort().join(','));
  };

  const selectAll = e => {
    e.preventDefault(); e.stopPropagation();
    onChange(COLO_REGIONS.map(r => r.code).sort().join(','));
  };

  const clearAll = e => {
    e.preventDefault(); e.stopPropagation();
    onChange('');
  };

  const remove = code => {
    const set = new Set(selected);
    set.delete(code);
    onChange([...set].sort().join(','));
  };

  const filtered = COLO_REGIONS.filter(r => {
    const qq = q.trim().toUpperCase();
    return !qq || r.code.includes(qq) || r.name.includes(qq);
  });

  const codes = [...selected].sort();

  return (
    <div className="colo-select" ref={rootRef}>
      <div className={'colo-trigger' + (open ? ' open' : '')} onClick={togglePanel}>
        <span className={codes.length ? '' : 'placeholder'}>
          {codes.length ? '已选 ' + codes.length + ' 个地区' : '全部地区'}
        </span>
        <span className="arrow">▼</span>
      </div>
      <div className={'colo-panel' + (open ? ' show' : '')}>
        <input
          className="colo-search"
          placeholder="搜索地区码或名称..."
          value={q}
          ref={searchRef}
          onChange={e => setQ(e.target.value)}
        />
        <div>
          {filtered.length ? filtered.map(r => (
            <label className="colo-item" key={r.code}>
              <input
                type="checkbox"
                checked={selected.has(r.code)}
                onChange={e => toggle(r.code, e.target.checked)}
              />
              <span className="colo-name">{r.name}</span>
              <span className="colo-code">{r.code}</span>
            </label>
          )) : (
            <div className="t-empty" style={{ padding: 12 }}>无匹配地区</div>
          )}
        </div>
        <div className="colo-actions">
          <button className="btn-ghost btn-sm" onClick={selectAll}>全选</button>
          <button className="btn-ghost btn-sm" onClick={clearAll}>清空</button>
        </div>
      </div>
      <div className="colo-tags">
        {codes.map(c => {
          const r = COLO_REGIONS.find(x => x.code === c);
          const label = r ? r.name + ' ' + c : c;
          return (
            <span className="colo-tag" key={c}>
              {label}
              <span className="x" onClick={() => remove(c)}>×</span>
            </span>
          );
        })}
      </div>
    </div>
  );
}
