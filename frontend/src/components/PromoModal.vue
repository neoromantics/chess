<template>
  <div id="modal">
    <div id="promo">
      <div>Promote to:</div>
      <div id="promo-buttons">
        <button v-for="p in promoOptions" 
                :key="p.v" 
                :class="turn === 'w' ? 'white-piece' : 'black-piece'"
                @click="$emit('select', p.v)">
          {{ PIECE[p.char] }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { PIECE } from '../constants';

const props = defineProps<{
  turn: string | undefined;
}>();

defineEmits<{
  (e: 'select', val: string): void;
}>();

const promoOptions = computed(() => {
  const isW = props.turn === 'w';
  return [
    { v: 'q', char: isW ? 'Q' : 'q' },
    { v: 'r', char: isW ? 'R' : 'r' },
    { v: 'b', char: isW ? 'B' : 'b' },
    { v: 'n', char: isW ? 'N' : 'n' }
  ];
});
</script>

<style scoped>
#modal { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: flex; align-items: center; justify-content: center; z-index: 1000; }
#promo { background: #2b2b2b; padding: 16px; border-radius: 6px; text-align: center; }
#promo > div:first-child { margin-bottom: 8px; }
#promo button { font-size: 40px; padding: 8px 14px; margin: 4px; line-height: 1; background: #333; color: #ddd; border: 1px solid #555; border-radius: 3px; cursor: pointer; }
#promo button:hover { background: #404040; }

.white-piece { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.black-piece { color: #000; }
</style>
