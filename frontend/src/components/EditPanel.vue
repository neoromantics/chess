<template>
  <div class="edit-panel">
    <header class="edit-header">
      <span class="title">Set up position</span>
      <span class="hint-text">
        Click a piece, then click a square. Right-click to remove.
      </span>
    </header>

    <div class="palette">
      <button
        v-for="tool in tools"
        :key="tool.v"
        :class="['pal-btn', { sel: palette === tool.v, white: tool.color === 'w', black: tool.color === 'b' }]"
        :title="tool.title"
        @click="$emit('update:palette', tool.v)"
      >{{ tool.label }}</button>
    </div>

    <div class="row">
      <span class="lbl">Side to move</span>
      <div class="seg">
        <button :class="{ active: turn === 'w' }" @click="$emit('update:turn', 'w')">White</button>
        <button :class="{ active: turn === 'b' }" @click="$emit('update:turn', 'b')">Black</button>
      </div>
    </div>

    <div class="row castling">
      <span class="lbl">Castling</span>
      <div class="cast-grid">
        <label v-for="key in castleKeys" :key="key">
          <input
            type="checkbox"
            :checked="castling[key]"
            @change="onCastle(key, ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ key }}</span>
        </label>
      </div>
    </div>

    <div class="row buttons">
      <button class="btn ghost" @click="$emit('clear')">Empty</button>
      <button class="btn ghost" @click="$emit('start-pos')">Start pos</button>
    </div>
    <!-- Save setup: bank the position as a study row without applying
         it to the game. Lets the user build a puzzle/training position
         and come back to it later. Gated on canSave (= signed in) so
         anonymous users don't get a button that 401s on click. -->
    <div v-if="canSave" class="row buttons">
      <button class="btn ghost full" @click="$emit('save-setup')" title="Save this position to your studies without applying it">Save setup</button>
    </div>
    <div class="row buttons">
      <button class="btn ghost" @click="$emit('cancel')">Cancel</button>
      <button class="btn primary" @click="$emit('apply')">Apply</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { PIECE } from '../constants';

const props = defineProps<{
  palette: string;
  turn: string;
  castling: Record<string, boolean>;
  // canSave gates the "Save setup" button. False for anonymous users
  // (createStudy is auth-only) so we don't show a 401-on-click button.
  canSave?: boolean;
}>();

const emit = defineEmits<{
  (e: 'update:palette', val: string): void;
  (e: 'update:turn', val: string): void;
  (e: 'update:castling', val: Record<string, boolean>): void;
  (e: 'clear'): void;
  (e: 'start-pos'): void;
  (e: 'apply'): void;
  (e: 'cancel'): void;
  // Save the current editor state to studies without applying it to
  // the game. GameView builds the FEN and posts it.
  (e: 'save-setup'): void;
}>();

const tools = [
  { v: 'delete', label: '×', title: 'Erase (or right-click)', color: '' },
  ...['K','Q','R','B','N','P'].map(c => ({ v: c, label: PIECE[c], title: 'Place white ' + c, color: 'w' })),
  ...['k','q','r','b','n','p'].map(c => ({ v: c, label: PIECE[c], title: 'Place black ' + c, color: 'b' })),
];

const castleKeys = ['K','Q','k','q'];

const onCastle = (key: string, val: boolean) => {
  emit('update:castling', { ...props.castling, [key]: val });
};
</script>

<style scoped>
.edit-panel {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 8px;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.edit-header { display: flex; flex-direction: column; gap: 4px; }
.title { font-size: 13px; font-weight: 600; color: #ddd; text-transform: uppercase; letter-spacing: 0.5px; }
.hint-text { font-size: 11px; color: #888; line-height: 1.4; }

.palette {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
  gap: 4px;
}
.pal-btn {
  background: #2f2f2f;
  border: 1px solid #3a3a3a;
  border-radius: 4px;
  color: #ccc;
  font-size: 22px;
  line-height: 1;
  padding: 8px 0;
  cursor: pointer;
  font: inherit;
  transition: border-color 120ms ease, background-color 120ms ease;
}
.pal-btn:hover { border-color: #4a6b8a; }
.pal-btn.sel { border-color: #ffe066; background: rgba(255,224,102,0.08); color: #ffe066; }
.pal-btn.white { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.pal-btn.black { color: #000; }

.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; color: #ccc; }
.lbl { color: #888; min-width: 90px; font-size: 12px; }
.seg { display: inline-flex; background: #1c1c1c; border: 1px solid #333; border-radius: 6px; overflow: hidden; }
.seg button { background: transparent; border: 0; color: #aaa; padding: 5px 12px; font-size: 12px; cursor: pointer; font: inherit; }
.seg button.active { background: #3a4754; color: #fff; }

.castling .cast-grid { display: flex; gap: 10px; flex-wrap: wrap; }
.castling label { display: inline-flex; align-items: center; gap: 4px; font-family: ui-monospace, Menlo, monospace; font-size: 12px; color: #ccc; cursor: pointer; }
.castling input { accent-color: #4a6b8a; }

.buttons { gap: 8px; }
.btn {
  flex: 1 1 0;
  padding: 8px 12px;
  background: #2f2f2f;
  border: 1px solid #3a3a3a;
  border-radius: 6px;
  color: #e0e0e0;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font: inherit;
}
.btn:hover { background: #383838; }
.btn.primary { background: #2d5a2d; border-color: #3a703a; color: #fff; font-weight: 600; }
.btn.primary:hover { background: #347a34; }
.btn.ghost { background: transparent; color: #ccc; }
.btn.ghost:hover { background: #2a2a2a; }
.btn.full { flex: 1 1 100%; }
</style>
