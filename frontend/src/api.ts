import { StateJSON, HintResponse, AssessResponse } from './types';

const API_BASE = (import.meta.env.VITE_API_URL as string) || '';

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const url = `${API_BASE}${path}`;
  const response = await fetch(url, options);
  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `Request failed with status ${response.status}`);
  }
  if (response.status === 204) return null as T;
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
  createGame: () => request<{ game_id: string }>('/api/games/new', { method: 'POST' }),
  listGames: () => request<any[]>('/api/games'),
  deleteGame: (gameId: string) => request<void>(`/api/games/delete?game_id=${gameId}`, { method: 'DELETE' }),

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
  
  loadGame: (gameId: string, gameData: any) => request<StateJSON>(`/api/load?game_id=${gameId}`, {
    method: 'POST',
    body: typeof gameData === 'string' ? gameData : JSON.stringify(gameData)
  }),
  
  ping: () => fetch(`${API_BASE}/api/ping`, { method: 'POST' }).catch(() => {}),

  getSaveUrl: (gameId: string) => `${API_BASE}/api/save?game_id=${gameId}`
};
