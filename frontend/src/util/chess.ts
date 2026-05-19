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
 * Walk a study tree's main chain (follow `children[0]` at each level)
 * and return the list of nodes traversed, excluding the root. The
 * root has no move; descendants carry the move that produced their
 * position, so the returned slice is one-to-one with the played plies
 * on the main line.
 *
 * v1 saves are always linear (single child everywhere) so the main
 * chain IS the whole tree. When branching ships the convention will
 * need a tie-break (longest branch? user-marked main?); for now
 * first-child wins.
 */
export function mainChainOf(tree: StudyNode | null | undefined): StudyNode[] {
  const out: StudyNode[] = [];
  let node: StudyNode | undefined = tree ?? undefined;
  while (node && node.children && node.children.length > 0) {
    node = node.children[0];
    out.push(node);
  }
  return out;
}

/**
 * Count of plies along the main chain — cheaper than `mainChainOf` if
 * the caller only needs the number (no node allocations, no list).
 */
export function plyCountOf(tree: StudyNode | null | undefined): number {
  let n = 0;
  let node: StudyNode | undefined = tree ?? undefined;
  while (node && node.children && node.children.length > 0) {
    n++;
    node = node.children[0];
  }
  return n;
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
 * Length of the longest shared prefix between two ordered sequences.
 * Used to find where two study main-chains diverge — pass each tree
 * through `mainChainOf` then map to `.move` strings.
 */
export function commonPrefixLength<T>(a: readonly T[], b: readonly T[]): number {
  let n = 0;
  const max = Math.min(a.length, b.length);
  while (n < max && a[n] === b[n]) n++;
  return n;
}

/**
 * Convert a 1-indexed half-move count (ply) to its move number for
 * display. Ply 1 → move 1 (white), ply 2 → move 1 (black), ply 3 →
 * move 2 (white), etc. Returns 0 for ply ≤ 0.
 */
export function moveNumberOfPly(ply: number): number {
  return ply <= 0 ? 0 : Math.ceil(ply / 2);
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
