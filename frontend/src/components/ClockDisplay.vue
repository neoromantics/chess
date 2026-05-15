<template>
  <section v-if="hasClock" class="clocks">
    <div class="clock" :class="{ active: state?.clock_mover === 'b' && isOngoing, low: blackLow, flagged: blackFlagged }">
      <span class="who">Black</span>
      <span class="time">{{ formatClock(blackDisplayMS) }}</span>
    </div>
    <div class="clock" :class="{ active: state?.clock_mover === 'w' && isOngoing, low: whiteLow, flagged: whiteFlagged }">
      <span class="who">White</span>
      <span class="time">{{ formatClock(whiteDisplayMS) }}</span>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { StateJSON } from '../types';

const props = defineProps<{ state: StateJSON | null }>();

// Local tick — re-renders the displayed bank ~10× a second so the
// running side counts down smoothly without waiting for the next
// snapshot. The server's clock_server_ms anchors us; we only
// extrapolate forward from receipt, never trust local time as ground
// truth.
const now = ref(Date.now());
let timerHandle: number | null = null;
onMounted(() => {
  timerHandle = window.setInterval(() => { now.value = Date.now(); }, 100);
});
onUnmounted(() => {
  if (timerHandle !== null) { window.clearInterval(timerHandle); timerHandle = null; }
});

const hasClock = computed(() => !!props.state && props.state.clock_initial_ms > 0);
const isOngoing = computed(() => props.state?.status === 'ongoing');

// Anchor: the moment we received the snapshot in browser-local time,
// paired with the server's reported snapshot timestamp. Using the
// delta (browser_now - browser_anchor) avoids any clock-skew
// adjustment — we just measure local elapsed time since the last
// snapshot and burn that down from the mover's bank.
const anchorBrowser = ref(Date.now());
watch(() => props.state?.clock_server_ms, () => { anchorBrowser.value = Date.now(); });

const elapsedSinceAnchor = computed(() => Math.max(0, now.value - anchorBrowser.value));

const whiteDisplayMS = computed(() => {
  if (!props.state) return 0;
  let ms = props.state.white_clock_ms;
  if (isOngoing.value && props.state.clock_mover === 'w') ms -= elapsedSinceAnchor.value;
  return Math.max(0, ms);
});
const blackDisplayMS = computed(() => {
  if (!props.state) return 0;
  let ms = props.state.black_clock_ms;
  if (isOngoing.value && props.state.clock_mover === 'b') ms -= elapsedSinceAnchor.value;
  return Math.max(0, ms);
});

const whiteLow = computed(() => isOngoing.value && whiteDisplayMS.value < 30_000 && whiteDisplayMS.value > 0);
const blackLow = computed(() => isOngoing.value && blackDisplayMS.value < 30_000 && blackDisplayMS.value > 0);
const whiteFlagged = computed(() => whiteDisplayMS.value === 0 && (props.state?.status === 'timeout' || !isOngoing.value));
const blackFlagged = computed(() => blackDisplayMS.value === 0 && (props.state?.status === 'timeout' || !isOngoing.value));

// MM:SS for >= 20s, MM:SS.D (one decimal) for last 20 seconds so the
// player can see tenths during the bullet-style scramble at the end.
function formatClock(ms: number): string {
  const totalSec = ms / 1000;
  const m = Math.floor(totalSec / 60);
  const s = totalSec - m * 60;
  if (totalSec < 20) {
    return `${m}:${s.toFixed(1).padStart(4, '0')}`;
  }
  const sInt = Math.floor(s);
  return `${m}:${sInt.toString().padStart(2, '0')}`;
}
</script>

<style scoped>
.clocks { display: flex; flex-direction: column; gap: 6px; margin-bottom: 14px; }
.clock {
  display: flex; align-items: center; justify-content: space-between;
  background: #232323; border-left: 3px solid #444;
  padding: 10px 14px; border-radius: 4px;
  font-family: ui-monospace, Menlo, monospace;
  transition: border-color 120ms ease, background-color 120ms ease;
}
.clock.active { border-left-color: #4a8a6b; background: #2a2f2c; }
.clock.low { border-left-color: #d4a14a; }
.clock.low.active { background: #2f2c25; }
.clock.flagged { border-left-color: #c0524a; color: #ff8a85; }
.who { font-size: 12px; color: #aaa; letter-spacing: 0.5px; text-transform: uppercase; }
.clock.active .who { color: #cfd8d2; }
.time { font-size: 22px; font-variant-numeric: tabular-nums; font-weight: 600; }
</style>
