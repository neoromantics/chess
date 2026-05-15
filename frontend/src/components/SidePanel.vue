<template>
  <div class="side-panel">
    <section class="section">
      <h3>Players</h3>
      <div class="row">
        <label>White</label>
        <span class="seg">
          <label><input type="radio" name="white-side" value="h" :checked="whitePlayerType === 'h'" @change="emitPlayerType('white', $event)">Human</label>
          <label><input type="radio" name="white-side" value="e" :checked="whitePlayerType === 'e'" @change="emitPlayerType('white', $event)">Engine</label>
        </span>
      </div>
      <div class="row" v-if="whitePlayerType === 'e'"><label>White think</label>
        <select :value="whiteThinkTime" @change="emitThinkTime('white', $event)">
          <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
        </select>
      </div>
      <div class="row">
        <label>Black</label>
        <span class="seg">
          <label><input type="radio" name="black-side" value="h" :checked="blackPlayerType === 'h'" @change="emitPlayerType('black', $event)">Human</label>
          <label><input type="radio" name="black-side" value="e" :checked="blackPlayerType === 'e'" @change="emitPlayerType('black', $event)">Engine</label>
        </span>
      </div>
      <div class="row" v-if="blackPlayerType === 'e'"><label>Black think</label>
        <select :value="blackThinkTime" @change="emitThinkTime('black', $event)">
          <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
        </select>
      </div>
      <div class="btn-row">
        <button @click="$emit('new-game')" style="flex: 2; background: #2d5a2d; border-color: #3a703a; font-weight: 600;">New Game</button>
      </div>
    </section>

    <section class="section">
      <h3>Options</h3>
      <div class="row" title="Play a click when a move is made."><label>Sound</label>
        <input type="checkbox" :checked="soundEnabled" @change="$emit('update:sound-enabled', ($event.target as HTMLInputElement).checked)">
      </div>
    </section>

    <section class="section">
      <h3>Analysis</h3>
      <div class="btn-row">
        <button @click="$emit('get-hint')" class="btn-analysis" :disabled="state?.thinking || state?.status !== 'ongoing'">Hint</button>
        <!-- Undo is unilateral; only safe to expose in engine games.
             PvP uses the Takeback flow (button moves to the Game
             section so it sits next to Resign / Offer Draw). -->
        <button v-if="!isPvP" @click="$emit('undo')" class="btn-analysis" :disabled="state?.thinking">Undo</button>
      </div>
      <div class="info-line hint-info">{{ hintInfo }}</div>
    </section>

    <section class="section">
      <h3>Game</h3>
      <!-- Incoming draw offer from the opponent. Render before the
           action row so it's the most prominent prompt while waiting. -->
      <div v-if="incomingDraw" class="draw-prompt">
        <span>Opponent offers a draw.</span>
        <div class="btn-row tight">
          <button class="btn-accept" @click="$emit('draw-accept')">Accept</button>
          <button class="btn-secondary" @click="$emit('draw-decline')">Decline</button>
        </div>
      </div>
      <div v-else-if="outgoingDraw" class="draw-prompt subtle">
        <span>Draw offer sent — waiting…</span>
      </div>
      <div v-if="incomingTakeback" class="draw-prompt">
        <span>Opponent requests a takeback.</span>
        <div class="btn-row tight">
          <button class="btn-accept" @click="$emit('takeback-accept')">Accept</button>
          <button class="btn-secondary" @click="$emit('takeback-decline')">Decline</button>
        </div>
      </div>
      <div v-else-if="outgoingTakeback" class="draw-prompt subtle">
        <span>Takeback requested — waiting…</span>
      </div>
      <div class="btn-row" v-if="state?.status === 'ongoing'">
        <button @click="$emit('resign')" class="btn-danger">Resign</button>
        <button v-if="canOfferDraw" @click="$emit('draw-offer')" class="btn-secondary" :disabled="outgoingDraw">Offer Draw</button>
        <button v-if="canRequestTakeback" @click="$emit('takeback-offer')" class="btn-secondary" :disabled="outgoingTakeback">Takeback</button>
      </div>
      <div class="btn-row">
        <button @click="$emit('new-game')" style="flex: 2; background: #2d5a2d; border-color: #3a703a; font-weight: 600;">New Game</button>
      </div>
    </section>

    <section class="section">
      <h3>View</h3>
      <div class="btn-row">
        <button @click="$emit('open-replay')" class="btn-view">Replay</button>
        <button @click="$emit('toggle-flip')" class="btn-view" title="Flip board (F)">Flip</button>
      </div>
    </section>

    <section class="section">
      <h3>Moves</h3>
      <div id="history">
        <div v-for="(pair, i) in historyPairs" :key="i">
          {{ i + 1 }}.
          <span v-for="(mv, j) in pair" :key="j">
            <span class="move-span" :title="mv.lan">{{ mv.san }}</span>
            <span v-if="j === 0 && pair.length > 1">&nbsp;&nbsp;</span>
          </span>
        </div>
      </div>
    </section>

    <section class="section">
      <h3>FEN</h3>
      <div id="fen">{{ state?.fen }}</div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { StateJSON } from '../types';

const props = defineProps<{
  state: StateJSON | null;
  whitePlayerType: string;
  blackPlayerType: string;
  whiteThinkTime: number;
  blackThinkTime: number;
  soundEnabled: boolean;
  hintInfo: string;
  historyPairs: any[];
  // Draw-offer banner state. canOfferDraw=false hides the "Offer Draw"
  // button (engine games, finished games). incomingDraw=true shows the
  // accept/decline prompt; outgoingDraw=true means we already sent one
  // and are waiting for the opponent.
  canOfferDraw?: boolean;
  incomingDraw?: boolean;
  outgoingDraw?: boolean;
  canRequestTakeback?: boolean;
  incomingTakeback?: boolean;
  outgoingTakeback?: boolean;
}>();

const emit = defineEmits<{
  (e: 'update:white-player-type', val: string): void;
  (e: 'update:black-player-type', val: string): void;
  (e: 'update:white-think-time', val: number): void;
  (e: 'update:black-think-time', val: number): void;
  (e: 'update:sound-enabled', val: boolean): void;
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
}>();

const isPvP = computed(() => !!(props.state && props.state.white_user_id !== null && props.state.black_user_id !== null));

const thinkOptions = [
  { v: 100, l: '0.1s (weak)' },
  { v: 300, l: '0.3s' },
  { v: 1000, l: '1s' },
  { v: 3000, l: '3s' },
  { v: 10000, l: '10s (strong)' }
];

const emitPlayerType = (side: 'white' | 'black', event: Event) => {
  const val = (event.target as HTMLInputElement).value;
  if (side === 'white') emit('update:white-player-type', val);
  else emit('update:black-player-type', val);
};

const emitThinkTime = (side: 'white' | 'black', event: Event) => {
  const val = parseInt((event.target as HTMLSelectElement).value);
  if (side === 'white') emit('update:white-think-time', val);
  else emit('update:black-think-time', val);
};
</script>

<style scoped>
#history { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 280px; overflow-y: auto; background: #2b2b2b; padding: 8px 10px; border-radius: 3px; }
#history div { line-height: 1.5; }
#fen { font-family: ui-monospace, Menlo, monospace; font-size: 11px; word-break: break-all; color: #888; background: #2b2b2b; padding: 6px 8px; border-radius: 3px; }

.move-span { padding: 0 3px; border-radius: 2px; }
.hint-info { color: #9fdcb5; }

.draw-prompt { background: #2a2f3a; border-left: 3px solid #4a6b8a; padding: 10px 12px; border-radius: 4px; margin-bottom: 10px; font-size: 13px; color: #d8dde6; }
.draw-prompt.subtle { background: #2b2b2b; border-left-color: #555; color: #999; font-style: italic; }
.draw-prompt .btn-row.tight { margin-top: 8px; gap: 8px; }
.btn-accept { background: #2d5a2d; border-color: #3a703a; color: #fff; font-weight: 600; }
.btn-accept:hover { background: #347a34; }
.btn-secondary { background: #3a3a3a; border-color: #4a4a4a; color: #ddd; }
.btn-secondary:hover { background: #444; }
.btn-secondary:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
