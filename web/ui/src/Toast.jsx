import { useEffect, useState } from 'react';

let toastFn = null;

export function toast(msg, isErr) {
  if (toastFn) toastFn(String(msg), !!isErr);
}

export function ToastHost() {
  const [state, setState] = useState(null);

  useEffect(() => {
    toastFn = (msg, isErr) => setState({ msg, isErr, key: Date.now() });
    return () => { toastFn = null; };
  }, []);

  useEffect(() => {
    if (!state) return undefined;
    const t = setTimeout(() => setState(null), 3000);
    return () => clearTimeout(t);
  }, [state]);

  return (
    <div
      className={'toast' + (state ? ' show' : '')}
      style={state && state.isErr ? { borderColor: 'var(--err)' } : undefined}
    >
      {state ? state.msg : ''}
    </div>
  );
}
