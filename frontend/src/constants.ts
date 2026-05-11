export const PIECE: Record<string, string> = {
  K: '♔', Q: '♕', R: '♖', B: '♗', N: '♘', P: '♙',
  k: '♚', q: '♛', r: '♜', b: '♝', n: '♞', p: '♟'
};

export const ASSESS_COLORS: Record<string, string> = {
  Brilliant: '#9bf6a6',
  Best: '#9fdcb5',
  Excellent: '#9fdcb5',
  Good: '#bcd0a3',
  Inaccuracy: '#e6c875',
  Mistake: '#e6a075',
  Blunder: '#ff7575',
};

export const ASSESS_SYMBOL: Record<string, string> = {
  Brilliant: '!!',
  Best: '',
  Excellent: '',
  Good: '',
  Inaccuracy: '?!',
  Mistake:    '?',
  Blunder:    '??',
};

export function parseBoard(fen: string): (string | null)[][] {
  const rows = fen.split(' ')[0].split('/');
  const grid: (string | null)[][] = [];
  for (const row of rows) {
    const arr: (string | null)[] = [];
    for (const ch of row) {
      if (ch >= '1' && ch <= '8') {
        for (let i = 0; i < parseInt(ch); i++) arr.push(null);
      } else {
        arr.push(ch);
      }
    }
    grid.push(arr);
  }
  return grid;
}
