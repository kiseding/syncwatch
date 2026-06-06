// SyncWatch Frontend - Main Application
import { store } from './store.js';
import { api } from './api.js';
import JASSUB from 'jassub';

// ====== DOM Refs ======
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// ====== State ======
let pc = null;
let ws = null;
let syncChannel = null;
let reconnectTimer = null;
let reconnectAttempts = 0;
const MAX_RECONNECT = 10;

// ====== Navigation ======
window.navigate = function (screen) {
  $$('.screen').forEach(s => s.classList.remove('active'));
  const el = document.getElementById(`${screen}-screen`);
  if (el) {
    el.classList.add('active');
    store.set('screen', screen);
    if (screen === 'admin') updateAdmin();
  }
};

// ====== Login ======
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const password = $('#password-input').value;
  const btn = $('#login-btn');
  const errorEl = $('#login-error');

  btn.disabled = true;
  btn.textContent = '验证中...';
  errorEl.classList.add('hidden');

  try {
    const result = await api.login(password);
    store.login(result.token, result.role);
    $('#password-input').value = '';
    navigate('player');
    connectWebSocket();
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    btn.disabled = false;
    btn.textContent = '进入观影室';
  }
});

// ====== WebSocket ======
function connectWebSocket() {
  const token = store.get('token');
  if (!token) return;

  store.set('connection.status', 'connecting');
  updateConnectionUI();

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${location.host}/ws?token=${encodeURIComponent(token)}`;

  ws = new WebSocket(wsUrl);

  ws.onopen = () => {
    reconnectAttempts = 0;
    console.log('[WS] connected');
  };

  ws.onmessage = async (event) => {
    const msg = JSON.parse(event.data);
    handleWSMessage(msg);
  };

  ws.onclose = () => {
    console.log('[WS] disconnected');
    store.set('connection.status', 'disconnected');
    updateConnectionUI();
    scheduleReconnect();
  };

  ws.onerror = (err) => {
    console.error('[WS] error', err);
  };
}

function scheduleReconnect() {
  if (reconnectAttempts >= MAX_RECONNECT) return;
  const delay = Math.min(1000 * Math.pow(1.5, reconnectAttempts), 30000);
  reconnectAttempts++;
  console.log(`[WS] reconnecting in ${delay}ms (attempt ${reconnectAttempts})`);
  showReconnect(true);

  reconnectTimer = setTimeout(() => {
    connectWebSocket();
  }, delay);
}

function handleWSMessage(msg) {
  switch (msg.type) {
    case 'offer':
      handleOffer(msg.sdp);
      break;
    case 'ice-candidate':
      handleICECandidate(msg);
      break;
    case 'joined':
      handleJoined(msg.room_state);
      break;
    case 'state':
      handleStateUpdate(msg.play_state);
      break;
    case 'sync':
      handleStateUpdate(msg.play_state);
      break;
    case 'chat':
      addChatMessage(msg.from, msg.text, false);
      break;
    case 'subtitle':
      initSubtitle(msg.from, msg.text);
      break;
    case 'system':
      addChatMessage(null, msg.text, true);
      break;
    case 'error':
      store.set('error', msg.message);
      break;
  }
}

// ====== WebRTC ======
async function handleOffer(sdp) {
  try {
    pc = new RTCPeerConnection({
      iceServers: [{ urls: 'stun:stun.l.google.com:19302' }],
    });

    pc.ontrack = (event) => {
      const video = $('#main-video');
      if (event.track.kind === 'video') {
        video.srcObject = event.streams[0];
        hideVideoStatus();
        showReconnect(false);
      }
    };

    pc.onicecandidate = (event) => {
      if (event.candidate) {
        ws.send(JSON.stringify({
          type: 'ice-candidate',
          candidate: event.candidate.candidate,
          sdp_mid: event.candidate.sdpMid,
          sdp_mline_index: event.candidate.sdpMLineIndex,
        }));
      }
    };

    pc.oniceconnectionstatechange = () => {
      store.set('connection.iceState', pc.iceConnectionState);
      updateConnectionUI();
      if (pc.iceConnectionState === 'disconnected' || pc.iceConnectionState === 'failed') {
        showReconnect(true);
      }
    };

    pc.ondatachannel = (event) => {
      const dc = event.channel;
      if (dc.label === 'sync') {
        syncChannel = dc;
        dc.onmessage = (e) => {
          const syncMsg = JSON.parse(e.data);
          if (syncMsg.type === 'position') {
            store.set('playback.position', syncMsg.t);
          }
        };
      }
    };

    await pc.setRemoteDescription({ type: 'offer', sdp });
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    ws.send(JSON.stringify({ type: 'answer', sdp: answer.sdp }));
  } catch (err) {
    console.error('[WebRTC] error:', err);
  }
}

async function handleICECandidate(msg) {
  try {
    if (pc && msg.candidate) {
      await pc.addIceCandidate({
        candidate: msg.candidate,
        sdpMid: msg.sdp_mid,
        sdpMLineIndex: msg.sdp_mline_index,
      });
    }
  } catch (err) {
    console.error('[ICE] error:', err);
  }
}

function handleJoined(roomState) {
  if (!roomState) return;
  store.set('playback', {
    state: roomState.state,
    position: roomState.position || 0,
    duration: roomState.media?.duration || 0,
    speed: roomState.speed || 1.0,
  });

  if (roomState.media) {
    store.set('media.filename', roomState.media.filename);
  }

  // Initialize subtitle if provided
  if (roomState.subtitle && roomState.subtitle.content) {
    initSubtitle(roomState.subtitle.format, roomState.subtitle.content);
  }

  // Update audio track selector
  if (roomState.audio_tracks) {
    const sel = $('#audio-select');
    sel.innerHTML = roomState.audio_tracks.map((t, i) =>
      `<option value="${i}">${t.language || t.title || 'Track ' + (i+1)}</option>`
    ).join('');
    sel.classList.toggle('hidden', roomState.audio_tracks.length <= 1);
    sel.value = roomState.selected_audio || 0;
  }

  showReconnect(false);
  updatePlayerUI();
}

// ====== Subtitle ======
let subRenderer = null;

async function initSubtitle(format, content) {
  // Clean up previous renderer
  if (subRenderer) {
    try { subRenderer.destroy(); } catch(e) {}
    subRenderer = null;
  }

  if (!content) return;

  try {
    const video = $('#main-video');
    const canvas = $('#subtitle-canvas');

    // Resize canvas to match video
    canvas.width = video.clientWidth || 1280;
    canvas.height = video.clientHeight || 720;
    canvas.style.display = 'block';

    subRenderer = new JASSUB({
      video,
      canvas,
      subContent: content,
      workerUrl: '/jassub/jassub-worker.js',
      modernWasmUrl: '/jassub/jassub-worker-modern.wasm',
      prescaleFactor: 0.5,
      blendMode: 'js',
      asyncRender: true,
      targetFps: 30,
    });

    console.log('[Sub] renderer initialized, format:', format);
  } catch (err) {
    console.error('[Sub] init failed:', err);
  }
}
// =======

function handleStateUpdate(ps) {
  if (!ps) return;
  store.set('playback.state', ps.playing ? 'playing' : 'paused');
  store.set('playback.position', ps.position);
  store.set('playback.speed', ps.speed || 1.0);
  updatePlayerUI();
}

// ====== Chat ======
function addChatMessage(from, text, isSystem) {
  const messages = store.get('chat.messages');
  messages.push({ from, text, system: isSystem, time: Date.now() });
  if (messages.length > 200) messages.shift();
  store.set('chat.messages', messages);
  renderChatMessages();
}

function renderChatMessages() {
  const container = $('#chat-messages');
  const messages = store.get('chat.messages');
  container.innerHTML = messages.slice(-50).map(m => {
    if (m.system) {
      return `<div class="chat-msg system">${escapeHtml(m.text)}</div>`;
    }
    return `<div class="chat-msg"><span class="sender">${escapeHtml(m.from)}</span>${escapeHtml(m.text)}</div>`;
  }).join('');
  container.scrollTop = container.scrollHeight;
}

$('#chat-form').addEventListener('submit', (e) => {
  e.preventDefault();
  const input = $('#chat-input');
  const text = input.value.trim();
  if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ type: 'chat', text }));
  addChatMessage('我', text, false);
  input.value = '';
});

// ====== Host Controls ======
function showHostControls(show) {
  $('#host-controls').classList.toggle('hidden', !show);
}

// Show controls for host role (based on login, not auto-detect)
store.on('role', (role) => {
  showHostControls(role === 'host');
});

// Initial check from stored role
if (store.get('role') === 'host') showHostControls(true);

// Play/Pause
$('#btn-play-pause').addEventListener('click', async () => {
  const state = store.get('playback.state');
  try {
    if (state === 'playing') {
      await api.pause();
    } else {
      await api.resume();
    }
  } catch (err) {
    console.error(err);
  }
});

// Seek
$('#seek-bar').addEventListener('input', async (e) => {
  const duration = store.get('playback.duration');
  if (!duration) return;
  const position = (e.target.value / 100) * duration;
  try {
    await api.seek(position);
  } catch (err) {
    console.error(err);
  }
});

// Speed
$('#speed-select').addEventListener('change', async (e) => {
  try {
    await api.speed(parseFloat(e.target.value));
  } catch (err) {
    console.error(err);
  }
});

// Audio track
$('#audio-select').addEventListener('change', async (e) => {
  try {
    await api.audioTrack(parseInt(e.target.value));
  } catch (err) {
    console.error(err);
  }
});

// File picker
$('#btn-open-file').addEventListener('click', () => {
  // For host, show a file path input (browser can't browse filesystem directly)
  const path = prompt('输入媒体文件路径（服务器本地路径）:');
  if (path) {
    loadMedia(path);
  }
});

$('#btn-load-url').addEventListener('click', () => {
  const url = $('#url-input').value.trim();
  if (url) {
    loadMedia(url);
  }
});

async function loadMedia(path) {
  try {
    $('#video-status').classList.remove('hidden');
    $('#status-text').textContent = '加载中...';
    const result = await api.play(path);

    // If async loading, poll until playing
    if (result.status === 'loading') {
      $('#status-text').textContent = '正在初始化流媒体...';
      for (let i = 0; i < 30; i++) {
        await new Promise(r => setTimeout(r, 1000));
        try {
          const s = await api.state();
          if (s.state === 'playing') {
            $('#status-text').textContent = '';
            $('#video-status').classList.add('hidden');
            updatePlayerUI();
            return;
          }
        } catch(e) {}
      }
      $('#status-text').textContent = '启动超时，请刷新重试';
      return;
    }

    $('#status-text').textContent = '';
    $('#video-status').classList.add('hidden');
  } catch (err) {
    $('#status-text').textContent = `错误: ${err.message}`;
    console.error(err);
  }
}

// ====== UI Updates ======
function updateConnectionUI() {
  const status = store.get('connection.status');
  const dot = $('#connection-status');
  dot.className = 'status-dot ' + status;
}

function updatePlayerUI() {
  const playback = store.get('playback');
  const duration = playback.duration || 0;

  // Seek bar
  const seekBar = $('#seek-bar');
  if (duration > 0) {
    seekBar.max = 100;
    seekBar.value = (playback.position / duration) * 100;
  }

  // Time display
  $('#time-display').textContent =
    `${formatTime(playback.position)} / ${formatTime(duration)}`;

  // Play/Pause icon
  const btn = $('#btn-play-pause');
  if (playback.state === 'playing') {
    btn.innerHTML = '<svg width="24" height="24" viewBox="0 0 24 24"><rect x="6" y="4" width="4" height="16" fill="currentColor"/><rect x="14" y="4" width="4" height="16" fill="currentColor"/></svg>';
  } else {
    btn.innerHTML = '<svg width="24" height="24" viewBox="0 0 24 24"><polygon points="8,5 19,12 8,19" fill="currentColor"/></svg>';
  }
}

function showReconnect(show) {
  $('#reconnect-overlay').classList.toggle('hidden', !show);
  if (show) {
    store.set('connection.status', 'connecting');
    updateConnectionUI();
  } else {
    store.set('connection.status', 'connected');
    updateConnectionUI();
  }
}

function hideVideoStatus() {
  $('#video-status').classList.add('hidden');
  $('#status-text').textContent = '';
}

// ====== Admin ======
async function updateAdmin() {
  try {
    const stats = await api.status();
    $('#admin-viewers').textContent = stats.viewers || 0;
    $('#admin-state').textContent = stats.state || '空闲';
    $('#admin-media').textContent = stats.media?.filename || '无';
    $('#admin-transcode').textContent = stats.state || '空闲';
  } catch (err) {
    console.error(err);
  }
}

// ====== Utilities ======
function formatTime(seconds) {
  if (!seconds || isNaN(seconds)) return '00:00';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${pad(h)}:${pad(m)}:${pad(s)}`;
  return `${pad(m)}:${pad(s)}`;
}

function pad(n) { return String(n).padStart(2, '0'); }

function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// ====== Keyboard ======
document.addEventListener('keydown', (e) => {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;

  const role = store.get('role');
  if (role !== 'host') return;

  switch (e.code) {
    case 'Space':
      e.preventDefault();
      $('#btn-play-pause').click();
      break;
    case 'ArrowLeft':
      e.preventDefault();
      seekRelative(-10);
      break;
    case 'ArrowRight':
      e.preventDefault();
      seekRelative(10);
      break;
    case 'KeyC':
      if (e.ctrlKey || e.metaKey) return;
      const chatInput = $('#chat-input');
      chatInput.focus();
      break;
  }
});

async function seekRelative(delta) {
  const pos = store.get('playback.position');
  try {
    await api.seek(Math.max(0, pos + delta));
  } catch (err) {
    console.error(err);
  }
}

// ====== Admin Login ======
$('#admin-login-form')?.addEventListener('submit', async (e) => {
  e.preventDefault();
  const password = $('#admin-password-input').value;
  const btn = $('#admin-login-btn');
  const errorEl = $('#admin-login-error');

  btn.disabled = true;
  btn.textContent = '验证中...';
  errorEl.classList.add('hidden');

  try {
    const result = await api.adminLogin(password);
    store.login(result.token, result.role);
    $('#admin-password-input').value = '';
    $('#admin-login-panel').classList.add('hidden');
    $('#admin-dashboard').classList.remove('hidden');
    showHostControls(true);
    navigate('player');
    connectWebSocket();
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    btn.disabled = false;
    btn.textContent = '进入控制台';
  }
});

// ====== Initialization ======
(function init() {
  const token = store.get('token');
  const role = store.get('role');
  const path = window.location.pathname;

  if (token && role === 'host') {
    showHostControls(true);
    $('#admin-login-panel')?.classList.add('hidden');
    $('#admin-dashboard')?.classList.remove('hidden');
    navigate('player');
    connectWebSocket();
  } else if (token && role === 'viewer') {
    navigate('player');
    connectWebSocket();
  } else if (path === '/admin') {
    navigate('admin');
  } else {
    navigate('login');
  }

  // Periodic status updates
  setInterval(() => {
    if (store.get('screen') === 'admin') updateAdmin();
  }, 3000);
})();
