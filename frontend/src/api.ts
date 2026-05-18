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
  getUserStats: () => request<{ games_played: number; wins: number; losses: number; draws: number; current_streak: number }>('/api/user/stats'),
  getUserProfile: (username: string) => request<any>(`/api/user/profile?username=${username}`),

  // ===== Game management =====
  createGame: (opts?: { white_think_time?: number; black_think_time?: number; engine_white?: boolean; engine_black?: boolean; }) =>
    request<{ game_id: string }>('/api/games/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(opts ?? {}),
    }),
  listGames: () => request<any[]>('/api/games'),
  deleteGame: (gameId: string) => request<void>(`/api/games/${encodeURIComponent(gameId)}`, { method: 'DELETE' }),

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

  // ===== In-game actions (require game_id in the path) =====
  // gameRoute() auto-namespaces to /api/temp/{id}/<verb> for temp games
  // (id prefix "temp-") and /api/games/{id}/<verb> for durable games,
  // so callers stay storage-tier-agnostic.
  getState: (gameId: string) => request<StateJSON>(gameRoute(gameId, 'state')),
  // ReplayFrame[] — index 0 is the starting position; index k is the
  // position after the k-th move was played. Used by Replay.vue and by
  // the move-list scrub in GameView.vue (finished games only).
  getReplayFrames: (gameId: string) => request<import('./types').ReplayFrame[]>(gameRoute(gameId, 'replay')),
  move: (gameId: string, move: string) => request<StateJSON>(gameRoute(gameId, 'move'), {
    method: 'POST',
    body: JSON.stringify({ move })
  }),
  resign: (gameId: string) => request<StateJSON>(gameRoute(gameId, 'resign'), {
    method: 'POST',
  }),
  newGame: (gameId: string, engine_white: boolean, engine_black: boolean) => request<StateJSON>(gameRoute(gameId, 'new'), {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black })
  }),
  setPlayers: (gameId: string, engine_white: boolean, engine_black: boolean, white_think_time: number, black_think_time: number) => request<StateJSON>(gameRoute(gameId, 'set_players'), {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black, white_think_time, black_think_time })
  }),
  setPosition: (gameId: string, fen: string) => request<StateJSON>(gameRoute(gameId, 'set_position'), {
    method: 'POST',
    body: JSON.stringify({ fen })
  }),

  // PGN. Download is a plain GET that the SPA hands to the browser
  // (returns a file download) — we don't need to round-trip through
  // request(). loadPgn replays a pasted PGN; same engine-only rule
  // as setPosition.
  pgnDownloadUrl: (gameId: string) => `${API_BASE}${gameRoute(gameId, 'pgn')}`,
  loadPgn: (gameId: string, pgn: string) => request<StateJSON>(gameRoute(gameId, 'load_pgn'), {
    method: 'POST',
    body: JSON.stringify({ pgn })
  }),
  analyze: (gameId: string) => request<{ ok: boolean; plies: number }>(gameRoute(gameId, 'analyze'), {
    method: 'POST',
  }),
  undo: (gameId: string) => request<StateJSON>(gameRoute(gameId, 'undo'), { method: 'POST' }),
  getHint: (gameId: string, movetime: number) => request<HintResponse>(gameRoute(gameId, 'hint'), {
    method: 'POST',
    body: JSON.stringify({ movetime })
  }),

  // ===== Draw flow (PvP only — temp games have no second player) =====
  drawOffer: (gameId: string) => request<void>(`/api/games/${gameId}/draw_offer`, { method: 'POST' }),
  drawAccept: (gameId: string) => request<StateJSON>(`/api/games/${gameId}/draw_accept`, { method: 'POST' }),
  drawDecline: (gameId: string) => request<void>(`/api/games/${gameId}/draw_decline`, { method: 'POST' }),

  // ===== Takeback flow (PvP, casual only) =====
  takebackOffer: (gameId: string) => request<void>(`/api/games/${gameId}/takeback_offer`, { method: 'POST' }),
  takebackAccept: (gameId: string) => request<StateJSON>(`/api/games/${gameId}/takeback_accept`, { method: 'POST' }),
  takebackDecline: (gameId: string) => request<void>(`/api/games/${gameId}/takeback_decline`, { method: 'POST' }),

  // ===== Rematch flow (finished PvP / bot games only) =====
  // Accept returns the new game's StateJSON so the accepter can
  // router.push immediately without a second /api/state round-trip.
  rematchOffer: (gameId: string) => request<void>(`/api/games/${gameId}/rematch_offer`, { method: 'POST' }),
  rematchAccept: (gameId: string) => request<{ game_id: string }>(`/api/games/${gameId}/rematch_accept`, { method: 'POST' }),
  rematchDecline: (gameId: string) => request<void>(`/api/games/${gameId}/rematch_decline`, { method: 'POST' }),

  // ===== Admin (is_admin gated; non-admins get 404) =====
  adminOverview: () => request<{
    users: number;
    bots: number;
    signups_24h: number;
    signups_7d: number;
    active_games: number;
    queue_depth: Record<string, number>;
  }>('/api/admin/overview'),
  adminBots: () => request<Array<{
    id: number;
    username: string;
    rating: number;
    games_played: number;
  }>>('/api/admin/bots'),
  adminSignups: () => request<Array<{
    id: number;
    username: string;
    display_name: string;
    country: string;
    rating: number;
    created_at: string;
  }>>('/api/admin/signups'),
  adminActions: (beforeIso?: string) => request<Array<{
    id: number;
    actor_user_id?: number;
    actor_username: string;
    action: string;
    target_user_id?: number;
    target_username: string;
    detail: string;
    created_at: string;
  }>>('/api/admin/actions' + (beforeIso ? `?before=${encodeURIComponent(beforeIso)}` : '')),
  adminLiveGames: () => request<Array<{
    id: string;
    white_user_id?: number;
    black_user_id?: number;
    white_username: string;
    black_username: string;
    time_control: string;
    rated: boolean;
    created_at: string;
    updated_at: string;
    viewer_count: number;
  }>>('/api/admin/live_games'),
  // Hard-delete. Body has to carry confirm_username matching the
  // target exactly — gateway 400s on mismatch as a typo guard.
  adminDeleteUser: (id: number, confirmUsername: string) =>
    request<void>(`/api/admin/users/${id}`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ confirm_username: confirmUsername }),
    }),

  // ===== Spectator (durable games only) =====
  // Owner-only toggle. Backend rejects with 404 if the caller isn't a
  // participant. When true, /watch/{id} works and any signed-in user can
  // /api/games/{id}/state the row.
  setVisibility: (gameId: string, is_public: boolean) =>
    request<{ is_public: boolean }>(`/api/games/${gameId}/visibility`, {
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

// gameRoute returns the RESTful per-game endpoint for the given verb,
// auto-namespaced by storage tier: /api/temp/{id}/<verb> for temp games
// (id prefix "temp-") and /api/games/{id}/<verb> for durable games.
// Centralises the "which surface owns this game" decision so callers
// don't sprinkle isTempGameId checks everywhere.
function gameRoute(id: string, verb: 'state' | 'move' | 'resign' | 'new' | 'hint' | 'undo' | 'set_players' | 'set_position' | 'pgn' | 'load_pgn' | 'analyze'): string {
  const base = isTempGameId(id) ? '/api/temp' : '/api/games';
  return `${base}/${encodeURIComponent(id)}/${verb}`;
}
