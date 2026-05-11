<template>
  <div id="app-container">
    <div id="board-wrap">
      <div class="board-row">
        <div class="coords-ranks">
          <div v-for="n in ranks" :key="n">{{ n }}</div>
        </div>
        <div id="board">
          <div v-for="sq in boardSquares" 
               :key="sq.name" 
               :class="['sq', sq.dark ? 'dark' : 'light', { last: sq.last }]">
            <span v-if="sq.piece" :class="sq.piece.color === 'w' ? 'white-piece' : 'black-piece'">
              {{ PIECE[sq.piece.char] }}
            </span>
          </div>
        </div>
      </div>
      <div class="coords-files">
        <div v-for="f in files" :key="f">{{ f }}</div>
      </div>
    </div>

    <div id="side">
      <h2>Replay</h2>
      <div id="status">{{ statusMsg }}</div>
      <div class="controls">
        <button @click="stop(); idx = 0" title="Go to start">⏮</button>
        <button @click="stop(); if (idx > 0) idx--" title="Previous">◀</button>
        <button @click="togglePlay" title="Play / Pause" id="play-btn">
          {{ timer ? '⏸' : '▶' }}
        </button>
        <button @click="stop(); if (idx < frames.length - 1) idx++" title="Next">▶</button>
        <button @click="stop(); idx = frames.length - 1" title="Go to end">⏭</button>
        <button @click="flipped = !flipped" title="Flip board (F)" style="margin-left:8px;">Flip</button>
      </div>
      <div class="speed">
        <span>Speed</span>
        <input type="range" v-model.number="speed" min="150" max="3000" step="50">
        <span>{{ speed }}ms</span>
      </div>
      <div id="moves">
        <div v-for="(pair, i) in movePairs" :key="i" class="row-line">
          {{ i + 1 }}. 
          <span v-for="(mv, j) in pair" :key="j">
            <span :class="['move', { cur: mv.idx === idx }]" @click="stop(); idx = mv.idx">
              {{ mv.san }}
            </span>
            <span v-if="j === 0 && pair.length > 1">&nbsp;</span>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { PIECE, parseBoard } from './constants';
import { ReplayFrame } from './types';

const frames = ref<ReplayFrame[]>([{ fen: 'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1' }]);
const idx = ref(0);
const timer = ref<ReturnType<typeof setTimeout> | null>(null);
const speed = ref(800);
const flipped = ref(localStorage.getItem('chess-flipped') === '1');

// Load frames from script tag
onMounted(() => {
  const dataEl = document.getElementById('replay-data');
  if (dataEl) {
    try {
      const parsed = JSON.parse(dataEl.textContent || '');
      if (Array.isArray(parsed) && parsed.length > 0) {
        frames.value = parsed;
      }
    } catch (e) {
      console.error('Failed to parse replay data', e);
    }
  }

  window.addEventListener('keydown', handleKeydown);
  if (frames.value.length > 1) setTimeout(play, 600);
});

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown);
  stop();
});

const ranks = computed(() => flipped.value ? [1,2,3,4,5,6,7,8] : [8,7,6,5,4,3,2,1]);
const files = computed(() => (flipped.value ? 'hgfedcba' : 'abcdefgh').split(''));

const currentFrame = computed(() => frames.value[idx.value]);

const boardSquares = computed(() => {
  const grid = parseBoard(currentFrame.value.fen);
  const res: any[] = [];
  for (let i = 0; i < 8; i++) {
    for (let j = 0; j < 8; j++) {
      const r = flipped.value ? 7 - i : i;
      const f = flipped.value ? 7 - j : j;
      const name = String.fromCharCode(97+f) + (8-r);
      const pc = grid[r][f];
      res.push({
        name, dark: (r + f) % 2 === 1,
        piece: pc ? { char: pc, color: pc === pc.toUpperCase() ? 'w' : 'b' } : null,
        last: currentFrame.value.from === name || currentFrame.value.to === name
      });
    }
  }
  return res;
});

const statusMsg = computed(() => {
  let label = idx.value === 0 ? 'Start position' : `Move ${idx.value}: ${currentFrame.value.san || ''}`;
  if (idx.value === frames.value.length - 1 && idx.value > 0) label += ' (end)';
  return label;
});

const movePairs = computed(() => {
  const res: any[] = [];
  for (let i = 1; i < frames.value.length; i += 2) {
    const pair = [{ san: frames.value[i].san || '?', idx: i }];
    if (i + 1 < frames.value.length) {
      pair.push({ san: frames.value[i+1].san || '?', idx: i + 1 });
    }
    res.push(pair);
  }
  return res;
});

function stop() {
  if (timer.value) {
    clearTimeout(timer.value);
    timer.value = null;
  }
}

function tick() {
  if (idx.value >= frames.value.length - 1) {
    stop();
    return;
  }
  idx.value++;
  timer.value = setTimeout(tick, speed.value);
}

function play() {
  if (idx.value >= frames.value.length - 1) idx.value = 0;
  tick();
}

function togglePlay() {
  if (timer.value) stop();
  else play();
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowLeft') { stop(); if (idx.value > 0) idx.value--; }
  else if (e.key === 'ArrowRight') { stop(); if (idx.value < frames.value.length - 1) idx.value++; }
  else if (e.key === ' ') { e.preventDefault(); togglePlay(); }
  else if (e.key === 'Home') { stop(); idx.value = 0; }
  else if (e.key === 'End') { stop(); idx.value = frames.value.length - 1; }
  else if (e.key === 'f' || e.key === 'F') flipped.value = !flipped.value;
}
</script>

<style>
:root { --sq: 88px; }
* { box-sizing: border-box; }
body { font-family: -apple-system, system-ui, sans-serif; background: #1e1e1e; color: #ddd; margin: 0; min-height: 100vh; }
#app-container { display: flex; gap: 32px; align-items: flex-start; justify-content: center; padding: 32px 20px; min-height: 100vh; width: 100%; }

#board-wrap { display: inline-block; border: 4px solid #333; }
#board { display: grid; grid-template-columns: repeat(8, var(--sq)); grid-template-rows: repeat(8, var(--sq)); }
.sq { width: var(--sq); height: var(--sq); display: flex; align-items: center; justify-content: center; font-size: calc(var(--sq) * 0.78); user-select: none; line-height: 1; position: relative; }
.light { background: #f0d9b5; }
.dark  { background: #b58863; }
.last  { background: #cdd26a !important; }
.white-piece { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.black-piece { color: #000; }

#side { width: 320px; }
h2 { margin: 0 0 12px; }
#status { padding: 10px; background: #2b2b2b; border-radius: 4px; min-height: 22px; font-weight: 600; margin-bottom: 12px; }

.controls { display: flex; gap: 6px; align-items: center; }
button { background: #333; color: #ddd; border: 1px solid #555; padding: 8px 12px; border-radius: 3px; font: inherit; cursor: pointer; }
button:hover { background: #404040; }
button:disabled { opacity: 0.4; cursor: default; }
#play-btn { width: 56px; }

.speed { display: flex; align-items: center; gap: 8px; margin-top: 10px; }
.speed input { flex: 1; }

#moves { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 380px; overflow-y: auto; background: #2b2b2b; padding: 8px; border-radius: 3px; margin-top: 12px; }
#moves .move { display: inline-block; padding: 0; cursor: pointer; border-radius: 2px; }
#moves .move:hover { background: #444; }
#moves .move.cur { background: #4a6b8a; color: #fff; }
.row-line { line-height: 1.6; }

.coords-files, .coords-ranks { color: #888; font-size: 12px; user-select: none; }
.coords-files { display: grid; grid-template-columns: repeat(8, var(--sq)); padding-left: 22px; }
.coords-files div { text-align: center; padding: 4px 0; }
.board-row { display: flex; align-items: center; }
.coords-ranks { display: grid; grid-template-rows: repeat(8, var(--sq)); width: 22px; }
.coords-ranks div { display: flex; align-items: center; justify-content: center; }
</style>
