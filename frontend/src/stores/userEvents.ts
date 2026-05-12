import { defineStore } from 'pinia';

// userEventsStore owns the single per-tab /ws/user WebSocket connection.
// It's a thin pipe — feature stores (invites, friends, matchmaking)
// register handlers by event-type string and receive payloads. Keeping
// the WS wiring here (and not in each feature store) means we never
// open more than one user-channel WS per tab.
//
// Reconnect strategy is exponential-backoff capped at 30s. We don't
// queue outgoing messages because /ws/user is push-only — every
// state-mutating action goes through HTTP for idempotency.

type Handler = (payload: any) => void;

function wsUrl(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}/ws/user`;
}

export const useUserEventsStore = defineStore('userEvents', {
  state: () => ({
    connected: false as boolean,
    socket: null as WebSocket | null,
    handlers: new Map<string, Set<Handler>>(),
    attempts: 0 as number,
    closeRequested: false as boolean,
  }),
  actions: {
    on(type: string, fn: Handler) {
      let set = this.handlers.get(type);
      if (!set) {
        set = new Set();
        this.handlers.set(type, set);
      }
      set.add(fn);
      return () => set!.delete(fn);
    },
    connect() {
      if (this.socket || this.closeRequested) return;
      const ws = new WebSocket(wsUrl());
      this.socket = ws;
      ws.onopen = () => {
        this.connected = true;
        this.attempts = 0;
      };
      ws.onmessage = (ev) => {
        try {
          const env = JSON.parse(ev.data);
          const set = this.handlers.get(env.type);
          if (set) for (const h of set) h(env.payload);
        } catch {
          // Drop garbage frames silently — the server only emits JSON.
        }
      };
      ws.onclose = () => {
        this.connected = false;
        this.socket = null;
        if (this.closeRequested) return;
        // Exponential backoff: 0.5s, 1s, 2s, 4s, 8s, 16s, then cap at 30s.
        const delay = Math.min(30000, 500 * Math.pow(2, this.attempts++));
        setTimeout(() => this.connect(), delay);
      };
      ws.onerror = () => {
        ws.close();
      };
    },
    disconnect() {
      this.closeRequested = true;
      if (this.socket) {
        this.socket.close();
        this.socket = null;
      }
      this.connected = false;
    },
    reset() {
      this.closeRequested = false;
      this.attempts = 0;
    },
  },
});
