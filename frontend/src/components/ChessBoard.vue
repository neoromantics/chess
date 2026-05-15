<template>
  <div id="board-wrap">
    <div class="board-row">
      <div class="coords-ranks">
        <div v-for="n in ranks" :key="n">{{ n }}</div>
      </div>
      <div id="board">
        <div v-for="sq in boardSquares"
             :key="sq.name"
             :class="['sq', sq.dark ? 'dark' : 'light', sq.classes]"
             @click="$emit('square-click', sq)">
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
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { PIECE, parseBoard } from '../constants';
import { StateJSON, Square } from '../types';

const props = defineProps<{
  state: StateJSON | null;
  flipped: boolean;
  selected: string | null;
  hint: { from: string; to: string } | null;
}>();

defineEmits<{
  (e: 'square-click', sq: Square): void;
}>();

const ranks = computed(() => props.flipped ? [1,2,3,4,5,6,7,8] : [8,7,6,5,4,3,2,1]);
const files = computed(() => (props.flipped ? 'hgfedcba' : 'abcdefgh').split(''));

const boardSquares = computed(() => {
  if (!props.state) return [];
  const grid = parseBoard(props.state.fen);
  const res: Square[] = [];

  for (let i = 0; i < 8; i++) {
    for (let j = 0; j < 8; j++) {
      const r = props.flipped ? 7 - i : i;
      const f = props.flipped ? 7 - j : j;
      const name = String.fromCharCode(97+f) + (8-r);
      const pc = grid[r][f];

      const lastMove = props.state?.last_move;
      const inCheck = !!props.state?.in_check;
      const turn = props.state?.turn;
      const classes: Record<string, boolean> = {
        last: !!(lastMove && (lastMove.from === name || lastMove.to === name)),
        sel: props.selected === name,
        check: !!(inCheck && pc && ((turn === 'w' && pc === 'K') || (turn === 'b' && pc === 'k'))),
        hint: !!(props.hint && (props.hint.from === name || props.hint.to === name)),
      };

      if (props.selected && props.state) {
        const moves = props.state.legal_moves.filter(m => m.startsWith(props.selected!) && m.substring(2,4) === name);
        if (moves.length) classes[pc ? 'cap' : 'dot'] = true;
      }

      res.push({
        name, r, f, dark: (r + f) % 2 === 1,
        piece: pc ? { char: pc, color: pc === pc.toUpperCase() ? 'w' : 'b' } : null,
        classes
      });
    }
  }
  return res;
});
</script>

<style scoped>
#board { display: grid; grid-template-columns: repeat(8, var(--sq)); grid-template-rows: repeat(8, var(--sq)); position: relative; }
.sq { width: var(--sq); height: var(--sq); display: flex; align-items: center; justify-content: center; font-size: calc(var(--sq) * 0.78); cursor: pointer; user-select: none; position: relative; line-height: 1; }
.light { background: #f0d9b5; }
.dark  { background: #b58863; }
.sel   { box-shadow: inset 0 0 0 4px #ffe066; }
.last  { background: #cdd26a !important; }
.check { box-shadow: inset 0 0 0 4px #ff4d4d; }
.hint  { box-shadow: inset 0 0 0 5px #4ec77a; }

.dot::before { content: ""; position: absolute; width: calc(var(--sq) * 0.34); height: calc(var(--sq) * 0.34); border-radius: 50%; background: rgba(0,0,0,0.25); }
.cap::before { content: ""; position: absolute; inset: 5px; border-radius: 50%; box-shadow: inset 0 0 0 6px rgba(20,20,20,0.35); }
.white-piece { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.black-piece { color: #000; }

.coords-files, .coords-ranks { color: #888; font-size: calc(var(--sq) * 0.12); user-select: none; }
.coords-files { display: grid; grid-template-columns: repeat(8, var(--sq)); padding-left: calc(var(--sq) * 0.2); }
.coords-files div { text-align: center; padding: 4px 0; }
.board-row { display: flex; align-items: center; }
.coords-ranks { display: grid; grid-template-rows: repeat(8, var(--sq)); width: calc(var(--sq) * 0.2); }
.coords-ranks div { display: flex; align-items: center; justify-content: center; }
</style>
