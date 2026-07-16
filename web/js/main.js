// SyncWatch Frontend
import { store } from './store.js';
import { api } from './api.js';

const $ = (sel) => document.querySelector(sel);

let ws = null, hls = null, syncTimer = null, reconnectTimer = null, mediaLoadID = 0;

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
  // Show overlay controls by default (progress bar + time)
  $('#viewer-overlay').classList.add('active');
  connectWebSocket();
}

function enterHost() {
  navigate('player');
  $('#host-controls').classList.remove('hidden');
  $('#viewer-overlay').classList.add('hidden');
  connectWebSocket();
  startHostSync();
}

function startHostSync() {
  stopHostSync();
  syncTimer = setInterval(() => {
    if (store.get('role') !== 'host') return;
    const video = $('#main-video');
    if (!video || !video.duration || video.paused) return;
    api.sync(video.currentTime).catch(() => {});
  }, 5000);
}

function stopHostSync() {
  if (syncTimer) { clearInterval(syncTimer); syncTimer = null; }
}

// ====== WebSocket ======
function connectWebSocket() {
  const token = store.get('token');
  if (!token) { console.warn('[SyncWatch] No token, skipping WS'); return; }
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
  clearTimeout(reconnectTimer);

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = `${proto}//${location.host}/ws?token=${encodeURIComponent(token)}`;
  console.log('[SyncWatch] WS connect');
  ws = new WebSocket(url);
  let wasOpen = false;

  ws.onopen = () => { console.log('[SyncWatch] WS open'); wasOpen = true; };
  ws.onmessage = (e) => {
    try { handleWSMessage(JSON.parse(e.data)); }
    catch (err) { console.error('[SyncWatch] Invalid WS message', err); }
  };
  ws.onclose = (ev) => {
    console.warn('[SyncWatch] WS close', ev.code);
    ws = null;
    if (!store.get('token')) return;
    if (!wasOpen) {
      api.status().catch((err) => {
        if (store.get('token')) reconnectTimer = setTimeout(connectWebSocket, 2000);
      });
      return;
    }
    reconnectTimer = setTimeout(connectWebSocket, 1000);
  };
  ws.onerror = () => console.error('[SyncWatch] WS error');
}

function handleWSMessage(msg) {
  switch (msg.type) {
    case 'joined':
      handleJoined(msg.room_state);
      break;
    case 'media':
      handleMedia(msg.media_url, true);
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
    case 'chat':
      appendChatMessage(msg);
      break;
    case 'system':
      appendChatMessage(msg);
      break;
  }
}

function handleJoined(rs) {
  if (!rs) return;
  // Only overwrite fields that are present (partial updates via broadcastRoomInfo)
  if (rs.state !== undefined) store.set('playback.state', rs.state);
  if (rs.position !== undefined) store.set('playback.position', rs.position);
  if (rs.media?.duration) store.set('playback.duration', rs.media.duration);
  if (rs.speed) {
    store.set('playback.speed', rs.speed);
    $('#main-video').playbackRate = rs.speed;
    $('#speed-select').value = String(rs.speed);
  }
  if (rs.media?.filename) store.set('media.filename', rs.media.filename);
  if (rs.subtitle?.content) initSubtitle(rs.subtitle.format, rs.subtitle.content);
  if (rs.audio_tracks) {
    const sel = $('#audio-select');
    sel.innerHTML = rs.audio_tracks.map((t, i) => `<option value="${i}">${escapeHTML(t.language || t.title || 'Track '+(i+1))}</option>`).join('');
    sel.classList.toggle('hidden', rs.audio_tracks.length <= 1);
    sel.value = rs.selected_audio || 0;
    applyAudioTrack(Number(sel.value));
  }
  if (rs.subtitle_tracks?.length) {
    const sel = $('#subtitle-select');
    sel.innerHTML = `<option value="-1">关闭</option>` + rs.subtitle_tracks.map((t, i) => `<option value="${i}">${escapeHTML(t.title || 'Sub '+(i+1))}</option>`).join('');
    sel.classList.toggle('hidden', false);
    sel.value = rs.selected_sub !== undefined ? String(rs.selected_sub) : '-1';
  }
  if (rs.selected_sub === -1) initSubtitle(null, null);
  updatePlayerUI();
}

function handleMedia(url, forceReload = false) {
  if (!url) return;
  const playableURL = authenticatedMediaURL(url);
  if (!forceReload && store.get('playback.mediaURL') === playableURL && $('#main-video').currentSrc) return;
  console.log('[SyncWatch] Loading media:', url);
  store.set('playback.mediaURL', playableURL);
  loadVideo(playableURL);
}

function handleStateUpdate(ps) {
  if (!ps) return;
  const role = store.get('role');

  // Viewer: sync to host's state
  const video = $('#main-video');
  if (role === 'viewer') {
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
  }
  if (ps.speed && video.playbackRate !== ps.speed) video.playbackRate = ps.speed;
  if (Number.isInteger(ps.audio_index)) {
    applyAudioTrack(ps.audio_index);
    const audioSelect = $('#audio-select');
    if (audioSelect) audioSelect.value = String(ps.audio_index);
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
  if (Number.isInteger(ps.audio_index)) applyAudioTrack(ps.audio_index);
  store.set('playback.position', ps.position);
  updatePlayerUI();
}

// ====== Video Player ======
async function loadVideo(url) {
  const loadID = ++mediaLoadID;
  const video = $('#main-video');
  let mediaReady = false;
  $('#video-status').classList.remove('hidden');
  $('#status-text').textContent = '加载中...';

  // Destroy previous HLS instance
  if (hls) { hls.destroy(); hls = null; }

  // Remove old timeupdate handler
  video.ontimeupdate = null;

  // Update progress bar in real-time
  video.ontimeupdate = () => {
    if (!mediaReady) return;
    store.set('playback.position', video.currentTime);
    updatePlayerUI();
  };
  video.onended = () => {
    if (mediaReady && store.get('role') === 'host' && store.get('playback.state') === 'playing') {
      api.sync(video.duration || video.currentTime)
        .then(() => api.pause())
        .catch(() => {});
    }
  };

  // Show loading when video buffers
  video.onwaiting = () => { $('#video-status').classList.remove('hidden'); $('#status-text').textContent = '缓冲中...'; };
  video.onplaying = () => { $('#video-status').classList.add('hidden'); };
  video.oncanplay = () => { $('#video-status').classList.add('hidden'); };

  const parsed = new URL(url, location.href);
  const mediaPath = parsed.searchParams.get('path') || parsed.pathname;
  const isM3U8 = /\.m3u8$/i.test(mediaPath);

  const hasNativeHLS = Boolean(video.canPlayType('application/vnd.apple.mpegurl'));
  let Hls = null;
  if (isM3U8 && !hasNativeHLS) {
    Hls = (await import('hls.js')).default;
    if (loadID !== mediaLoadID) return;
  }

  if (isM3U8 && Hls?.isSupported()) {
    hls = new Hls({
      enableWorker: true,
      lowLatencyMode: false,
      maxBufferLength: 90,
      maxMaxBufferLength: 120,
      maxBufferSize: 100 * 1000 * 1000, // 100MB
    });
    hls.loadSource(url);
    hls.attachMedia(video);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      $('#video-status').classList.add('hidden');
      store.set('playback.duration', hls.levels[0]?.details?.totalduration || video.duration || 0);
      updatePlayerUI();
      applyStoredPlaybackState();
      mediaReady = true;
    });
    hls.on(Hls.Events.AUDIO_TRACKS_UPDATED, (_, data) => {
      renderAudioTracks(data.audioTracks || []);
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
    video.onloadedmetadata = () => {
      $('#video-status').classList.add('hidden');
      store.set('playback.duration', video.duration || 0);
      updatePlayerUI();
      applyAudioTrack(Number($('#audio-select').value || 0));
      applyStoredPlaybackState();
      mediaReady = true;
    };
    video.oncanplay = () => {
      $('#video-status').classList.add('hidden');
      applyStoredPlaybackState();
    };
    video.onerror = () => {
      $('#status-text').textContent = `加载失败: ${video.error?.message || '未知错误'}`;
      $('#video-status').classList.remove('hidden');
    };
    video.load();
  }
}

// ====== Subtitle ======
async function initSubtitle(format, content) {
  // Destroy previous renderer to free WASM/worker memory
  if (window._jassub) {
    window._jassub.destroy();
    window._jassub = null;
  }

  if (!content) return;

  try {
    const JASSUB = (await import('jassub')).default;
    const video = $('#main-video');
    const jassub = new JASSUB({
      video, subContent: content,
      workerUrl: '/jassub/jassub-worker.js',
      modernWasmUrl: '/jassub/jassub-worker-modern.wasm',
      prescaleFactor: 0.75,
      blendMode: 'wasm',
      asyncRender: true,
      offscreenRender: true,
      onDemandRender: true,
      availableFonts: { 'liberation sans': '/jassub/default.woff2' },
      fallbackFont: 'liberation sans',
    });
    jassub.addEventListener('error', (event) => console.error('[SyncWatch] Subtitle renderer error:', event.error || event));
    window._jassub = jassub;
  } catch (err) { console.error('[SyncWatch] Subtitle init failed:', err); }
}

function resizeSubtitle() {
  if (!window._jassub) return;
  window._jassub.resize();
}

function applyStoredPlaybackState() {
  const video = $('#main-video');
  if (video.readyState < HTMLMediaElement.HAVE_METADATA) return;
  const playback = store.get('playback');
  const position = Math.max(0, Math.min(playback.position || 0, Number.isFinite(video.duration) ? video.duration : Infinity));
  if (Math.abs(video.currentTime - position) > 0.35) video.currentTime = position;
  video.playbackRate = playback.speed || 1;
  if (playback.state === 'playing') video.play().catch(showAudioPrompt);
  else video.pause();
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

$('#seek-bar').addEventListener('input', (e) => {
  const d = store.get('playback.duration'); if (!d) return;
  const pos = (e.target.value / 100) * d;
  if (store.get('role') === 'host') {
    $('#main-video').currentTime = pos;
    store.set('playback.position', pos);
    updatePlayerUI();
  }
  debounce('seek', () => api.seek(pos).catch(() => {}), 200);
});

$('#speed-select').addEventListener('change', async (e) => {
  const speed = parseFloat(e.target.value);
  $('#main-video').playbackRate = speed;
  store.set('playback.speed', speed);
  try { await api.speed(speed); } catch (e) { console.error(e); }
});

$('#audio-select').addEventListener('change', async (e) => {
  const index = parseInt(e.target.value);
  applyAudioTrack(index);
  try { await api.audioTrack(index); } catch (e) { console.error(e); }
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

$('#btn-fullscreen').addEventListener('click', toggleFullscreen);
$('#btn-viewer-fullscreen')?.addEventListener('click', toggleFullscreen);

$('#btn-mute').addEventListener('click', () => setMuted(!$('#main-video').muted));
$('#btn-viewer-audio')?.addEventListener('click', () => setMuted(!$('#main-video').muted));
$('#volume-bar').addEventListener('input', (e) => {
  const video = $('#main-video');
  video.volume = Number(e.target.value);
  setMuted(video.volume === 0);
});

function toggleFullscreen() {
  const el = $('#video-container');
  document.fullscreenElement ? document.exitFullscreen() : el.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
}

// File/URL
$('#btn-open-file').addEventListener('click', () => {
  openMediaLibrary();
});

$('#btn-close-media').addEventListener('click', () => $('#media-dialog').close());
$('#btn-load-path').addEventListener('click', () => {
  const path = $('#local-path-input').value.trim();
  if (!path) return;
  $('#media-dialog').close();
  loadMedia(path);
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
      handleMedia(result.url);
    } catch (e) {
      $('#status-text').textContent = `上传失败: ${e.message}`;
    }
  };
  input.click();
});

// Subtitle upload
$('#btn-upload-sub')?.addEventListener('click', () => {
  const input = document.createElement('input');
  input.type = 'file';
  input.accept = '.srt,.ass,.ssa,.vtt';
  input.onchange = async () => {
    const file = input.files[0];
    if (!file) return;
    try {
      const data = await api.uploadSubtitle(file);
      console.log('[SyncWatch] Subtitle loaded:', data.format);
    } catch (e) {
      console.error('Subtitle upload failed:', e);
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
      handleMedia(result.url);
    } else {
      $('#status-text').textContent = '错误: 未能获取播放地址';
    }
  } catch (err) {
    $('#status-text').textContent = `错误: ${err.message}`;
  }
}

async function openMediaLibrary() {
  const dialog = $('#media-dialog');
  const list = $('#media-list');
  list.textContent = '正在扫描...';
  dialog.showModal();
  try {
    const result = await api.mediaScan();
    list.replaceChildren();
    if (!result.files?.length) {
      list.textContent = '配置的媒体目录中没有找到可播放文件';
      return;
    }
    result.files.forEach((file) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'media-item';
      const name = document.createElement('span');
      name.textContent = file.path.split(/[\\/]/).pop();
      name.title = file.path;
      const format = document.createElement('small');
      format.textContent = file.format || '';
      button.append(name, format);
      button.addEventListener('click', () => {
        dialog.close();
        loadMedia(file.path);
      });
      list.append(button);
    });
  } catch (err) {
    list.textContent = `扫描失败: ${err.message}`;
  }
}

// ====== Audio ======
function setMuted(muted) {
  const video = $('#main-video');
  video.muted = muted;
  if (!muted && video.volume === 0) video.volume = 1;
  $('#btn-mute').textContent = muted ? '🔇' : '🔊';
  const viewerButton = $('#btn-viewer-audio');
  if (viewerButton) viewerButton.textContent = muted ? '启用声音' : '静音';
  if (!muted) video.play().catch(() => {});
}

function showAudioPrompt() {
  const button = $('#btn-viewer-audio');
  if (button) button.textContent = '点击播放声音';
}

function applyAudioTrack(index) {
  if (hls && hls.audioTracks?.length) {
    hls.audioTrack = index;
    return;
  }
  const tracks = $('#main-video').audioTracks;
  if (!tracks) return;
  for (let i = 0; i < tracks.length; i++) tracks[i].enabled = i === index;
}

function renderAudioTracks(tracks) {
  if (!tracks.length) return;
  const select = $('#audio-select');
  select.replaceChildren(...tracks.map((track, index) => {
    const option = document.createElement('option');
    option.value = String(index);
    option.textContent = track.name || track.lang || `Track ${index + 1}`;
    return option;
  }));
  select.classList.toggle('hidden', tracks.length <= 1);
  select.value = String(Math.max(0, hls?.audioTrack || 0));
}

// ====== Chat ======
function toggleChat(force) {
  const panel = $('#chat-panel');
  panel.classList.toggle('hidden', force === undefined ? !panel.classList.contains('hidden') : !force);
  if (!panel.classList.contains('hidden')) {
    $('#chat-messages').scrollTop = $('#chat-messages').scrollHeight;
    $('#chat-input').focus();
  }
}

$('#btn-chat').addEventListener('click', () => toggleChat());
$('#btn-viewer-chat').addEventListener('click', () => toggleChat());
$('#btn-close-chat').addEventListener('click', () => toggleChat(false));
$('#chat-form').addEventListener('submit', (e) => {
  e.preventDefault();
  const input = $('#chat-input');
  const text = input.value.trim();
  if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ type: 'chat', text }));
  input.value = '';
});

function appendChatMessage(message) {
  const item = document.createElement('div');
  item.className = `chat-message${message.system ? ' system' : ''}`;
  if (!message.system) {
    const meta = document.createElement('small');
    const time = message.timestamp ? new Date(message.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
    meta.textContent = `${message.from || 'Viewer'}${time ? ` · ${time}` : ''}`;
    item.append(meta);
  }
  const text = document.createElement('div');
  text.textContent = message.text || '';
  item.append(text);
  const messages = $('#chat-messages');
  messages.append(item);
  while (messages.children.length > 200) messages.firstElementChild.remove();
  messages.scrollTop = messages.scrollHeight;
}

function logout() {
  clearTimeout(reconnectTimer);
  stopHostSync();
  const current = ws;
  ws = null;
  mediaLoadID++;
  if (hls) { hls.destroy(); hls = null; }
  const video = $('#main-video');
  video.pause();
  video.removeAttribute('src');
  video.load();
  video.muted = true;
  initSubtitle(null, null);
  $('#chat-messages').replaceChildren();
  $('#chat-panel').classList.add('hidden');
  store.logout();
  current?.close(1000, 'logout');
  navigate(location.pathname === '/admin' ? 'admin' : 'login');
}

$('#btn-logout').addEventListener('click', logout);
$('#btn-viewer-logout').addEventListener('click', logout);

// ====== Viewer Overlay ======
let overlayTimer = null;
// Show overlay on any tap/click in the video area
$('#video-container').addEventListener('click', () => {
  clearTimeout(overlayTimer);
  $('#viewer-overlay').classList.add('active');
  updatePlayerUI();
  overlayTimer = setTimeout(() => $('#viewer-overlay').classList.remove('active'), 4000);
});

// ====== UI Updates ======
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
  if (btn) {
    btn.disabled = !p.mediaURL;
    btn.innerHTML = p.state === 'playing'
      ? '<svg width="24" height="24" viewBox="0 0 24 24"><rect x="6" y="4" width="4" height="16" fill="currentColor"/><rect x="14" y="4" width="4" height="16" fill="currentColor"/></svg>'
      : '<svg width="24" height="24" viewBox="0 0 24 24"><polygon points="8,5 19,12 8,19" fill="currentColor"/></svg>';
  }
  if (bar) bar.disabled = !p.mediaURL;
}

// ====== Admin ======
async function updateAdmin() {
  if (!store.get('token')) return;
  try {
    const s = await api.status();
    $('#admin-state').textContent = s.state || '空闲';
    $('#admin-media').textContent = s.media_url || '无';
  } catch (e) {}
}

// ====== Utils ======
const _debounceTimers = {};
function debounce(key, fn, ms) {
  clearTimeout(_debounceTimers[key]);
  _debounceTimers[key] = setTimeout(fn, ms);
}
function authenticatedMediaURL(rawURL) {
  const url = new URL(rawURL, location.href);
  if (url.origin === location.origin && url.pathname === '/api/media/file') {
    const token = store.get('token');
    if (token) url.searchParams.set('token', token);
  }
  return url.href;
}
function escapeHTML(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}
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
  const position = Math.max(0, store.get('playback.position') + d);
  $('#main-video').currentTime = position;
  store.set('playback.position', position);
  try { await api.seek(position); } catch (e) { console.error(e); }
}

// ====== Init ======
window.addEventListener('syncwatch:unauthorized', logout);

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
  window.addEventListener('resize', resizeSubtitle);
  document.addEventListener('fullscreenchange', () => setTimeout(resizeSubtitle, 200));
  setInterval(() => { if (store.get('screen') === 'admin') updateAdmin(); }, 30000);
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(() => {});
  }
})();
