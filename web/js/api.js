// HTTP API client for SyncWatch
import { store } from './store.js';

async function request(method, path, body) {
  const headers = body ? { 'Content-Type': 'application/json' } : {};
  const token = store.get('token');
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const contentType = res.headers.get('content-type') || '';
  const data = res.status === 204
    ? null
    : contentType.includes('application/json') ? await res.json() : await res.text();
  if (!res.ok) {
    if (res.status === 401) window.dispatchEvent(new CustomEvent('syncwatch:unauthorized'));
    throw new Error(data?.error || data || `HTTP ${res.status}`);
  }
  return data;
}

async function upload(path, file) {
  const form = new FormData();
  form.append('file', file);
  const token = store.get('token');
  const res = await fetch(path, {
    method: 'POST',
    headers: token ? { 'Authorization': `Bearer ${token}` } : {},
    body: form,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 401) window.dispatchEvent(new CustomEvent('syncwatch:unauthorized'));
    throw new Error(data.error || `HTTP ${res.status}`);
  }
  return data;
}

export const api = {
  // Auth
  login: (password) => request('POST', '/api/auth', { password }),
  adminLogin: (password) => request('POST', '/api/admin/auth', { password }),

  // Playback (host only)
  play: (path) => request('POST', '/api/playback/play', { path }),
  pause: () => request('POST', '/api/playback/pause'),
  resume: () => request('POST', '/api/playback/resume'),
  seek: (position) => request('POST', '/api/playback/seek', { position }),
  speed: (speed) => request('POST', '/api/playback/speed', { speed }),
  sync: (position) => request('POST', '/api/playback/sync', { position }),
  audioTrack: (index) => request('POST', '/api/playback/audio', { index }),
  subtitle: (index) => request('POST', '/api/playback/subtitle', { index }),

  // Upload
  upload: (file) => upload('/api/upload', file),
  uploadSubtitle: (file) => upload('/api/upload/subtitle', file),

  // Status & Media
  status: () => request('GET', '/api/status'),
  mediaInfo: (path) => request('GET', `/api/media/info?path=${encodeURIComponent(path)}`),
  mediaScan: (dir = '') => request('GET', dir ? `/api/media/scan?dir=${encodeURIComponent(dir)}` : '/api/media/scan'),
  state: () => request('GET', '/api/state'),
};
