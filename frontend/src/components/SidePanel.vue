<template>
  <div class="side-panel">
    <!-- Compact toolbar: board / audio controls that aren't tied to
         game state. Always present so the affordances don't move. -->
    <div class="toolbar">
      <button class="tool" :class="{ on: soundEnabled }" @click="$emit('update:sound-enabled', !soundEnabled)" :title="soundEnabled ? 'Sound on (click to mute)' : 'Sound off (click to unmute)'">
        <span aria-hidden="true">{{ soundEnabled ? '🔊' : '🔇' }}</span>
        <span class="tool-label">{{ soundEnabled ? 'Sound on' : 'Muted' }}</span>
      </button>
      <button class="tool" @click="$emit('toggle-flip')" title="Flip board (F)">
        <span aria-hidden="true">⇅</span>
        <span class="tool-label">Flip</span>
      </button>
      <button v-if="state?.status !== 'ongoing'" class="tool" @click="$emit('open-replay')" title="Open replay viewer">
        <span aria-hidden="true">▶</span>
        <span class="tool-label">Replay</span>
      </button>
      <button v-if="canEditPosition" class="tool" @click="$emit('edit-position')" title="Set up a custom position (engine games only)">
        <span aria-hidden="true">✎</span>
        <span class="tool-label">Setup</span>
      </button>
      <button class="tool" :class="{ on: touchMove }" @click="$emit('update:touch-move', !touchMove)" :title="touchMove ? 'Touch-move ON: a touched piece must move (FIDE rule)' : 'Touch-move OFF (default)'">
        <span aria-hidden="true">☝</span>
        <span class="tool-label">{{ touchMove ? 'Touch-move' : 'Free' }}</span>
      </button>
    </div>

    <!-- Draw / takeback prompts. Render before the action row so
         they're the most prominent prompt while waiting. -->
    <div v-if="incomingDraw" class="prompt">
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

    <!-- Primary actions. Visible only while the game is live; once
         terminal, a single "New Game" CTA replaces the row. -->
    <div v-if="state?.status === 'ongoing'" class="actions">
      <button class="btn" :disabled="state?.thinking" @click="$emit('get-hint')">Hint</button>
      <button v-if="!isPvP" class="btn" :disabled="state?.thinking" @click="$emit('undo')">Undo</button>
      <button v-if="canRequestTakeback" class="btn" :disabled="outgoingTakeback" @click="$emit('takeback-offer')">Takeback</button>
      <button v-if="canOfferDraw" class="btn" :disabled="outgoingDraw" @click="$emit('draw-offer')">Offer Draw</button>
      <button class="btn danger" @click="$emit('resign')">Resign</button>
    </div>

    <!-- Engine-settings panel. Hidden by default — most users land,
         play, leave; only revealed via the disclosure. Collapsed
         when the game is not engine-against-anything (PvP) since
         the toggles wouldn't do anything useful there. -->
    <details v-if="!isPvP" class="settings" :open="settingsOpen">
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

    <!-- Bottom CTA: New Game. Always visible — it's the most common
         next action after a game ends, and a useful reset escape
         hatch during play. -->
    <button class="btn new-game" @click="$emit('new-game')">New Game</button>

    <!-- FEN: hidden behind a disclosure. Power-user feature; most
         players don't care. -->
    <details class="fen-block">
      <summary>FEN</summary>
      <div class="fen">{{ state?.fen }}</div>
    </details>

    <!-- PGN export + import. Behind a disclosure since most players
         don't care; load is engine-only (same rule as set_position). -->
    <details class="fen-block">
      <summary>PGN</summary>
      <div class="pgn-actions">
        <a v-if="pgnDownloadUrl" :href="pgnDownloadUrl" download class="btn ghost">Download</a>
        <button v-if="canEditPosition" class="btn ghost" @click="pgnImportOpen = !pgnImportOpen">
          {{ pgnImportOpen ? 'Cancel' : 'Load…' }}
        </button>
      </div>
      <div v-if="pgnImportOpen" class="pgn-import">
        <textarea v-model="pgnImportText" placeholder="Paste PGN here" rows="6"></textarea>
        <button class="btn primary" :disabled="!pgnImportText.trim()" @click="onLoadPgn">Apply</button>
      </div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { StateJSON } from '../types';

const props = defineProps<{
  state: StateJSON | null;
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
</script>

<style scoped>
.side-panel { display: flex; flex-direction: column; gap: 14px; }

/* Top toolbar */
.toolbar { display: flex; gap: 6px; align-items: stretch; }
.tool {
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 9px 10px;
  background: #232323;
  border: 1px solid #333;
  border-radius: 6px;
  color: #ccc;
  font-size: 13px;
  cursor: pointer;
  font: inherit;
  transition: border-color 120ms ease, background-color 120ms ease;
}
.tool:hover { border-color: #4a6b8a; background: #2a2f36; }
.tool.on { color: #fff; border-color: #4a6b8a; background: rgba(74,107,138,0.15); }
.tool-label { font-size: 12px; }

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

/* New game CTA */
.new-game {
  flex: none;
  background: #2d5a2d;
  border-color: #3a703a;
  color: #fff;
  font-weight: 700;
  padding: 12px;
  font-size: 14px;
}
.new-game:hover { background: #347a34; }

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

.pgn-actions { display: flex; gap: 6px; padding: 0 12px 8px; }
.pgn-actions .btn { flex: 1 1 auto; text-align: center; text-decoration: none; padding: 6px 10px; font-size: 12px; }
.pgn-import { padding: 0 12px 12px; display: flex; flex-direction: column; gap: 8px; }
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
</style>
