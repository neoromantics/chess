import { StateJSON, HintResponse, InviteWire, UserSummary } from './types';

const API_BASE = (import.meta.env.VITE_API_URL as string) || '';

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const url = `${API_BASE}${path}`;
  // credentials: 'include' so cookies (session, JWT) are sent even in
  // dev where Vite runs on a different port than the API.
  const response = await fetch(url, { credentials: 'include', ...options });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `Request failed with status ${response.status}`);
  }
  if (response.status === 204 || response.status === 202) return null as T;
  return response.json();
}

export const api = {
  // ===== Auth + profile =====
  signup: (username: string, password: string) => request<any>('/api/auth/signup', {
    method: 'POST',
    body: JSON.stringify({ username, password })
  }),
  login: (username: string, password: string) => request<any>('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password })
  }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  // Live availability probe for the Signup form. `reason` is one of
  // 'empty' | 'too_short' | 'too_long' | 'taken' (when available=false)
  // or absent otherwise.
  checkUsername: (username: string) =>
    request<{ available: boolean; reason?: string }>(
      `/api/auth/check-username?username=${encodeURIComponent(username)}`
    ),
  getMe: () => request<any>('/api/user/me'),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('/api/user/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    }),
  getUserStats: (userId?: number) => request<{ games_played: number; wins: number; losses: number; draws: number; current_streak: number }>('/api/user/stats' + (userId ? `?user_id=${userId}` : '')),
  getUserProfile: (username: string) => request<any>(`/api/user/profile?username=${username}`),

  // ===== Game management =====
  createGame: (opts?: { white_think_time?: number; black_think_time?: number; engine_white?: boolean; engine_black?: boolean; }) =>
    request<{ game_id: string }>('/api/games/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(opts ?? {}),
    }),
  listGames: () => request<any[]>('/api/games'),
  deleteGame: (gameId: string) => request<void>(`/api/games/delete?game_id=${gameId}`, { method: 'DELETE' }),

  // ===== Matchmaking =====
  joinQueue: (time_control: string) => request<void>('/api/matchmaking/join', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ time_control }),
  }),
  leaveQueue: (time_control: string) => request<void>('/api/matchmaking/leave', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ time_control }),
  }),

  // ===== Invites =====
  searchUsers: (q: string) => request<UserSummary[]>(`/api/users/search?q=${encodeURIComponent(q)}`),
  listPendingInvites: () => request<{ received: InviteWire[]; sent: InviteWire[] }>('/api/invites/pending'),
  sendInvite: (body: { to_username: string; time_control: string; rated: boolean }) =>
    request<InviteWire>('/api/invites/send', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  acceptInvite: (id: string) => request<InviteWire>(`/api/invites/${id}/accept`, { method: 'POST' }),
  declineInvite: (id: string) => request<void>(`/api/invites/${id}/decline`, { method: 'POST' }),
  cancelInvite: (id: string) => request<void>(`/api/invites/${id}/cancel`, { method: 'POST' }),

  // ===== In-game actions (require game_id) =====
  // Each helper auto-routes to /api/temp/* when the gameId starts with
  // "temp-" so GameView is unaware of the storage tier.
  getState: (gameId: string) => request<StateJSON>(`${gameRoute(gameId, 'state')}?game_id=${gameId}`),
  move: (gameId: string, move: string) => request<StateJSON>(`${gameRoute(gameId, 'move')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ move })
  }),
  resign: (gameId: string) => request<StateJSON>(`${gameRoute(gameId, 'resign')}?game_id=${gameId}`, {
    method: 'POST',
  }),
  newGame: (gameId: string, engine_white: boolean, engine_black: boolean) => request<StateJSON>(`${gameRoute(gameId, 'new')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black })
  }),
  setPlayers: (gameId: string, engine_white: boolean, engine_black: boolean, white_think_time: number, black_think_time: number) => request<StateJSON>(`${gameRoute(gameId, 'set_players')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black, white_think_time, black_think_time })
  }),
  setPosition: (gameId: string, fen: string) => request<StateJSON>(`${gameRoute(gameId, 'set_position')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ fen })
  }),

  // PGN. Download is a plain GET that the SPA hands to the browser
  // (returns a file download) — we don't need to round-trip through
  // request(). loadPgn replays a pasted PGN; same engine-only rule
  // as setPosition.
  pgnDownloadUrl: (gameId: string) => `${API_BASE}${gameRoute(gameId, 'pgn')}?game_id=${gameId}`,
  loadPgn: (gameId: string, pgn: string) => request<StateJSON>(`${gameRoute(gameId, 'load_pgn')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ pgn })
  }),
  analyze: (gameId: string) => request<{ ok: boolean; plies: number }>(`${gameRoute(gameId, 'analyze')}?game_id=${gameId}`, {
    method: 'POST',
  }),
  undo: (gameId: string) => request<StateJSON>(`${gameRoute(gameId, 'undo')}?game_id=${gameId}`, { method: 'POST' }),
  getHint: (gameId: string, movetime: number) => request<HintResponse>(`${gameRoute(gameId, 'hint')}?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ movetime })
  }),

  // ===== Draw flow (PvP only) =====
  drawOffer: (gameId: string) => request<void>(`/api/draw_offer?game_id=${gameId}`, { method: 'POST' }),
  drawAccept: (gameId: string) => request<StateJSON>(`/api/draw_accept?game_id=${gameId}`, { method: 'POST' }),
  drawDecline: (gameId: string) => request<void>(`/api/draw_decline?game_id=${gameId}`, { method: 'POST' }),

  // ===== Takeback flow (PvP, casual only) =====
  takebackOffer: (gameId: string) => request<void>(`/api/takeback_offer?game_id=${gameId}`, { method: 'POST' }),
  takebackAccept: (gameId: string) => request<StateJSON>(`/api/takeback_accept?game_id=${gameId}`, { method: 'POST' }),
  takebackDecline: (gameId: string) => request<void>(`/api/takeback_decline?game_id=${gameId}`, { method: 'POST' }),

  // ===== Spectator (durable games only) =====
  // Owner-only toggle. Backend rejects with 404 if the caller isn't a
  // participant. When true, /watch/{id} works and any signed-in user can
  // /api/state the row.
  setVisibility: (gameId: string, is_public: boolean) =>
    request<{ is_public: boolean }>(`/api/visibility?game_id=${gameId}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ is_public }),
    }),

  // ===== Anonymous temp-game session =====
  // Idempotent: returns the caller's currently-active temp game (if
  // any, refreshing TTL) or creates a fresh one. The chess-anon cookie
  // is set by the gateway on first call.
  tempSession: () => request<StateJSON & { id?: string }>('/api/temp/session', {
    method: 'POST',
  }),

  ping: () => fetch(`${API_BASE}/api/ping`, { method: 'POST' }).catch(() => {}),
};

export function isTempGameId(id: string): boolean {
  return id.startsWith('temp-');
}

// gameRoute picks /api/temp/<verb> for temp games and /api/<verb> for
// durable games. Centralises the "which surface owns this game" decision
// so callers don't sprinkle isTempGameId checks everywhere.
function gameRoute(id: string, verb: 'state' | 'move' | 'resign' | 'new' | 'hint' | 'undo' | 'set_players' | 'set_position' | 'pgn' | 'load_pgn' | 'analyze'): string {
  return isTempGameId(id) ? `/api/temp/${verb}` : `/api/${verb}`;
}
