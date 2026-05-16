// U+FE0E (VS15) forces text presentation on glyphs that have an
// emoji-presentation variant. Without it, iOS Safari renders some of
// these as colorful emoji — most visibly the black pawn ♟ (U+265F),
// which standalone shows as the larger emoji glyph while the other
// pieces stay symbol-shaped. Applying VS15 to all of them keeps the
// rendering uniform across iOS, Android, and desktop browsers without
// shipping a webfont. Escape sequence (not literal char) so the
// invariant survives editors that normalize trailing combining marks.
const VS15 = '︎';
export const PIECE: Record<string, string> = {
  K: '♔' + VS15, Q: '♕' + VS15, R: '♖' + VS15,
  B: '♗' + VS15, N: '♘' + VS15, P: '♙' + VS15,
  k: '♚' + VS15, q: '♛' + VS15, r: '♜' + VS15,
  b: '♝' + VS15, n: '♞' + VS15, p: '♟' + VS15,
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
