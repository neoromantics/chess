export interface MoveJSON {
  from: string;
  to: string;
}

export interface StateJSON {
  fen: string;
  turn: 'w' | 'b';
  engine_white: boolean;
  engine_black: boolean;
  engine_to_move: boolean;
  status: string;
  in_check: boolean;
  legal_moves: string[];
  history: string[];
  history_san: string[];
  last_move: MoveJSON | null;
  thinking: boolean;
  touch_move: boolean;
  touched_square: string;
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

export interface AssessResponse {
  index: number;
  player: string;
  move: string;
  best_move: string;
  user_score: string;
  best_score: string;
  cp_loss: number;
  label: string;
  depth: number;
  pv?: string[];
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
