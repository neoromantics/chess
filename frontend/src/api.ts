import { StateJSON, HintResponse, AssessResponse, InviteWire, UserSummary } from './types';

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
  // Auth
  signup: (username: string, password: string) => request<any>('/api/auth/signup', {
    method: 'POST',
    body: JSON.stringify({ username, password })
  }),
  login: (username: string, password: string) => request<any>('/api/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username, password })
  }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  getMe: () => request<any>('/api/user/me'),

  // Game Management
  createGame: (opts?: { white_think_time?: number; black_think_time?: number; engine_white?: boolean; engine_black?: boolean; }) =>
    request<{ game_id: string }>('/api/games/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(opts ?? {}),
    }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('/api/user/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
    }),
  getUserStats: (userId?: number) => request<{ games_played: number; wins: number; losses: number; draws: number; current_streak: number }>('/api/user/stats' + (userId ? `?user_id=${userId}` : '')),
  getUserProfile: (username: string) => request<any>(`/api/user/profile?username=${username}`),
  listGames: () => request<any[]>('/api/games'),
  deleteGame: (gameId: string) => request<void>(`/api/games/delete?game_id=${gameId}`, { method: 'DELETE' }),

  // === Matchmaking ===
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

  // === Invites ===
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

  // Game actions (require game_id)
  getState: (gameId: string) => request<StateJSON>(`/api/state?game_id=${gameId}`),
  
  move: (gameId: string, move: string) => request<StateJSON>(`/api/move?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ move })
  }),
  
  newGame: (gameId: string, engine_white: boolean, engine_black: boolean) => request<StateJSON>(`/api/new?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black })
  }),
  
  getHint: (gameId: string, movetime: number) => request<HintResponse>(`/api/hint?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ movetime })
  }),
  
  touch: (gameId: string, square: string) => request<StateJSON>(`/api/touch?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ square })
  }),
  
  setTouchMove: (gameId: string, enabled: boolean) => request<StateJSON>(`/api/touch_move?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ enabled })
  }),
  
  assess: (gameId: string, movetime: number, index?: number) => {
    const body: { movetime: number; index?: number } = { movetime };
    if (index !== undefined) body.index = index;
    return request<AssessResponse>(`/api/assess?game_id=${gameId}`, {
      method: 'POST',
      body: JSON.stringify(body)
    });
  },
  
  setPlayers: (gameId: string, engine_white: boolean, engine_black: boolean, white_think_time: number, black_think_time: number) => request<StateJSON>(`/api/set_players?game_id=${gameId}`, {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black, white_think_time, black_think_time })
  }),
  
  undo: (gameId: string) => request<StateJSON>(`/api/undo?game_id=${gameId}`, { method: 'POST' }),
  
  resign: (gameId: string) => request<StateJSON>(`/api/games/${gameId}/resign`, { method: 'POST' }),
  offerDraw: (gameId: string) => request<void>(`/api/games/${gameId}/offer-draw`, { method: 'POST' }),
  acceptDraw: (gameId: string) => request<StateJSON>(`/api/games/${gameId}/accept-draw`, { method: 'POST' }),
  declineDraw: (gameId: string) => request<void>(`/api/games/${gameId}/decline-draw`, { method: 'POST' }),

  offerTakeback: (gameId: string) => request<void>(`/api/games/${gameId}/offer-takeback`, { method: 'POST' }),
  acceptTakeback: (gameId: string) => request<StateJSON>(`/api/games/${gameId}/accept-takeback`, { method: 'POST' }),
  declineTakeback: (gameId: string) => request<void>(`/api/games/${gameId}/decline-takeback`, { method: 'POST' }),

  loadGame: (gameId: string, gameData: any) => request<StateJSON>(`/api/load?game_id=${gameId}`, {
    method: 'POST',
    body: typeof gameData === 'string' ? gameData : JSON.stringify(gameData)
  }),
  
  ping: () => fetch(`${API_BASE}/api/ping`, { method: 'POST' }).catch(() => {}),

  getSaveUrl: (gameId: string) => `${API_BASE}/api/save?game_id=${gameId}`
};
