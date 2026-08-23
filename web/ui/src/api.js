let authToken = sessionStorage.getItem('dsa_token') || '';
let onUnauthorized = null;

export function getToken() { return authToken; }

export function setToken(t) {
  authToken = t;
  if (t) sessionStorage.setItem('dsa_token', t);
  else sessionStorage.removeItem('dsa_token');
}

// 401 时由 App 注册此回调，用于弹出登录遮罩
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

export async function api(path, opts = {}) {
  opts.headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
  if (authToken) opts.headers['Authorization'] = 'Bearer ' + authToken;
  const resp = await fetch(path, opts);
  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized();
    throw new Error('未登录或会话已过期');
  }
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok && !data.error) throw new Error('HTTP ' + resp.status);
  return data;
}
