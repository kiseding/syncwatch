// SyncWatch Frontend
import { store } from './store.js';
import { api } from './api.js';
import Hls from 'hls.js';

const $ = (sel) => document.querySelector(sel);

let ws = null, hls = null;

// ====== Navigation ======
window.navigate = function (screen) {
  document.querySelectorAll('.screen').forEach(s => s.classList.remove('active'));
  const el = document.getElementById(`${screen}-screen`);
  if (el) { el.classList.add('active'); store.set('screen', screen); if (screen === 'admin') updateAdmin(); }
};

// ====== Viewer Login ======
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const pwd = $('#password-input').value;
  const btn = $('#login-btn'), err = $('#login-error');
  btn.disabled = true; btn.textContent = '验证中...'; err.classList.add('hidden');
  try {
    const r = await api.login(pwd);
    store.login(r.token, 'viewer');
    $('#password-input').value = '';
    enterViewer();
  } catch (ex) { err.textContent = ex.message; err.classList.remove('hidden'); }
  finally { btn.disabled = false; btn.textContent = '进入观影室'; }
});

// ====== Admin Login ======
$('#admin-login-form')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const pwd = $('#admin-password-input').value;
  const btn = $('#admin-login-btn'), err = $('#admin-login-error');
  btn.disabled = true; btn.textContent = '验证中...'; err.classList.add('hidden');
  try {
    const r = await api.adminLogin(pwd);
    store.login(r.token, 'host');
    $('#admin-password-input').value = '';
    $('#admin-login-panel').classList.add('hidden');
    $('#admin-dashboard').classList.remove('hidden');
    enterHost();
  } catch (ex) { err.textContent = ex.message; err.classList.remove('hidden'); }
  finally { btn.disabled = false; btn.textContent = '进入控制台'; }
});

// ====== Enter Modes ======
function enterViewer() {
  navigate('player');
  $('#host-controls').classList.add('hidden');
  $('#video-status').classList.add('hidden');
  $('#viewer-overlay').classList.remove('hidden');
  connectWebSocket();
}

function enterHost() {
  navigate('player');
  $('#host-controls').classList.remove('hidden');
  $('#viewer-overlay').classList.add('hidden');
  $('#status-bar').style.display = 'none';
  connectWebSocket();
}

// ====== WebSocket ======
function connectWebSocket() {
  const token = store.get('token');
  if (!token) { console.warn('[SyncWatch] No token, skipping WS'); return; }
  store.set('connection.status', 'connecting');
  updateConnectionUI();

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = `${proto}//${location.host}/ws?token=${encodeURIComponent(token)}`;
  console.log('[SyncWatch] WS connect');
  ws = new WebSocket(url);

  ws.onopen = () => {
    console.log('[SyncWatch] WS open');
    store.set('connection.status', 'connected');
    updateConnectionUI();
  };
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    handleWSMessage(msg);
  };
  ws.onclose = (ev) => {
    console.warn('[SyncWatch] WS close', ev.code);
    store.set('connection.status', 'disconnected');
    updateConnectionUI();
    setTimeout(connectWebSocket, 1000);
  };
  ws.onerror = () => console.error('[SyncWatch] WS error');
}

function handleWSMessage(msg) {
  switch (msg.type) {
    case 'joined':
      handleJoined(msg.room_state);
      break;
    case 'media':
      handleMedia(msg.media_url);
      break;
    case 'state':
      handleStateUpdate(msg.play_state);
      break;
    case 'sync':
      handleSync(msg.play_state);
      break;
    case 'subtitle':
      initSubtitle(msg.from, msg.text);
      break;
    case 'system':
      updateViewerCount(msg.text);
      break;
  }
}

function handleJoined(rs) {
  if (!rs) return;
  // Only overwrite fields that are present (partial updates via broadcastRoomInfo)
  if (rs.state !== undefined) store.set('playback.state', rs.state);
  if (rs.position !== undefined) store.set('playback.position', rs.position);
  if (rs.media?.duration) store.set('playback.duration', rs.media.duration);
  if (rs.speed) store.set('playback.speed', rs.speed);
  if (rs.media?.filename) store.set('media.filename', rs.media.filename);
  if (rs.subtitle?.content) initSubtitle(rs.subtitle.format, rs.subtitle.content);
  if (rs.audio_tracks) {
    const sel = $('#audio-select');
    sel.innerHTML = rs.audio_tracks.map((t, i) => `<option value="${i}">${t.language || t.title || 'Track '+(i+1)}</option>`).join('');
    sel.classList.toggle('hidden', rs.audio_tracks.length <= 1);
    sel.value = rs.selected_audio || 0;
  }
  if (rs.subtitle_tracks?.length) {
    const sel = $('#subtitle-select');
    sel.innerHTML = `<option value="-1">关闭</option>` + rs.subtitle_tracks.map((t, i) => `<option value="${i}">${t.title || 'Sub '+(i+1)}</option>`).join('');
    sel.classList.toggle('hidden', false);
    sel.value = rs.selected_sub !== undefined ? String(rs.selected_sub) : '-1';
  }
  updatePlayerUI();
}

function handleMedia(url) {
  if (!url) return;
  console.log('[SyncWatch] Loading media:', url);
  store.set('playback.mediaURL', url);
  loadVideo(url);
}

function handleStateUpdate(ps) {
  if (!ps) return;
  const role = store.get('role');

  // Viewer: sync to host's state
  if (role === 'viewer') {
    const video = $('#main-video');
    if (ps.playing) {
      if (video.paused) {
        if (Math.abs(video.currentTime - ps.position) > 2) {
          video.currentTime = ps.position;
        }
        video.play().catch(() => {});
      }
    } else {
      video.pause();
    }
    if (ps.speed && video.playbackRate !== ps.speed) {
      video.playbackRate = ps.speed;
    }
  }

  store.set('playback.state', ps.playing ? 'playing' : 'paused');
  store.set('playback.position', ps.position);
  store.set('playback.speed', ps.speed || 1.0);
  updatePlayerUI();
}

function handleSync(ps) {
  if (!ps) return;
  // Host controls playback, only viewers sync to server
  if (store.get('role') === 'host') {
    store.set('playback.position', ps.position);
    updatePlayerUI();
    return;
  }
  const video = $('#main-video');
  if (Math.abs(video.currentTime - ps.position) > 0.5) {
    video.currentTime = ps.position;
  }
  if (ps.playing && video.paused) video.play().catch(() => {});
  else if (!ps.playing && !video.paused) video.pause();
  store.set('playback.position', ps.position);
  updatePlayerUI();
}

// ====== Video Player ======
function loadVideo(url) {
  const video = $('#main-video');
  $('#video-status').classList.remove('hidden');
  $('#status-text').textContent = '加载中...';

  // Destroy previous HLS instance
  if (hls) { hls.destroy(); hls = null; }

  // Remove old timeupdate handler
  video.ontimeupdate = null;

  // Update progress bar in real-time for host
  if (store.get('role') === 'host') {
    video.ontimeupdate = () => {
      store.set('playback.position', video.currentTime);
      updatePlayerUI();
    };
  }

  const isM3U8 = url.match(/\.m3u8(\?.*)?$/i);

  if (isM3U8 && Hls.isSupported()) {
    hls = new Hls({ enableWorker: true, lowLatencyMode: false });
    hls.loadSource(url);
    hls.attachMedia(video);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      $('#video-status').classList.add('hidden');
      store.set('playback.duration', hls.levels[0]?.details?.totalduration || video.duration || 0);
      updatePlayerUI();
      video.play().catch(() => {});
    });
    hls.on(Hls.Events.ERROR, (evt, data) => {
      if (data.fatal) {
        console.error('[SyncWatch] HLS error:', data.type, data.details);
        $('#status-text').textContent = `HLS 错误: ${data.details}`;
      }
    });
  } else {
    // Direct play (mp4, webm, etc.)
    video.src = url;
    video.load();
    video.onloadedmetadata = () => {
      $('#video-status').classList.add('hidden');
      store.set('playback.duration', video.duration || 0);
      updatePlayerUI();
    };
    video.oncanplay = () => {
      $('#video-status').classList.add('hidden');
      video.play().catch(() => {});
    };
    video.onerror = () => {
      $('#status-text').textContent = `加载失败: ${video.error?.message || '未知错误'}`;
    };
  }
}

// ====== Subtitle ======
async function initSubtitle(format, content) {
  // Destroy previous renderer
  const canvas = $('#subtitle-canvas');
  const ctx = canvas.getContext('2d');
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  if (!content) return;

  try {
    const JASSUB = (await import('jassub')).default;
    const video = $('#main-video');
    canvas.width = video.clientWidth || 1280;
    canvas.height = video.clientHeight || 720;
    const jassub = new JASSUB({
      video, canvas, subContent: content,
      workerUrl: '/jassub/jassub-worker.js',
      modernWasmUrl: '/jassub/jassub-worker-modern.wasm',
      prescaleFactor: 0.5, blendMode: 'js', asyncRender: true, targetFps: 30,
    });
    // Store for cleanup (simplified — just use module scope)
    window._jassub = jassub;
  } catch (err) { console.error('[SyncWatch] Subtitle init failed:', err); }
}

// ====== Host Controls ======
$('#btn-play-pause').addEventListener('click', async () => {
  try {
    const playing = store.get('playback.state') === 'playing';
    const video = $('#main-video');
    // Optimistic update
    if (playing) {
      video.pause();
      store.set('playback.state', 'paused');
      await api.pause();
    } else {
      video.play().catch(() => {});
      store.set('playback.state', 'playing');
      await api.resume();
    }
    updatePlayerUI();
  } catch (e) { console.error(e); }
});

$('#seek-bar').addEventListener('input', async (e) => {
  const d = store.get('playback.duration'); if (!d) return;
  try { await api.seek((e.target.value / 100) * d); } catch (e) {}
});

$('#speed-select').addEventListener('change', async (e) => {
  try { await api.speed(parseFloat(e.target.value)); } catch (e) {}
});

$('#audio-select').addEventListener('change', async (e) => {
  try { await api.audioTrack(parseInt(e.target.value)); } catch (e) {}
});

$('#subtitle-select').addEventListener('change', async (e) => {
  try { await api.subtitle(parseInt(e.target.value)); } catch (e) {}
});

$('#btn-force-sync').addEventListener('click', async () => {
  try {
    const s = await api.state();
    const video = $('#main-video');
    const role = store.get('role');

    // Sync video position to server state
    if (s.position > 0 && Math.abs(video.currentTime - s.position) > 0.3) {
      video.currentTime = s.position;
    }
    if (s.state === 'playing' && video.paused) {
      video.play().catch(() => {});
    } else if (s.state === 'paused' && !video.paused) {
      video.pause();
    }
    if (s.speed && video.playbackRate !== s.speed) {
      video.playbackRate = s.speed;
    }
    store.set('playback.state', s.state === 'playing' ? 'playing' : 'paused');
    store.set('playback.position', s.position || 0);
    store.set('playback.speed', s.speed || 1.0);
    updatePlayerUI();
  } catch (e) { console.error(e); }
});

$('#btn-fullscreen').addEventListener('click', () => {
  const el = $('#video-container');
  document.fullscreenElement ? document.exitFullscreen() : el.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
});

// File/URL
$('#btn-open-file').addEventListener('click', () => {
  const path = prompt('输入媒体文件路径:');
  if (path) loadMedia(path);
});

$('#btn-load-url').addEventListener('click', () => {
  const url = $('#url-input').value.trim();
  if (url) loadMedia(url);
});

// File upload
$('#btn-upload-file')?.addEventListener('click', () => {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = 'video/*';
  input.onchange = async () => {
    const file = input.files[0];
    if (!file) return;
    $('#video-status').classList.remove('hidden');
    $('#status-text').textContent = '上传中...';
    try {
      const result = await api.upload(file);
      $('#status-text').textContent = '上传完成，加载中...';
      loadVideo(result.url);
    } catch (e) {
      $('#status-text').textContent = `上传失败: ${e.message}`;
    }
  };
  input.click();
});

async function loadMedia(path) {
  try {
    $('#video-status').classList.remove('hidden');
    $('#status-text').textContent = '加载中...';
    const result = await api.play(path);
    if (result.url) {
      loadVideo(result.url);
    } else {
      $('#status-text').textContent = '错误: 未能获取播放地址';
    }
  } catch (err) {
    $('#status-text').textContent = `错误: ${err.message}`;
  }
}

// ====== Viewer Overlay ======
let overlayTimer = null;
$('#viewer-overlay').addEventListener('click', () => {
  clearTimeout(overlayTimer);
  $('#viewer-overlay').classList.add('active');
  updatePlayerUI();
  overlayTimer = setTimeout(() => $('#viewer-overlay').classList.remove('active'), 5000);
});

// ====== UI Updates ======
function updateConnectionUI() {
  const s = store.get('connection.status');
  const dot = $('#connection-status');
  if (dot) dot.className = 'status-dot ' + s;
}

function updatePlayerUI() {
  const p = store.get('playback'), d = p.duration || 0;
  const pct = d > 0 ? Math.min(100, (p.position / d) * 100) : 0;
  const bar = $('#seek-bar');
  if (bar && d > 0) {
    bar.max = 100;
    bar.value = pct;
    bar.style.background = `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--bg-tertiary) ${pct}%, var(--bg-tertiary) 100%)`;
  }
  const td = $('#time-display'); if (td) td.textContent = `${fmt(p.position)} / ${fmt(d)}`;
  const vp = $('#viewer-position');
  if (vp && d > 0) vp.style.width = `${pct}%`;
  const vt = $('#viewer-time'); if (vt) vt.textContent = `${fmt(p.position)} / ${fmt(d)}`;
  const btn = $('#btn-play-pause');
  if (btn) btn.innerHTML = p.state === 'playing'
    ? '<svg width="24" height="24" viewBox="0 0 24 24"><rect x="6" y="4" width="4" height="16" fill="currentColor"/><rect x="14" y="4" width="4" height="16" fill="currentColor"/></svg>'
    : '<svg width="24" height="24" viewBox="0 0 24 24"><polygon points="8,5 19,12 8,19" fill="currentColor"/></svg>';
}

function updateViewerCount(text) {
  // System messages only — viewer count not displayed
}

// ====== Admin ======
async function updateAdmin() {
  if (!store.get('token')) return;
  try {
    const s = await api.status();
    $('#admin-viewers').textContent = s.viewers || 0;
    $('#admin-state').textContent = s.state || '空闲';
    $('#admin-media').textContent = s.media_url || '无';
    $('#admin-transcode').textContent = s.state || '空闲';
  } catch (e) {}
}

// ====== Utils ======
function fmt(s) {
  if (!s || isNaN(s)) return '00:00';
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = Math.floor(s % 60);
  return h > 0 ? `${p(h)}:${p(m)}:${p(sec)}` : `${p(m)}:${p(sec)}`;
}
function p(n) { return String(n).padStart(2, '0'); }

// ====== Keyboard ======
document.addEventListener('keydown', (e) => {
  if (e.target.tagName === 'INPUT') return;
  if (store.get('role') !== 'host') return;
  if (e.code === 'Space') { e.preventDefault(); $('#btn-play-pause').click(); }
  if (e.code === 'ArrowLeft') { e.preventDefault(); seekRelative(-10); }
  if (e.code === 'ArrowRight') { e.preventDefault(); seekRelative(10); }
  if (e.code === 'KeyF' && !e.ctrlKey && !e.metaKey) { $('#btn-fullscreen').click(); }
});

async function seekRelative(d) {
  try { await api.seek(Math.max(0, store.get('playback.position') + d)); } catch (e) {}
}

// ====== Init ======
(function () {
  const token = store.get('token'), role = store.get('role'), path = location.pathname;
  if (token && role === 'host') {
    $('#admin-login-panel')?.classList.add('hidden');
    $('#admin-dashboard')?.classList.remove('hidden');
    enterHost();
  } else if (token && role === 'viewer') {
    enterViewer();
  } else if (path === '/admin') {
    navigate('admin');
  } else {
    navigate('login');
  }
  setInterval(() => { if (store.get('screen') === 'admin') updateAdmin(); }, 3000);
})();
