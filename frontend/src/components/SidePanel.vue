<template>
  <div class="side-panel">
    <!-- Compact toolbar: 3 always-visible board/audio toggles. Setup
         moves into the actions row (it's a game action, not a board
         control). Replay moves to a bottom CTA next to New Game (it's
         a "what's next" action available only after the game ends). -->
    <div class="toolbar">
      <button class="tool" :class="{ on: soundEnabled }" @click="$emit('update:sound-enabled', !soundEnabled)" :title="soundEnabled ? 'Sound on (click to mute)' : 'Sound off (click to unmute)'">
        <span class="tool-icon" aria-hidden="true">{{ soundEnabled ? '🔊' : '🔇' }}</span>
        <span class="tool-label">{{ soundEnabled ? 'Sound' : 'Muted' }}</span>
      </button>
      <button class="tool" @click="$emit('toggle-flip')" title="Flip board (F)">
        <span class="tool-icon" aria-hidden="true">⇅</span>
        <span class="tool-label">Flip</span>
      </button>
      <button class="tool" :class="{ on: touchMove }" @click="$emit('update:touch-move', !touchMove)" :title="touchMove ? 'Touch-move ON: a touched piece must move (FIDE rule)' : 'Touch-move OFF (default)'">
        <span class="tool-icon" aria-hidden="true">☝</span>
        <span class="tool-label">{{ touchMove ? 'Touch' : 'Free' }}</span>
      </button>
    </div>

    <!-- Spectator banner: present whenever the caller isn't a
         participant. Hidden during normal play. -->
    <div v-if="spectator" class="prompt subtle">
      <span class="prompt-text">Spectating — read-only view.</span>
    </div>

    <!-- Visibility toggle: owner-only. When ON, anyone can read the
         game (signed in or anonymous via /watch/{id}). PvP games only
         really benefit from this; engine games can flip too. -->
    <div v-if="owner && state" class="visibility">
      <label class="vis-row">
        <input
          type="checkbox"
          :checked="state.is_public"
          @change="$emit('set-visibility', ($event.target as HTMLInputElement).checked)"
        />
        <span>Public — anyone can watch</span>
      </label>
      <button
        v-if="state.is_public && gameId"
        class="btn ghost vis-link"
        @click="copyWatchLink"
        title="Click to copy the spectator link"
      >Copy spectator link</button>
    </div>

    <!-- Draw / takeback prompts. Render before the action row so
         they're the most prominent prompt while waiting. -->
    <div v-if="!spectator && incomingDraw" class="prompt">
      <div class="prompt-text">Opponent offers a draw.</div>
      <div class="prompt-actions">
        <button class="btn primary" @click="$emit('draw-accept')">Accept</button>
        <button class="btn ghost" @click="$emit('draw-decline')">Decline</button>
      </div>
    </div>
    <div v-else-if="outgoingDraw" class="prompt subtle">Draw offer sent — waiting…</div>

    <div v-if="incomingTakeback" class="prompt">
      <div class="prompt-text">Opponent requests a takeback.</div>
      <div class="prompt-actions">
        <button class="btn primary" @click="$emit('takeback-accept')">Accept</button>
        <button class="btn ghost" @click="$emit('takeback-decline')">Decline</button>
      </div>
    </div>
    <div v-else-if="outgoingTakeback" class="prompt subtle">Takeback requested — waiting…</div>

    <!-- Primary actions. Visible only while the game is live AND the
         caller is a participant; once terminal, the bottom CTA row
         (New Game / Replay) takes over. Spectators see no buttons —
         they can read the board, move list, and clock, but every
         mutation surface (hint/undo/draw/resign/setup/takeback) is
         participant-only, both client-side here and server-side via
         userOwnsGame. -->
    <div v-if="!spectator && state?.status === 'ongoing'" class="actions">
      <button class="btn" :disabled="state?.thinking" @click="$emit('get-hint')">Hint</button>
      <button v-if="!isPvP" class="btn" :disabled="state?.thinking" @click="$emit('undo')">Undo</button>
      <button v-if="canEditPosition" class="btn" @click="$emit('edit-position')" title="Set up a custom position (engine games only)">Setup</button>
      <button v-if="canRequestTakeback" class="btn" :disabled="outgoingTakeback" @click="$emit('takeback-offer')">Takeback</button>
      <button v-if="canOfferDraw" class="btn" :disabled="outgoingDraw" @click="$emit('draw-offer')">Offer Draw</button>
      <button class="btn danger" @click="$emit('resign')">Resign</button>
    </div>

    <!-- Engine-settings panel. Hidden by default — most users land,
         play, leave; only revealed via the disclosure. Collapsed
         when the game is not engine-against-anything (PvP) since
         the toggles wouldn't do anything useful there. -->
    <details v-if="!spectator && !isPvP" class="settings" :open="settingsOpen">
      <summary @click.prevent="settingsOpen = !settingsOpen">Engine settings</summary>
      <div class="settings-body">
        <div class="setting-row">
          <span class="setting-label">White</span>
          <div class="seg">
            <button :class="{ active: whitePlayerType === 'h' }" @click="emit('update:white-player-type', 'h')">Human</button>
            <button :class="{ active: whitePlayerType === 'e' }" @click="emit('update:white-player-type', 'e')">Engine</button>
          </div>
        </div>
        <div v-if="whitePlayerType === 'e'" class="setting-row">
          <span class="setting-label">White think</span>
          <select :value="whiteThinkTime" @change="emitThinkTime('white', $event)">
            <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
          </select>
        </div>
        <div class="setting-row">
          <span class="setting-label">Black</span>
          <div class="seg">
            <button :class="{ active: blackPlayerType === 'h' }" @click="emit('update:black-player-type', 'h')">Human</button>
            <button :class="{ active: blackPlayerType === 'e' }" @click="emit('update:black-player-type', 'e')">Engine</button>
          </div>
        </div>
        <div v-if="blackPlayerType === 'e'" class="setting-row">
          <span class="setting-label">Black think</span>
          <select :value="blackThinkTime" @change="emitThinkTime('black', $event)">
            <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
          </select>
        </div>
      </div>
    </details>

    <!-- Hint info (last analysis result). Tucked under settings so
         it doesn't visually compete with action buttons. -->
    <div v-if="hintInfo" class="hint-info">{{ hintInfo }}</div>

    <!-- Move list. Larger than before — most of the panel's real
         estate goes here since it's what users actually read. -->
    <section class="moves">
      <header>
        <h3>Moves</h3>
        <span v-if="historyPairs.length" class="muted">{{ plyCount }} {{ plyCount === 1 ? 'ply' : 'plies' }}</span>
      </header>
      <div class="move-list">
        <div v-for="(pair, i) in historyPairs" :key="i" class="move-row">
          <span class="move-num">{{ i + 1 }}.</span>
          <span v-for="(mv, j) in pair" :key="j" class="move-cell">
            <span
              class="move-san"
              :class="moveClass(mv.idx)"
              :title="moveTooltip(mv)"
            >{{ mv.san }}{{ moveMarker(mv.idx) }}</span>
          </span>
        </div>
        <div v-if="!historyPairs.length" class="move-empty">No moves yet.</div>
      </div>
      <!-- Analyze button: visible whenever there's at least one ply.
           Disabled during analysis; shows partial progress. -->
      <div v-if="plyCount > 0" class="analyze-row">
        <button class="btn ghost analyze-btn" :disabled="analyzing" @click="$emit('analyze')">
          {{ analyzing ? `Analyzing… ${assessedCount}/${plyCount}` : (assessedCount > 0 ? 'Re-analyze' : 'Analyze game') }}
        </button>
      </div>
    </section>

    <!-- Save / Load. Lifted out of a disclosure so it's discoverable
         without hunting; the standard chess export format is PGN, and
         it's how engines and other sites round-trip games. Load is
         engine-only (same rule as set_position) since replacing the
         board mid-PvP would let one side undo their opponent's moves.
         Spectators can still Save (.pgn) — it's a read-only download. -->
    <section class="saveload">
      <header><h3>Save / Load</h3></header>
      <div class="saveload-actions">
        <a v-if="pgnDownloadUrl" :href="pgnDownloadUrl" :download="pgnFilename" class="btn ghost" title="Download this game as a .pgn file">Save (.pgn)</a>
        <button v-if="!spectator && canEditPosition" class="btn ghost" @click="onPickFile" title="Load a .pgn file from disk">Load file…</button>
        <button v-if="!spectator && canEditPosition" class="btn ghost" @click="pgnImportOpen = !pgnImportOpen" title="Paste a PGN as text">
          {{ pgnImportOpen ? 'Cancel paste' : 'Paste…' }}
        </button>
      </div>
      <input ref="pgnFileInputEl" type="file" accept=".pgn,application/x-chess-pgn,text/plain" style="display:none" @change="onFilePicked"/>
      <div v-if="!spectator && pgnImportOpen" class="pgn-import">
        <textarea v-model="pgnImportText" placeholder="Paste PGN here" rows="6"></textarea>
        <button class="btn primary" :disabled="!pgnImportText.trim()" @click="onLoadPgn">Apply</button>
      </div>
    </section>

    <!-- Bottom CTA row. "New Game" is the dominant next action;
         Replay joins it only when the game is finished. Spectators
         see neither — they can't start a game in someone else's slot,
         and Replay is reachable from /game/{id} (or a fresh tab) if
         they want it. -->
    <div v-if="!spectator" class="bottom-cta">
      <button class="btn new-game" @click="$emit('new-game')">New Game</button>
      <button v-if="state?.status && state.status !== 'ongoing'" class="btn ghost replay-cta" @click="$emit('open-replay')" title="Open frame-by-frame replay">▶ Replay</button>
    </div>

    <!-- FEN: hidden behind a disclosure. Power-user feature; most
         players don't care. -->
    <details class="fen-block">
      <summary>FEN</summary>
      <div class="fen">{{ state?.fen }}</div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { StateJSON } from '../types';

const props = defineProps<{
  state: StateJSON | null;
  // Durable game ID from the route. SidePanel uses it to build the
  // spectator link without depending on whether snapshot.id is set
  // (which only happens for temp games).
  gameId?: string;
  // Read-only spectator view: hides all action buttons and shows a
  // "Spectating" banner instead. GameView still passes the live state
  // so the board/clock/history all render normally.
  spectator?: boolean;
  // True iff caller is a participant (used to gate the visibility
  // toggle separately — only the game's owner sets is_public).
  owner?: boolean;
  whitePlayerType: string;
  blackPlayerType: string;
  whiteThinkTime: number;
  blackThinkTime: number;
  soundEnabled: boolean;
  touchMove?: boolean;
  hintInfo: string;
  historyPairs: any[];
  canOfferDraw?: boolean;
  incomingDraw?: boolean;
  outgoingDraw?: boolean;
  canRequestTakeback?: boolean;
  incomingTakeback?: boolean;
  outgoingTakeback?: boolean;
  canEditPosition?: boolean;
  pgnDownloadUrl?: string;
  assessments?: Record<number, { ply: number; played: string; best: string; score: number; depth: number; class: string }>;
  analyzing?: boolean;
}>();

const emit = defineEmits<{
  (e: 'update:white-player-type', val: string): void;
  (e: 'update:black-player-type', val: string): void;
  (e: 'update:white-think-time', val: number): void;
  (e: 'update:black-think-time', val: number): void;
  (e: 'update:sound-enabled', val: boolean): void;
  (e: 'update:touch-move', val: boolean): void;
  (e: 'new-game'): void;
  (e: 'get-hint'): void;
  (e: 'undo'): void;
  (e: 'open-replay'): void;
  (e: 'toggle-flip'): void;
  (e: 'resign'): void;
  (e: 'draw-offer'): void;
  (e: 'draw-accept'): void;
  (e: 'draw-decline'): void;
  (e: 'takeback-offer'): void;
  (e: 'takeback-accept'): void;
  (e: 'takeback-decline'): void;
  (e: 'edit-position'): void;
  (e: 'load-pgn', pgn: string): void;
  (e: 'analyze'): void;
  (e: 'set-visibility', isPublic: boolean): void;
}>();

const isPvP = computed(() => !!(props.state && props.state.white_user_id !== null && props.state.black_user_id !== null));
const plyCount = computed(() => props.state?.history?.length ?? 0);
const assessedCount = computed(() => Object.keys(props.assessments ?? {}).length);

const moveClass = (idx: number) => {
  const a = props.assessments?.[idx];
  if (!a) return '';
  return 'assess-' + a.class;
};
const moveMarker = (idx: number) => {
  const a = props.assessments?.[idx];
  if (!a) return '';
  if (a.class === 'best') return ' ✓';
  if (a.class === 'only') return ' ★';
  return ' ?';
};
const moveTooltip = (mv: { san: string; lan: string; idx: number }) => {
  const a = props.assessments?.[mv.idx];
  if (!a) return mv.lan;
  if (a.class === 'best') return `${mv.lan} — engine's pick (eval ${a.score / 100}, depth ${a.depth})`;
  if (a.class === 'only') return `${mv.lan} — only legal move`;
  return `${mv.lan} — engine preferred ${a.best} (eval ${a.score / 100}, depth ${a.depth})`;
};

// Engine settings collapsed by default. Stored in localStorage so a
// power user who likes seeing them keeps that across reloads.
const settingsOpen = ref(localStorage.getItem('chess-settings-open') === '1');
watch(settingsOpen, (v) => localStorage.setItem('chess-settings-open', v ? '1' : '0'));

const thinkOptions = [
  { v: 100, l: '0.1s (weak)' },
  { v: 300, l: '0.3s' },
  { v: 1000, l: '1s' },
  { v: 3000, l: '3s' },
  { v: 10000, l: '10s (strong)' }
];

const emitThinkTime = (side: 'white' | 'black', event: Event) => {
  const val = parseInt((event.target as HTMLSelectElement).value);
  if (side === 'white') emit('update:white-think-time', val);
  else emit('update:black-think-time', val);
};

const pgnImportOpen = ref(false);
const pgnImportText = ref('');
const onLoadPgn = () => {
  emit('load-pgn', pgnImportText.value);
  pgnImportText.value = '';
  pgnImportOpen.value = false;
};

// File picker for PGN load. The hidden <input type="file"> is clicked
// programmatically from the visible button so we get the standard OS
// file dialog without an ugly default button.
const pgnFileInputEl = ref<HTMLInputElement | null>(null);
const onPickFile = () => { pgnFileInputEl.value?.click(); };
const onFilePicked = async (e: Event) => {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  try {
    const text = await file.text();
    if (text.trim()) emit('load-pgn', text);
  } finally {
    // Reset so picking the same file twice in a row still fires change.
    input.value = '';
  }
};

// Download filename: dated + short ID so a folder of saved games is
// browsable. The href is the existing /api/pgn URL emitted by GameView.
const pgnFilename = computed(() => {
  const d = new Date();
  const yyyy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `chess-${yyyy}${mm}${dd}.pgn`;
});

const copyWatchLink = async () => {
  if (!props.gameId) return;
  const url = `${location.origin}/watch/${props.gameId}`;
  try {
    await navigator.clipboard.writeText(url);
  } catch {
    // Fallback: open the link so the user can copy it from the address bar.
    window.open(url, '_blank');
  }
};
</script>

<style scoped>
.side-panel { display: flex; flex-direction: column; gap: 14px; }

/* Top toolbar. Equal-width buttons (flex: 1 1 0) so a row of 3, 4, or
   5 always lines up cleanly; min-width:0 + ellipsized labels keep
   narrow viewports from overflowing. flex-wrap is the safety net. */
.toolbar { display: flex; gap: 6px; align-items: stretch; flex-wrap: wrap; }
.tool {
  flex: 1 1 0;
  min-width: 56px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  padding: 8px 6px;
  background: #232323;
  border: 1px solid #333;
  border-radius: 6px;
  color: #ccc;
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  font: inherit;
  transition: border-color 120ms ease, background-color 120ms ease;
}
.tool:hover { border-color: #4a6b8a; background: #2a2f36; }
.tool.on { color: #fff; border-color: #4a6b8a; background: rgba(74,107,138,0.15); }
.tool-icon { font-size: 14px; line-height: 1; flex: 0 0 auto; }
.tool-label { font-size: 12px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* Draw / takeback prompt cards */
.prompt {
  background: #2a3340;
  border-left: 3px solid #4a6b8a;
  padding: 12px 14px;
  border-radius: 6px;
  color: #e2e8ec;
  font-size: 13px;
}
.prompt.subtle {
  background: #262626;
  border-left-color: #555;
  color: #888;
  font-style: italic;
  padding: 10px 14px;
}
.prompt-text { margin-bottom: 8px; }
.prompt-actions { display: flex; gap: 8px; }

/* Visibility toggle row */
.visibility {
  background: #262b32;
  border-left: 3px solid #4a6b8a;
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.vis-row { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.vis-row input { margin: 0; }
.vis-link { align-self: flex-start; padding: 4px 10px; font-size: 12px; }

/* Primary action row */
.actions { display: flex; flex-wrap: wrap; gap: 6px; }
.btn {
  flex: 1 1 0;
  min-width: 80px;
  padding: 9px 12px;
  background: #2f2f2f;
  border: 1px solid #3a3a3a;
  border-radius: 6px;
  color: #e0e0e0;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font: inherit;
  transition: background-color 120ms ease, border-color 120ms ease;
}
.btn:hover:not(:disabled) { background: #383838; border-color: #4a4a4a; }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }
.btn.primary { background: #2d5a2d; border-color: #3a703a; color: #fff; font-weight: 600; }
.btn.primary:hover:not(:disabled) { background: #347a34; }
.btn.danger { background: #5a2d2d; border-color: #803535; color: #fff; }
.btn.danger:hover:not(:disabled) { background: #6a3535; }
.btn.ghost { background: transparent; border-color: #444; color: #ccc; }
.btn.ghost:hover:not(:disabled) { background: #2a2a2a; }

/* Engine settings disclosure */
.settings {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 6px;
  padding: 0;
}
.settings > summary {
  list-style: none;
  cursor: pointer;
  padding: 10px 14px;
  font-size: 12px;
  color: #aaa;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  user-select: none;
}
.settings > summary::after {
  content: '▸';
  float: right;
  transition: transform 120ms ease;
  color: #666;
}
.settings[open] > summary::after { transform: rotate(90deg); }
.settings > summary:hover { color: #ddd; }
.settings-body { padding: 4px 14px 14px; display: flex; flex-direction: column; gap: 10px; }
.setting-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; color: #ccc; }
.setting-label { color: #888; min-width: 90px; }
.seg { display: inline-flex; background: #1c1c1c; border: 1px solid #333; border-radius: 6px; overflow: hidden; }
.seg button { background: transparent; border: 0; color: #aaa; padding: 5px 12px; font-size: 12px; cursor: pointer; font: inherit; }
.seg button.active { background: #3a4754; color: #fff; }
.setting-row select { background: #1c1c1c; border: 1px solid #333; color: #ddd; padding: 5px 10px; border-radius: 4px; font-size: 12px; }

/* Hint info */
.hint-info { color: #9fdcb5; font-size: 12px; padding: 4px 4px; }

/* Move list */
.moves { background: #232323; border: 1px solid #2f2f2f; border-radius: 6px; padding: 10px 12px 6px; }
.moves header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 6px; }
.moves h3 { margin: 0; font-size: 12px; color: #aaa; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600; }
.moves .muted { font-size: 11px; color: #666; }
.move-list { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 320px; overflow-y: auto; padding-right: 4px; }
.move-row { display: flex; align-items: baseline; gap: 6px; padding: 2px 0; line-height: 1.5; }
.move-num { color: #666; min-width: 28px; font-size: 12px; }
.move-cell { min-width: 60px; }
.move-san { color: #e0e0e0; padding: 0 2px; border-radius: 2px; }
.move-empty { color: #666; font-style: italic; padding: 8px 0; font-family: inherit; }
.move-san.assess-best { color: #6ec77a; }
.move-san.assess-alt  { color: #e4b15a; }
.move-san.assess-only { color: #c2bfff; }

.analyze-row { padding: 8px 0 0; border-top: 1px solid #2f2f2f; margin-top: 8px; }
.analyze-btn { width: 100%; font-size: 12px; }

/* Save / Load card. Same visual weight as the move list — discoverable
   without being pushy. */
.saveload { background: #232323; border: 1px solid #2f2f2f; border-radius: 6px; padding: 10px 12px 12px; }
.saveload header { margin-bottom: 8px; }
.saveload h3 { margin: 0; font-size: 12px; color: #aaa; text-transform: uppercase; letter-spacing: 0.5px; font-weight: 600; }
.saveload-actions { display: flex; gap: 6px; flex-wrap: wrap; }
.saveload-actions .btn { flex: 1 1 auto; text-align: center; text-decoration: none; padding: 7px 10px; font-size: 12px; min-width: 0; }
.pgn-import { padding: 10px 0 0; display: flex; flex-direction: column; gap: 8px; }
.pgn-import textarea {
  width: 100%;
  box-sizing: border-box;
  background: #1c1c1c;
  border: 1px solid #333;
  color: #ddd;
  font-family: ui-monospace, Menlo, monospace;
  font-size: 11px;
  padding: 6px 8px;
  border-radius: 4px;
  resize: vertical;
}

/* Bottom CTA row: New Game is the dominant action; Replay sits beside
   it (smaller / ghost) only when the game is finished. */
.bottom-cta { display: flex; gap: 8px; align-items: stretch; }
.new-game {
  flex: 1 1 auto;
  background: #2d5a2d;
  border-color: #3a703a;
  color: #fff;
  font-weight: 700;
  padding: 12px;
  font-size: 14px;
}
.new-game:hover { background: #347a34; }
.replay-cta { flex: 0 0 auto; padding: 12px 16px; font-size: 13px; }

/* FEN disclosure */
.fen-block {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 6px;
}
.fen-block > summary {
  list-style: none;
  cursor: pointer;
  padding: 8px 12px;
  font-size: 11px;
  color: #888;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  user-select: none;
}
.fen-block > summary::after { content: '▸'; float: right; transition: transform 120ms ease; color: #666; }
.fen-block[open] > summary::after { transform: rotate(90deg); }
.fen { padding: 0 12px 12px; font-family: ui-monospace, Menlo, monospace; font-size: 11px; color: #888; word-break: break-all; }
</style>
