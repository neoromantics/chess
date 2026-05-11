<template>
  <div id="edit-panel">
    <div style="font-weight:600; margin-bottom: 6px;">Edit Position</div>
    <div style="font-size:12px; color:#aaa;">Pick a piece, click squares to place. Right-click a square to remove.</div>
    <div id="edit-palette">
      <button v-for="tool in editTools" 
              :key="tool.v" 
              :title="tool.title"
              :class="{ sel: editPalette === tool.v, 'white-piece': tool.isWhite, 'black-piece': tool.isBlack }"
              @click="$emit('update:edit-palette', tool.v)">
        {{ tool.label }}
      </button>
    </div>
    <div class="row"><label>Turn</label>
      <select :value="editTurn" @change="$emit('update:edit-turn', $event.target.value)">
        <option value="w">White</option>
        <option value="b">Black</option>
      </select>
    </div>
    <div class="row"><label>Castling</label>
      <span style="font-family: ui-monospace, Menlo, monospace;">
        <label v-for="(val, key) in editCastling" :key="key">
          <input type="checkbox" :checked="editCastling[key]" @change="editCastling[key] = $event.target.checked"> {{ key }}
        </label>
      </span>
    </div>
    <div class="row" style="margin-top:8px; gap:6px;">
      <button @click="$emit('clear')" style="flex:1">Clear</button>
      <button @click="$emit('start-pos')" style="flex:1">Start Pos</button>
    </div>
    <div class="row" style="margin-top:6px; gap:6px;">
      <button @click="$emit('apply')" style="flex:1; background:#2d5a2d; border-color:#3a703a;">Apply</button>
      <button @click="$emit('cancel')" style="flex:1">Cancel</button>
    </div>
  </div>
</template>

<script setup>
import { PIECE } from '../constants';

defineProps({
  editPalette: String,
  editTurn: String,
  editCastling: Object
});

defineEmits(['update:edit-palette', 'update:edit-turn', 'clear', 'start-pos', 'apply', 'cancel']);

const editTools = [
  { v: 'select', label: '☞', title: 'Select / move' },
  { v: 'delete', label: '×', title: 'Delete' },
  ...['K','Q','R','B','N','P'].map(c => ({ v: c, label: PIECE[c], title: 'Paint ' + c, isWhite: true })),
  ...['k','q','r','b','n','p'].map(c => ({ v: c, label: PIECE[c], title: 'Paint ' + c, isBlack: true }))
];
</script>

<style scoped>
#edit-panel { margin-top: 12px; padding: 10px; background: #2b2b2b; border-radius: 4px; }
#edit-palette { display: grid; grid-template-columns: repeat(7, 1fr); gap: 4px; margin: 8px 0; }
#edit-palette button { font-size: 28px; padding: 0; height: 40px; line-height: 1; background: #3a3a3a; }
#edit-palette button.sel { background: #ffe066; color: #000; outline: 2px solid #ffe066; }

.white-piece { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.black-piece { color: #000; }
</style>
