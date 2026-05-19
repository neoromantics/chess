// Chess-shaped utilities shared across views. PGN building, tree walks,
// position constants — anything multiple components would otherwise
// inline copies of. Keep this module dependency-free (no Pinia, no Vue)
// so the standalone Replay bundle can import it too without dragging in
// the main-bundle world.

/**
 * Standard chess starting position. Used as the comparison anchor when
 * deciding whether a PGN needs explicit [SetUp]/[FEN] headers — we omit
 * them for standard-start games so the output stays clean.
 */
export const STANDARD_START_FEN =
  'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1';

/**
 * Build a PGN fragment from a starting position + a sequence of SAN
 * moves. Used to round-trip a game's prefix through load_pgn so the
 * new game's move list inherits both position AND history (used by
 * fork-from-ply in GameView and Play-from-here in StudyView).
 *
 * [SetUp][FEN] headers are emitted only for non-standard starts; the
 * output always terminates with `*` so the PGN parser sees a complete
 * game. Empty `sanMoves` produces a position-only PGN.
 */
export function buildPGNFromMoves(startFen: string, sanMoves: string[]): string {
  let pgn = '';
  if (startFen && startFen !== STANDARD_START_FEN) {
    pgn += `[SetUp "1"]\n[FEN "${startFen}"]\n\n`;
  }
  for (let i = 0; i < sanMoves.length; i++) {
    if (i % 2 === 0) pgn += `${Math.floor(i / 2) + 1}. `;
    pgn += `${sanMoves[i]} `;
  }
  pgn += '*';
  return pgn.trim();
}
