<template>
  <div class="side-panel">
    <section class="section hide-on-edit">
      <h3>Players</h3>
      <div class="row"><label>White</label>
        <span class="seg">
          <label><input type="radio" value="h" :modelValue="whitePlayerType" @change="$emit('update:white-player-type', $event.target.value)">Human</label>
          <label><input type="radio" value="e" :modelValue="whitePlayerType" @change="$emit('update:white-player-type', $event.target.value)">Engine</label>
        </span>
      </div>
      <div class="row" v-if="whitePlayerType === 'e'"><label>White think</label>
        <select :value="whiteThinkTime" @change="$emit('update:white-think-time', parseInt($event.target.value))">
          <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
        </select>
      </div>
      <div class="row"><label>Black</label>
        <span class="seg">
          <label><input type="radio" value="h" :modelValue="blackPlayerType" @change="$emit('update:black-player-type', $event.target.value)">Human</label>
          <label><input type="radio" value="e" :modelValue="blackPlayerType" @change="$emit('update:black-player-type', $event.target.value)">Engine</label>
        </span>
      </div>
      <div class="row" v-if="blackPlayerType === 'e'"><label>Black think</label>
        <select :value="blackThinkTime" @change="$emit('update:black-think-time', parseInt($event.target.value))">
          <option v-for="opt in thinkOptions" :key="opt.v" :value="opt.v">{{ opt.l }}</option>
        </select>
      </div>
      <div class="btn-row">
        <button @click="$emit('toggle-pause')" :class="['btn-view', { 'btn-pause-on': paused }]">
          {{ paused ? 'Resume' : 'Pause' }}
        </button>
        <button @click="$emit('new-game')" style="flex: 2; background: #2d5a2d; border-color: #3a703a; font-weight: 600;">New Game</button>
      </div>
    </section>

    <section class="section hide-on-edit">
      <h3>Options</h3>
      <div class="row" title="Tournament rule: clicking a piece commits you to moving it."><label>Touch-move</label>
        <input type="checkbox" :checked="touchMoveEnabled" @change="$emit('update:touch-move', $event.target.checked)">
      </div>
      <div class="row" title="After each move you make, the engine evaluates how good it was."><label>Assess my moves</label>
        <input type="checkbox" :checked="autoAssess" @change="$emit('update:auto-assess', $event.target.checked)">
      </div>
      <div class="row" title="Play a click when a move is made."><label>Sound</label>
        <input type="checkbox" :checked="soundEnabled" @change="$emit('update:sound-enabled', $event.target.checked)">
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
        <input type="text" :value="fenInput" @input="$emit('update:fen-input', $event.target.value)" placeholder="paste FEN…" @keyup.enter="$emit('load-fen')">
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

<script setup>
import { ref } from 'vue';
import { ASSESS_COLORS, ASSESS_SYMBOL } from '../constants';

const props = defineProps({
  state: Object,
  editMode: Boolean,
  paused: Boolean,
  whitePlayerType: String,
  blackPlayerType: String,
  whiteThinkTime: Number,
  blackThinkTime: Number,
  touchMoveEnabled: Boolean,
  autoAssess: Boolean,
  soundEnabled: Boolean,
  hintInfo: String,
  assessInfo: String,
  assessColor: String,
  historyPairs: Array,
  fenInput: String
});

defineEmits([
  'update:white-player-type', 'update:black-player-type', 
  'update:white-think-time', 'update:black-think-time',
  'update:touch-move', 'update:auto-assess', 'update:sound-enabled',
  'update:fen-input', 'toggle-pause', 'new-game', 'get-hint', 'run-assess',
  'undo', 'open-replay', 'toggle-flip', 'save-game', 'load-game', 'load-fen', 'toggle-edit'
]);

const thinkOptions = [
  { v: 100, l: '0.1s (weak)' },
  { v: 300, l: '0.3s' },
  { v: 1000, l: '1s' },
  { v: 3000, l: '3s' },
  { v: 10000, l: '10s (strong)' }
];

const fileInput = ref(null);
const loadFile = () => fileInput.value.click();
</script>

<style scoped>
#history { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 280px; overflow-y: auto; background: #2b2b2b; padding: 8px 10px; border-radius: 3px; }
#history div { line-height: 1.5; }
#fen { font-family: ui-monospace, Menlo, monospace; font-size: 11px; word-break: break-all; color: #888; background: #2b2b2b; padding: 6px 8px; border-radius: 3px; }

.move-span { cursor: pointer; padding: 0 3px; border-radius: 2px; }
.move-span:hover { background: #3a3a3a; }

.hint-info { color: #9fdcb5; }

:global(body.editing) .hide-on-edit { display: none; }
</style>
