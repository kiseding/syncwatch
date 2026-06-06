// SyncWatch Frontend
import { store } from './store.js';
import { api } from './api.js';
import JASSUB from 'jassub';

const $ = (sel) => document.querySelector(sel);

let pc = null, ws = null, subRenderer = null;
let reconnectAttempts = 0, reconnectTimer = null;
const MAX_RECONNECT = 20;

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
  document.getElementById('player-screen').classList.add('web-fullscreen');
  $('#host-controls').classList.add('hidden');
  $('#viewer-overlay').classList.remove('hidden');
  connectWebSocket();
}

function enterHost() {
  navigate('player');
  $('#host-controls').classList.remove('hidden');
  $('#viewer-overlay').classList.add('hidden');
  document.getElementById('player-screen').classList.remove('web-fullscreen');
  connectWebSocket();
}

// ====== WebSocket ======
function connectWebSocket() {
  const token = store.get('token');
  if (!token) return;
  store.set('connection.status', 'connecting');
  updateConnectionUI();

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws?token=${encodeURIComponent(token)}`);

  ws.onopen = () => { reconnectAttempts = 0; console.log('[WS] open'); };
  ws.onmessage = (e) => handleWSMessage(JSON.parse(e.data));
  ws.onclose = () => {
    store.set('connection.status', 'disconnected');
    updateConnectionUI();
    scheduleReconnect();
  };
  ws.onerror = () => {};
}

function scheduleReconnect() {
  clearTimeout(reconnectTimer);
  if (reconnectAttempts >= MAX_RECONNECT) return;
  const delay = Math.min(1000 * Math.pow(1.5, reconnectAttempts), 30000);
  reconnectAttempts++;
  showReconnect(true);
  reconnectTimer = setTimeout(connectWebSocket, delay);
}

function handleWSMessage(msg) {
  switch (msg.type) {
    case 'offer':        handleOffer(msg.sdp); break;
    case 'ice-candidate': handleICECandidate(msg); break;
    case 'joined':       handleJoined(msg.room_state); break;
    case 'state':        handleStateUpdate(msg.play_state); break;
    case 'sync':         handleStateUpdate(msg.play_state); break;
    case 'subtitle':     initSubtitle(msg.from, msg.text); break;
    case 'system':       updateViewerCount(msg.text); break;
    case 'error':        console.error(msg.message); break;
  }
}

// ====== WebRTC ======
async function handleOffer(sdp) {
  try {
    if (pc) { pc.close(); pc = null; }
    pc = new RTCPeerConnection({ iceServers: [{ urls: 'stun:stun.l.google.com:19302' }] });

    pc.ontrack = (event) => {
      if (event.track.kind === 'video') {
        $('#main-video').srcObject = event.streams[0];
        hideVideoStatus(); showReconnect(false);
      }
    };

    pc.onicecandidate = (event) => {
      if (event.candidate && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'ice-candidate', candidate: event.candidate.candidate,
          sdp_mid: event.candidate.sdpMid, sdp_mline_index: event.candidate.sdpMLineIndex }));
      }
    };

    pc.oniceconnectionstatechange = () => {
      store.set('connection.iceState', pc.iceConnectionState);
      updateConnectionUI();
      if (pc.iceConnectionState === 'disconnected' || pc.iceConnectionState === 'failed') {
        showReconnect(true);
      } else if (pc.iceConnectionState === 'connected' || pc.iceConnectionState === 'completed') {
        showReconnect(false);
      }
    };

    pc.ondatachannel = (event) => {
      if (event.channel.label === 'sync') {
        event.channel.onmessage = (e) => {
          const m = JSON.parse(e.data);
          if (m.type === 'position') store.set('playback.position', m.t);
          else if (m.type === 'pause' || m.type === 'resume' || m.type === 'seek') {
            store.set('playback.position', m.position);
            store.set('playback.state', m.type === 'pause' ? 'paused' : 'playing');
            updatePlayerUI();
          }
        };
      }
    };

    await pc.setRemoteDescription({ type: 'offer', sdp });
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'answer', sdp: answer.sdp }));
    }
  } catch (err) { console.error('[WebRTC]', err); }
}

async function handleICECandidate(msg) {
  try {
    if (pc && msg.candidate) await pc.addIceCandidate({ candidate: msg.candidate, sdpMid: msg.sdp_mid, sdpMLineIndex: msg.sdp_mline_index });
  } catch (e) {}
}

function handleJoined(rs) {
  if (!rs) return;
  store.set('playback', { state: rs.state, position: rs.position || 0, duration: rs.media?.duration || 0, speed: rs.speed || 1.0 });
  if (rs.media) store.set('media.filename', rs.media.filename);
  if (rs.subtitle?.content) initSubtitle(rs.subtitle.format, rs.subtitle.content);
  if (rs.audio_tracks) {
    const sel = $('#audio-select');
    sel.innerHTML = rs.audio_tracks.map((t, i) => `<option value="${i}">${t.language || t.title || 'Track '+(i+1)}</option>`).join('');
    sel.classList.toggle('hidden', rs.audio_tracks.length <= 1);
    sel.value = rs.selected_audio || 0;
  }
  showReconnect(false);
  updatePlayerUI();
}

function handleStateUpdate(ps) {
  if (!ps) return;
  store.set('playback.state', ps.playing ? 'playing' : 'paused');
  store.set('playback.position', ps.position);
  store.set('playback.speed', ps.speed || 1.0);
  updatePlayerUI();
}

// ====== Subtitle ======
async function initSubtitle(format, content) {
  if (subRenderer) { try { subRenderer.destroy(); } catch(e) {} subRenderer = null; }
  if (!content) return;
  try {
    const video = $('#main-video'), canvas = $('#subtitle-canvas');
    canvas.width = video.clientWidth || 1280;
    canvas.height = video.clientHeight || 720;
    subRenderer = new JASSUB({
      video, canvas, subContent: content,
      workerUrl: '/jassub/jassub-worker.js',
      modernWasmUrl: '/jassub/jassub-worker-modern.wasm',
      prescaleFactor: 0.5, blendMode: 'js', asyncRender: true, targetFps: 30,
    });
  } catch (err) { console.error('[Sub]', err); }
}

// ====== Host Controls ======
$('#btn-play-pause').addEventListener('click', async () => {
  try { store.get('playback.state') === 'playing' ? await api.pause() : await api.resume(); } catch(e) {}
});

$('#seek-bar').addEventListener('input', async (e) => {
  const d = store.get('playback.duration'); if (!d) return;
  try { await api.seek((e.target.value / 100) * d); } catch(e) {}
});

$('#speed-select').addEventListener('change', async (e) => {
  try { await api.speed(parseFloat(e.target.value)); } catch(e) {}
});

$('#audio-select').addEventListener('change', async (e) => {
  try { await api.audioTrack(parseInt(e.target.value)); } catch(e) {}
});

$('#btn-force-sync').addEventListener('click', async () => {
  try {
    const s = await api.state();
    store.set('playback.state', s.state === 'playing' ? 'playing' : 'paused');
    store.set('playback.position', s.position || 0);
    store.set('playback.speed', s.speed || 1.0);
    updatePlayerUI();
  } catch(e) {}
});

$('#btn-web-fullscreen').addEventListener('click', () => {
  document.getElementById('player-screen').classList.toggle('web-fullscreen');
});

$('#btn-fullscreen').addEventListener('click', () => {
  const el = $('#video-container');
  document.fullscreenElement ? document.exitFullscreen() : el.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
});

document.addEventListener('fullscreenchange', () => {
  $('#main-video').style.objectFit = document.fullscreenElement ? 'contain' : '';
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

async function loadMedia(path) {
  try {
    $('#video-status').classList.remove('hidden');
    $('#status-text').textContent = '加载中...';
    const result = await api.play(path);
    if (result.status === 'loading') {
      $('#status-text').textContent = '正在初始化流媒体...';
      for (let i = 0; i < 60; i++) {
        await new Promise(r => setTimeout(r, 1000));
        try { const s = await api.state(); if (s.state === 'playing') { $('#video-status').classList.add('hidden'); updatePlayerUI(); return; } } catch(e) {}
      }
      $('#status-text').textContent = '启动超时';
    } else {
      $('#video-status').classList.add('hidden');
    }
  } catch (err) { $('#status-text').textContent = `错误: ${err.message}`; }
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
  const bar = $('#seek-bar'); if (bar && d > 0) { bar.max = 100; bar.value = (p.position / d) * 100; }
  const td = $('#time-display'); if (td) td.textContent = `${fmt(p.position)} / ${fmt(d)}`;
  const vp = $('#viewer-position'); if (vp && d > 0) vp.style.width = `${(p.position / d) * 100}%`;
  const vt = $('#viewer-time'); if (vt) vt.textContent = `${fmt(p.position)} / ${fmt(d)}`;
  const btn = $('#btn-play-pause');
  if (btn) btn.innerHTML = p.state === 'playing'
    ? '<svg width="24" height="24" viewBox="0 0 24 24"><rect x="6" y="4" width="4" height="16" fill="currentColor"/><rect x="14" y="4" width="4" height="16" fill="currentColor"/></svg>'
    : '<svg width="24" height="24" viewBox="0 0 24 24"><polygon points="8,5 19,12 8,19" fill="currentColor"/></svg>';
}

function showReconnect(show) {
  const reEl = $('#reconnect-overlay');
  if (reEl) reEl.classList.toggle('hidden', !show);
  if (show) store.set('connection.status', 'connecting'); else store.set('connection.status', 'connected');
  updateConnectionUI();
}

function hideVideoStatus() { $('#video-status').classList.add('hidden'); }

function updateViewerCount(text) {
  const el = $('#viewer-count');
  if (el) { el.textContent = text; setTimeout(() => { el.textContent = `${store.get('viewers') || 0} 人在线`; }, 3000); }
}

// ====== Admin ======
async function updateAdmin() {
  try {
    const s = await api.status();
    $('#admin-viewers').textContent = s.viewers || 0;
    $('#admin-state').textContent = s.state || '空闲';
    $('#admin-media').textContent = s.media?.filename || '无';
    $('#admin-transcode').textContent = s.state || '空闲';
  } catch(e) {}
}

// ====== Utils ======
function fmt(s) { if (!s || isNaN(s)) return '00:00'; const h=Math.floor(s/3600),m=Math.floor((s%3600)/60),sec=Math.floor(s%60); return h>0?`${p(h)}:${p(m)}:${p(sec)}`:`${p(m)}:${p(sec)}`; }
function p(n) { return String(n).padStart(2,'0'); }

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
  try { await api.seek(Math.max(0, store.get('playback.position') + d)); } catch(e) {}
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
