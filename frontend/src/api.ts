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
  getState: () => request<StateJSON>('/api/state'),
  
  move: (move: string) => request<StateJSON>('/api/move', {
    method: 'POST',
    body: JSON.stringify({ move })
  }),
  
  newGame: (engine_white: boolean, engine_black: boolean) => request<StateJSON>('/api/new', {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black })
  }),
  
  engineStep: (movetime: number) => request<StateJSON>('/api/engine_step', {
    method: 'POST',
    body: JSON.stringify({ movetime })
  }),
  
  getHint: (movetime: number) => request<HintResponse>('/api/hint', {
    method: 'POST',
    body: JSON.stringify({ movetime })
  }),
  
  touch: (square: string) => request<StateJSON>('/api/touch', {
    method: 'POST',
    body: JSON.stringify({ square })
  }),
  
  setTouchMove: (enabled: boolean) => request<StateJSON>('/api/touch_move', {
    method: 'POST',
    body: JSON.stringify({ enabled })
  }),
  
  assess: (movetime: number, index?: number) => {
    const body: { movetime: number; index?: number } = { movetime };
    if (index !== undefined) body.index = index;
    return request<AssessResponse>('/api/assess', {
      method: 'POST',
      body: JSON.stringify(body)
    });
  },
  
  setPlayers: (engine_white: boolean, engine_black: boolean) => request<StateJSON>('/api/set_players', {
    method: 'POST',
    body: JSON.stringify({ engine_white, engine_black })
  }),
  
  undo: () => request<StateJSON>('/api/undo', { method: 'POST' }),
  
  loadGame: (gameData: any) => request<StateJSON>('/api/load', {
    method: 'POST',
    body: typeof gameData === 'string' ? gameData : JSON.stringify(gameData)
  }),
  
  ping: () => fetch(`${API_BASE}/api/ping`, { method: 'POST' }).catch(() => {}),

  getSaveUrl: () => `${API_BASE}/api/save`
};
