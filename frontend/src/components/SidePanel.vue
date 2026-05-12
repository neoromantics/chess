<template>
  <div class="side-panel">
    <section class="section hide-on-edit">
      <h3>Players</h3>
      <div class="row">
        <label>White</label>
        <span class="clock">{{ whiteTimeDisplay }}</span>
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
        <span class="clock">{{ blackTimeDisplay }}</span>
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

    <section class="section hide-on-edit">
      <h3>Options</h3>
      <div class="row" title="Tournament rule: clicking a piece commits you to moving it."><label>Touch-move</label>
        <input type="checkbox" :checked="touchMoveEnabled" @change="$emit('update:touch-move', ($event.target as HTMLInputElement).checked)">
      </div>
      <div class="row" title="After each move you make, the engine evaluates how good it was."><label>Assess my moves</label>
        <input type="checkbox" :checked="autoAssess" @change="$emit('update:auto-assess', ($event.target as HTMLInputElement).checked)">
      </div>
      <div class="row" title="Play a click when a move is made."><label>Sound</label>
        <input type="checkbox" :checked="soundEnabled" @change="$emit('update:sound-enabled', ($event.target as HTMLInputElement).checked)">
      </div>
    </section>

    <section class="section hide-on-edit">
      <h3>Analysis</h3>
      <div class="btn-row">
        <button @click="$emit('get-hint')" class="btn-analysis" :disabled="state?.thinking || state?.status !== 'ongoing'">Hint</button>
        <button @click="$emit('run-assess')" class="btn-analysis" :disabled="state?.thinking">Assess</button>
        <button @click="$emit('undo')" class="btn-analysis" :disabled="state?.thinking">Undo</button>
      </div>
      <div class="info-line hint-info">{{ hintInfo }}</div>
      <div class="info-line assess-info" :style="{ color: assessColor }">{{ assessInfo }}</div>
    </section>

    <section class="section hide-on-edit">
      <h3>View</h3>
      <div class="btn-row">
        <button @click="$emit('open-replay')" class="btn-view">Replay</button>
        <button @click="$emit('toggle-flip')" class="btn-view" title="Flip board (F)">Flip</button>
      </div>
    </section>

    <section class="section hide-on-edit">
      <h3>File</h3>
      <div class="btn-row">
        <button @click="$emit('save-game')" class="btn-file">Save Game</button>
        <button @click="loadFile" class="btn-file">Load Game</button>
        <input type="file" ref="fileInput" @change="$emit('load-game', $event)" accept="application/json,.json" style="display:none">
      </div>
    </section>

    <section class="section">
      <h3>Position</h3>
      <div class="btn-row">
        <button @click="$emit('toggle-edit')" class="btn-edit">{{ editMode ? 'Editing…' : 'Edit Position' }}</button>
      </div>
      <div class="btn-row" v-if="!editMode">
        <input type="text" :value="fenInput" @input="$emit('update:fen-input', ($event.target as HTMLInputElement).value)" placeholder="paste FEN…" @keyup.enter="$emit('load-fen')">
        <button @click="$emit('load-fen')" class="btn-edit">Load</button>
      </div>
    </section>

    <section class="section hide-on-edit">
      <h3>Moves</h3>
      <div id="history">
        <div v-for="(pair, i) in historyPairs" :key="i">
          {{ i + 1 }}. 
          <span v-for="(mv, j) in pair" :key="j">
            <span class="move-span" 
                  @click="$emit('run-assess', mv.idx, true)" 
                  :title="mv.lan + ' (click to assess)'">
              {{ mv.san }}<span v-if="mv.assess" :style="{ color: ASSESS_COLORS[mv.assess.label], fontWeight: 'bold', marginLeft: '1px' }">{{ ASSESS_SYMBOL[mv.assess.label] }}</span>
            </span>
            <span v-if="j === 0 && pair.length > 1">&nbsp;&nbsp;</span>
          </span>
        </div>
      </div>
    </section>

    <section class="section hide-on-edit">
      <h3>FEN</h3>
      <div id="fen">{{ state?.fen }}</div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { ASSESS_COLORS, ASSESS_SYMBOL } from '../constants';
import { StateJSON } from '../types';

const props = defineProps<{
  state: StateJSON | null;
  editMode: boolean;
  whitePlayerType: string;
  blackPlayerType: string;
  whiteThinkTime: number;
  blackThinkTime: number;
  whiteTimeDisplay: string;
  blackTimeDisplay: string;
  touchMoveEnabled: boolean;
  autoAssess: boolean;
  soundEnabled: boolean;
  hintInfo: string;
  assessInfo: string;
  assessColor: string;
  historyPairs: any[];
  fenInput: string;
}>();

const emit = defineEmits<{
  (e: 'update:white-player-type', val: string): void;
  (e: 'update:black-player-type', val: string): void;
  (e: 'update:white-think-time', val: number): void;
  (e: 'update:black-think-time', val: number): void;
  (e: 'update:touch-move', val: boolean): void;
  (e: 'update:auto-assess', val: boolean): void;
  (e: 'update:sound-enabled', val: boolean): void;
  (e: 'update:fen-input', val: string): void;
  (e: 'new-game'): void;
  (e: 'get-hint'): void;
  (e: 'run-assess', idx?: number, fromHistory?: boolean): void;
  (e: 'undo'): void;
  (e: 'open-replay'): void;
  (e: 'toggle-flip'): void;
  (e: 'save-game'): void;
  (e: 'load-game', event: Event): void;
  (e: 'load-fen'): void;
  (e: 'toggle-edit'): void;
}>();

const thinkOptions = [
  { v: 100, l: '0.1s (weak)' },
  { v: 300, l: '0.3s' },
  { v: 1000, l: '1s' },
  { v: 3000, l: '3s' },
  { v: 10000, l: '10s (strong)' }
];

const fileInput = ref<HTMLInputElement | null>(null);
const loadFile = () => fileInput.value?.click();

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
.clock { font-family: ui-monospace, Menlo, monospace; font-weight: bold; background: #1a1a1a; padding: 2px 6px; border-radius: 4px; color: #fff; margin-left: auto; margin-right: 8px; font-size: 14px; min-width: 50px; text-align: center; }
#history { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 280px; overflow-y: auto; background: #2b2b2b; padding: 8px 10px; border-radius: 3px; }
#history div { line-height: 1.5; }
#fen { font-family: ui-monospace, Menlo, monospace; font-size: 11px; word-break: break-all; color: #888; background: #2b2b2b; padding: 6px 8px; border-radius: 3px; }

.move-span { cursor: pointer; padding: 0 3px; border-radius: 2px; }
.move-span:hover { background: #3a3a3a; }

.hint-info { color: #9fdcb5; }

:global(body.editing) .hide-on-edit { display: none; }
</style>
