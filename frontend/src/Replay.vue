<template>
  <div id="replay-root">
    <!-- Minimal top nav. The replay page is a separate single-file
         bundle (no Pinia / no vue-router), so reproducing the full
         Navbar component would pull in half the SPA. A simple link
         row plus an inline notification bell (plain fetch, no store)
         gives signed-in viewers parity with the main SPA's nav without
         dragging the rest of it in. -->
    <nav class="replay-nav">
      <a class="replay-brand" href="/">Chess</a>
      <div class="replay-nav-links">
        <a href="/">Home</a>
        <a href="/match">Match</a>
        <a href="/profile">Profile</a>

        <!-- Sound toggle. Click counts as a user gesture so the
             AudioContext can resume immediately; muted state persists
             across the SPA via the shared chess-muted localStorage key. -->
        <button
          class="sound-btn"
          :class="{ on: soundEnabled }"
          :aria-pressed="soundEnabled"
          :title="soundEnabled ? 'Sound on (click to mute)' : 'Sound off (click to unmute)'"
          @click="toggleSound"
        >
          <svg v-if="soundEnabled" class="sound-icon" aria-hidden="true" viewBox="0 0 24 24" fill="none"
               stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <path d="M11 5L6 9H3v6h3l5 4z" />
            <path d="M15.5 8.5a5 5 0 0 1 0 7" />
            <path d="M18 6a8 8 0 0 1 0 12" />
          </svg>
          <svg v-else class="sound-icon" aria-hidden="true" viewBox="0 0 24 24" fill="none"
               stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
            <path d="M11 5L6 9H3v6h3l5 4z" />
            <line x1="16" y1="9" x2="22" y2="15" />
            <line x1="22" y1="9" x2="16" y2="15" />
          </svg>
        </button>

        <!-- Bell only renders once the pending-invites fetch resolves
             with a 2xx. A 401 (not signed in) leaves bellAvailable=false
             and the slot stays empty, so spectator viewers don't see a
             dead control. -->
        <div v-if="bellAvailable" class="bell-wrap" v-click-outside="closeBell">
          <button
            class="bell-btn"
            :class="{ active: bellOpen, has: pendingInvites.length > 0 }"
            @click="toggleBell"
            :aria-label="pendingInvites.length > 0 ? `${pendingInvites.length} pending invites` : 'No pending invites'"
          >
            <svg class="bell-icon" aria-hidden="true" viewBox="0 0 24 24" fill="none"
                 stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M6 8a6 6 0 0 1 12 0c0 5 2 6 2 8H4c0-2 2-3 2-8z" />
              <path d="M10 19a2 2 0 1 0 4 0" />
            </svg>
            <span v-if="pendingInvites.length > 0" class="badge">{{ pendingInvites.length }}</span>
          </button>
          <div v-if="bellOpen" class="bell-panel">
            <header class="bell-header">
              <span class="bell-title">Invites</span>
              <span class="muted">{{ pendingInvites.length }} pending</span>
            </header>
            <ul v-if="pendingInvites.length" class="bell-list">
              <li v-for="inv in pendingInvites" :key="inv.id" class="bell-item">
                <div class="bell-item-text">
                  <strong>{{ inv.from_username || 'Unknown' }}</strong>
                  <span class="muted"> · {{ inv.time_control }}{{ inv.rated ? ' · rated' : '' }}</span>
                </div>
                <div class="bell-actions">
                  <button class="bell-btn-accept" :disabled="actingOn === inv.id" @click="onAccept(inv)">Accept</button>
                  <button class="bell-btn-decline" :disabled="actingOn === inv.id" @click="onDecline(inv)">Decline</button>
                </div>
              </li>
            </ul>
            <p v-else class="bell-empty">No invites right now.</p>
            <footer class="bell-footer">
              <a href="/invites" class="bell-link">View all invites →</a>
            </footer>
          </div>
        </div>
      </div>
    </nav>
    <main id="app-container">
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
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, type DirectiveBinding } from 'vue';
import { PIECE, parseBoard } from './constants';
import { ReplayFrame, InviteWire } from './types';

const frames = ref<ReplayFrame[]>([{ fen: 'rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1' }]);
const idx = ref(0);
const timer = ref<ReturnType<typeof setTimeout> | null>(null);
const speed = ref(800);
const flipped = ref(localStorage.getItem('chess-flipped') === '1');

// Sound on/off — shares the chess-muted localStorage key with the main
// SPA's GameView so the setting is unified across pages. Browser audio
// is gated behind a user gesture; the very first replay step likely
// happens before any click, so we'll silently skip until the user
// interacts with the page. Toggling the speaker icon counts as a
// gesture and primes the AudioContext immediately.
const soundEnabled = ref(localStorage.getItem('chess-muted') !== '1');
let audioCtx: AudioContext | null = null;
const ensureAudio = (): AudioContext | null => {
  if (typeof window === 'undefined' || typeof (window as any).AudioContext === 'undefined') return null;
  if (!audioCtx) audioCtx = new (window.AudioContext || (window as any).webkitAudioContext)();
  return audioCtx;
};
const playClick = (freq: number, dur: number, gain: number) => {
  if (!soundEnabled.value) return;
  const ctx = ensureAudio();
  if (!ctx || ctx.state === 'suspended') return;
  const osc = ctx.createOscillator();
  const g = ctx.createGain();
  osc.connect(g); g.connect(ctx.destination);
  osc.type = 'triangle';
  osc.frequency.value = freq;
  g.gain.setValueAtTime(gain, ctx.currentTime);
  g.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + dur);
  osc.start();
  osc.stop(ctx.currentTime + dur);
};
const toggleSound = () => {
  soundEnabled.value = !soundEnabled.value;
  localStorage.setItem('chess-muted', soundEnabled.value ? '0' : '1');
  // The toggle click itself is a gesture — resume() now so the next
  // step plays audibly instead of waiting for another user action.
  if (soundEnabled.value) {
    const ctx = ensureAudio();
    if (ctx && ctx.state === 'suspended') void ctx.resume();
  }
};
// Single wiring point for every navigation path: auto-play, next/prev
// buttons, arrow keys, click-to-jump in the move list, Home/End. Fires
// on any idx change; the initial 0→0 of the first frame is skipped by
// the !== oldIdx guard inherent to watch.
watch(idx, (newIdx, oldIdx) => {
  if (newIdx === oldIdx) return;
  if (newIdx === 0) return; // jumping to start position isn't a "move"
  const frame = frames.value[newIdx];
  if (!frame) return;
  const san = frame.san || '';
  if (san.includes('#')) playClick(880, 0.12, 0.14);     // checkmate
  else if (san.includes('+')) { playClick(880, 0.08, 0.12); setTimeout(() => playClick(660, 0.10, 0.10), 90); }
  else if (san.includes('x')) playClick(220, 0.12, 0.15); // capture
  else playClick(520, 0.06, 0.10);                        // quiet move
});

// ===== Notification bell =====
// Replay is a standalone bundle; we reach into the same /api/invites/*
// endpoints over the JWT cookie that the main SPA uses. The fetch
// fails closed (401 -> bell hidden) so unauthenticated viewers don't
// see a dead control.
const bellAvailable = ref(false);
const bellOpen = ref(false);
const pendingInvites = ref<InviteWire[]>([]);
const actingOn = ref<string | null>(null);

const toggleBell = () => { bellOpen.value = !bellOpen.value; };
const closeBell = () => { bellOpen.value = false; };

async function refreshInvites() {
  try {
    const res = await fetch('/api/invites/pending', { credentials: 'include' });
    if (!res.ok) {
      bellAvailable.value = false;
      return;
    }
    const data = await res.json();
    const received: InviteWire[] = data?.received ?? [];
    pendingInvites.value = received.filter((i) => i.status === 'pending');
    bellAvailable.value = true;
  } catch {
    bellAvailable.value = false;
  }
}

async function onAccept(inv: InviteWire) {
  if (actingOn.value) return;
  actingOn.value = inv.id;
  try {
    const res = await fetch(`/api/invites/${inv.id}/accept`, { method: 'POST', credentials: 'include' });
    if (!res.ok) throw new Error(await res.text());
    const accepted: InviteWire = await res.json();
    closeBell();
    if (accepted?.game_id) {
      // Hard navigation: replay is its own bundle, no router available.
      window.location.href = `/game/${accepted.game_id}`;
    }
  } catch (e) {
    console.error('accept failed', e);
  } finally {
    actingOn.value = null;
  }
}

async function onDecline(inv: InviteWire) {
  if (actingOn.value) return;
  actingOn.value = inv.id;
  try {
    await fetch(`/api/invites/${inv.id}/decline`, { method: 'POST', credentials: 'include' });
    pendingInvites.value = pendingInvites.value.filter((i) => i.id !== inv.id);
  } catch (e) {
    console.error('decline failed', e);
  } finally {
    actingOn.value = null;
  }
}

// Local v-click-outside directive (same shape as the one in Navbar.vue).
// Inlined rather than imported because Replay.vue is the only consumer
// in this bundle and lifting to a plugin would just expand the bundle.
const vClickOutside = {
  mounted(el: HTMLElement, binding: DirectiveBinding<() => void>) {
    const handler = (e: MouseEvent) => {
      if (!el.contains(e.target as Node)) binding.value();
    };
    (el as any)._clickOutside = handler;
    document.addEventListener('click', handler);
  },
  unmounted(el: HTMLElement) {
    const handler = (el as any)._clickOutside;
    if (handler) document.removeEventListener('click', handler);
  },
};

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
  void refreshInvites();
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
/* Responsive square sizing — mirrors App.vue's --sq formula so the
   board fits the viewport on phones + laptops without scrolling.
   Subtracted chrome: 48px top nav + 32px container padding + ~30px
   for the coords strip + breathing room. */
:root {
  --sq: min(
    88px,
    calc((100vh - 180px) / 8.2),
    calc((100vw - 380px) / 8.2)
  );
}
@media (max-width: 1000px) {
  :root {
    --sq: min(
      calc((100vh - 220px) / 8.2),
      calc((100vw - 40px) / 8.2)
    );
  }
}
* { box-sizing: border-box; }
body { font-family: -apple-system, system-ui, sans-serif; background: #1e1e1e; color: #ddd; margin: 0; min-height: 100vh; }
#replay-root { display: flex; flex-direction: column; min-height: 100vh; }

.replay-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #2b2b2b;
  border-bottom: 1px solid #3d3d3d;
  padding: 10px 20px;
  height: 48px;
}
.replay-brand { color: #fff; font-size: 18px; font-weight: 700; text-decoration: none; letter-spacing: -0.2px; }
.replay-nav-links { display: flex; gap: 18px; align-items: center; }
.replay-nav-links a { color: #aaa; text-decoration: none; font-size: 14px; transition: color 120ms ease; }
.replay-nav-links a:hover { color: #fff; }

/* Sound toggle. Same visual rhythm as the bell — transparent button
   with a stroked SVG that picks up currentColor, so the on/off state
   is just a color swap. */
.sound-btn { background: transparent; border: 1px solid transparent; color: #777; cursor: pointer; padding: 4px 6px; border-radius: 6px; display: inline-flex; align-items: center; transition: color 120ms ease, background-color 120ms ease; }
.sound-btn:hover { background: #333; color: #ddd; }
.sound-btn.on { color: #aaa; }
.sound-btn.on:hover { color: #fff; }
.sound-icon { width: 18px; height: 18px; display: inline-block; vertical-align: middle; color: inherit; }

/* Bell + dropdown — mirrors Navbar.vue. Replay's nav lives outside
   the SPA's component tree, so the styles are duplicated here rather
   than imported (avoids pulling Navbar.vue's whole CSS into the
   replay bundle). Keep the two in visual sync if either is reskinned. */
.bell-wrap { position: relative; display: inline-block; }
.bell-btn {
  background: transparent;
  border: 1px solid transparent;
  color: #aaa;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 14px;
  line-height: 1;
  position: relative;
  transition: background-color 120ms ease, border-color 120ms ease, color 120ms ease;
}
.bell-btn:hover { background: #333; color: #fff; }
.bell-btn.active { background: #333; border-color: #4a6b8a; color: #fff; }
.bell-icon { width: 18px; height: 18px; display: inline-block; vertical-align: middle; color: inherit; }
.bell-btn.has { color: #f0d27a; }
.bell-btn.has:hover { color: #f5dc94; }

.badge {
  background: #d4544c;
  color: white;
  font-size: 11px;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  position: absolute;
  top: -4px;
  right: -4px;
}

.bell-panel {
  position: absolute;
  top: 110%;
  right: 0;
  z-index: 200;
  width: 320px;
  max-width: calc(100vw - 24px);
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 8px;
  box-shadow: 0 10px 30px rgba(0,0,0,0.5);
  overflow: hidden;
}
.bell-header { display: flex; align-items: baseline; justify-content: space-between; padding: 12px 14px 8px; border-bottom: 1px solid #333; }
.bell-title { font-weight: 600; font-size: 14px; color: #ddd; }
.muted { color: #777; font-size: 12px; }
.bell-list { list-style: none; margin: 0; padding: 6px 0; max-height: 320px; overflow-y: auto; }
.bell-item { display: flex; align-items: center; gap: 10px; padding: 8px 14px; border-bottom: 1px solid #2a2a2a; }
.bell-item:last-child { border-bottom: none; }
.bell-item-text { flex: 1 1 auto; min-width: 0; font-size: 13px; color: #ddd; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bell-actions { display: inline-flex; gap: 6px; flex: 0 0 auto; }
.bell-btn-accept, .bell-btn-decline {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  cursor: pointer;
  border: 1px solid;
  font-weight: 600;
  transition: background-color 120ms ease, border-color 120ms ease;
}
.bell-btn-accept { background: #2d4a2d; border-color: #3a703a; color: #b6e3b6; }
.bell-btn-accept:hover:not(:disabled) { background: #3a6b3a; }
.bell-btn-decline { background: transparent; border-color: #555; color: #aaa; }
.bell-btn-decline:hover:not(:disabled) { background: #333; color: #ddd; }
.bell-btn-accept:disabled, .bell-btn-decline:disabled { opacity: 0.5; cursor: not-allowed; }

.bell-empty { color: #888; font-style: italic; padding: 14px; margin: 0; font-size: 13px; text-align: center; }
.bell-footer { border-top: 1px solid #333; padding: 10px 14px; text-align: right; }
.bell-link { color: #9bb3cc !important; text-decoration: none; font-size: 12px; font-weight: 600; }
.bell-link:hover { text-decoration: underline; }

#app-container { display: flex; gap: 24px; align-items: flex-start; justify-content: center; padding: 20px; flex: 1; }

#board-wrap { display: inline-block; border: 4px solid #333; }
#board { display: grid; grid-template-columns: repeat(8, var(--sq)); grid-template-rows: repeat(8, var(--sq)); }
.sq { width: var(--sq); height: var(--sq); display: flex; align-items: center; justify-content: center; font-size: calc(var(--sq) * 0.78); user-select: none; line-height: 1; position: relative; }
.light { background: #f0d9b5; }
.dark  { background: #b58863; }
.last  { background: #cdd26a !important; }
.white-piece { color: #fff; text-shadow: 0 0 1px #000, 0 0 2px #000, 1px 1px 1px #000; }
.black-piece { color: #000; }

#side { width: 320px; max-width: 100%; }
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

/* Stacked layout for narrow viewports — board first, controls below.
   Mirrors GameView's mobile treatment so the replay page lines up
   visually with the rest of the SPA on phones. */
@media (max-width: 1000px) {
  #app-container { flex-direction: column; align-items: center; gap: 16px; padding: 12px; }
  #side { width: 100%; max-width: 560px; }
  #moves { max-height: 220px; }
}
</style>
