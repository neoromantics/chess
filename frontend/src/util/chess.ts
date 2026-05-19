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
 * One node in a study tree. Mirrors the backend's `studyTreeNode`
 * shape (cmd/game/studies.go). Root node has no `move`; descendants
 * carry the move that produced their position. Re-declared here
 * (rather than imported from types.ts) so this util module stays
 * self-contained and importable from the standalone Replay bundle.
 */
export interface StudyNode {
  move?: string;
  san?: string;
  comment?: string;
  children: StudyNode[];
}

/**
 * Build a linear-chain study tree from an ordered list of moves —
 * every node has at most one child. Used when capturing a played
 * line (game history or replay frames) as a save-as-study payload;
 * branching trees are a future feature, so v1 saves are always
 * single-line.
 *
 * Items without a `move` are skipped (defensive against partial
 * frames). `san` falls back to `move` so the saved tree always has
 * a human-readable label for the move-list rendering.
 */
export function linearTreeFromMoves(
  moves: { move?: string; san?: string }[],
): StudyNode {
  const root: StudyNode = { children: [] };
  let cur = root;
  for (const m of moves) {
    if (!m.move) continue;
    const child: StudyNode = { move: m.move, san: m.san || m.move, children: [] };
    cur.children.push(child);
    cur = child;
  }
  return root;
}

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
