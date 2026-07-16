// Simple pub-sub state store for SyncWatch
class Store {
  constructor() {
    this.state = {
      token: localStorage.getItem('syncwatch_token') || null,
      role: localStorage.getItem('syncwatch_role') || 'viewer',
      screen: 'login',

      playback: { state: 'idle', position: 0, duration: 0, speed: 1.0, mediaURL: null },
      media: { filename: null, audioTracks: [], subtitleTracks: [], selectedAudio: 0, selectedSubtitle: -1 },
    };
    this.listeners = new Map();
  }

  get(path) {
    return path.split('.').reduce((obj, key) => obj?.[key], this.state);
  }

  set(path, value) {
    const keys = path.split('.');
    let obj = this.state;
    for (let i = 0; i < keys.length - 1; i++) {
      if (!(keys[i] in obj)) obj[keys[i]] = {};
      obj = obj[keys[i]];
    }
    obj[keys[keys.length - 1]] = value;
    this.notify(path);
  }

  on(path, fn) {
    if (!this.listeners.has(path)) this.listeners.set(path, new Set());
    this.listeners.get(path).add(fn);
    return () => this.listeners.get(path)?.delete(fn);
  }

  notify(path) {
    const fns = this.listeners.get(path);
    if (fns) fns.forEach(fn => fn(this.get(path)));
    const parts = path.split('.');
    while (parts.length > 0) {
      parts.pop();
      const parent = parts.join('.');
      if (parent) {
        const pFns = this.listeners.get(parent);
        if (pFns) pFns.forEach(fn => fn(this.get(parent)));
      }
    }
  }

  login(token, role) {
    localStorage.setItem('syncwatch_token', token);
    localStorage.setItem('syncwatch_role', role);
    this.set('token', token);
    this.set('role', role);
  }

  logout() {
    localStorage.removeItem('syncwatch_token');
    localStorage.removeItem('syncwatch_role');
    this.set('token', null);
    this.set('role', 'viewer');
    this.set('playback', { state: 'idle', position: 0, duration: 0, speed: 1.0, mediaURL: null });
    this.set('media', { filename: null, audioTracks: [], subtitleTracks: [], selectedAudio: 0, selectedSubtitle: -1 });
    this.set('screen', 'login');
  }
}

export const store = new Store();
