// HTTP API client for SyncWatch
import { store } from './store.js';

const BASE = '';

async function request(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  const token = store.get('token');
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const res = await fetch(`${BASE}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  const data = await res.json();
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
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
  audioTrack: (index) => request('POST', '/api/playback/audio', { index }),
  subtitle: (index) => request('POST', '/api/playback/subtitle', { index }),

  // Status & Media
  status: () => request('GET', '/api/status'),
  mediaInfo: (path) => request('GET', `/api/media/info?path=${encodeURIComponent(path)}`),
  mediaScan: (dir) => request('GET', `/api/media/scan?dir=${encodeURIComponent(dir)}`),
  state: () => request('GET', '/api/state'),
};
