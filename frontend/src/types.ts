export interface MoveJSON {
  from: string;
  to: string;
}

export interface StateJSON {
  // Set on temp-game snapshots; durable game snapshots leave it
  // undefined since the SPA already has the ID from the URL.
  id?: string;
  fen: string;
  turn: 'w' | 'b';
  engine_white: boolean;
  engine_black: boolean;
  engine_to_move: boolean;
  status: string;
  // '*' | '1-0' | '0-1' | '1/2-1/2'
  result: string;
  in_check: boolean;
  legal_moves: string[];
  history: string[];
  history_san: string[];
  last_move: MoveJSON | null;
  thinking: boolean;
  white_think_time: number;
  black_think_time: number;

  // Player metadata. null = that side is engine, not human. SPA uses
  // these to decide board orientation and label the players.
  white_user_id: number | null;
  black_user_id: number | null;
  time_control: string;
  rated: boolean;

  // Server-authoritative clock projection. clock_initial_ms === 0 means
  // this game has no clock (engine games, pre-clock games). Otherwise:
  //   - white_clock_ms / black_clock_ms are the bank values at
  //     clock_server_ms (mover's bank already had elapsed time deducted).
  //   - clock_mover is "w" | "b" while running, "" when paused/over.
  //   - SPA extrapolates locally between snapshots: for the mover side,
  //     subtract (Date.now() - received_at) from the bank each frame.
  white_clock_ms: number;
  black_clock_ms: number;
  clock_initial_ms: number;
  clock_inc_ms: number;
  clock_mover: '' | 'w' | 'b';
  clock_server_ms: number;
}

export interface HintMove {
  move: string;
  from: string;
  to: string;
  promo?: string;
  score: string;
  depth: number;
  pv: string[];
}

export interface HintResponse {
  hint?: HintMove;
  state: StateJSON;
}

export interface Square {
  name: string;
  r: number;
  f: number;
  dark: boolean;
  piece: { char: string; color: 'w' | 'b' } | null;
  classes: Record<string, boolean>;
}

export interface ReplayFrame {
  fen: string;
  san?: string;
  from?: string;
  to?: string;
}

// Phase 2: invites
export interface InviteWire {
  id: string;
  from_user_id: number;
  from_username?: string;
  to_user_id: number;
  to_username?: string;
  time_control: string;
  rated: boolean;
  status: 'pending' | 'accepted' | 'declined' | 'cancelled' | 'expired';
  game_id?: string | null;
  created_at: string;
  expires_at: string;
}

export interface UserSummary {
  id: number;
  username: string;
  display_name: string;
  country: string;
  rating: number;
}
