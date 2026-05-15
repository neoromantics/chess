<template>
  <div v-if="error" class="error-container">
    <div class="error-box">
      <h3>Failed to load game</h3>
      <p>{{ error }}</p>
      <router-link to="/" class="btn-primary">Start a new game</router-link>
    </div>
  </div>
  <div v-else-if="!state" class="loading">Loading game...</div>
  <div v-else id="app-container">
    <ChessBoard
      :state="state"
      :flipped="flipped"
      :selected="selected"
      :hint="hint"
      @square-click="onSquare"
    />

    <div id="side">
      <h2>Game Session</h2>
      <div class="game-id">ID: {{ id }}</div>
      <div id="status">{{ statusMsg }}</div>

      <SidePanel
        :state="state"
        :white-player-type="whitePlayerType"
        :black-player-type="blackPlayerType"
        :white-think-time="whiteThinkTime"
        :black-think-time="blackThinkTime"
        :sound-enabled="soundEnabled"
        :hint-info="hintInfo"
        :history-pairs="historyPairs"
        @update:white-player-type="updatePlayerType('white', $event)"
        @update:black-player-type="updatePlayerType('black', $event)"
        @update:white-think-time="updateThinkTime('white', $event)"
        @update:black-think-time="updateThinkTime('black', $event)"
        @update:sound-enabled="setSoundEnabled"
        @new-game="newGame"
        @get-hint="getHint"
        @undo="undoMove"
        @resign="resign"
        @open-replay="openReplay"
        @toggle-flip="flipped = !flipped"
      />
    </div>

    <PromoModal
      v-if="pendingPromo"
      :turn="state?.turn"
      @select="completePromo"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import ChessBoard from '../components/ChessBoard.vue';
import SidePanel from '../components/SidePanel.vue';
import PromoModal from '../components/PromoModal.vue';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { useAuthStore } from '../stores/auth';
import { StateJSON, Square } from '../types';

const props = defineProps<{
  id: string;
}>();

const toastStore = useToastStore();
const authStore = useAuthStore();

// True once we've auto-oriented the board for the current user. Without
// this guard the orientation would re-flip on every state update,
// fighting the user if they manually toggle Flip.
let didAutoOrient = false;

// User's color in this game, or null in engine-only / spectator views.
// Computed once we've seen the first StateJSON.
const myColor = ref<'white' | 'black' | null>(null);
const state = ref<StateJSON | null>(null);
const error = ref<string | null>(null);
const selected = ref<string | null>(null);
const flipped = ref(localStorage.getItem('chess-flipped') === '1');
const soundEnabled = ref(localStorage.getItem('chess-muted') !== '1');
const hint = ref<{ from: string; to: string } | null>(null);
const hintInfo = ref('');
const pendingPromo = ref<{ from: string; to: string } | null>(null);

let ws: WebSocket | null = null;

// Players
const whitePlayerType = ref('h');
const blackPlayerType = ref('e');
const whiteThinkTime = ref(1000);
const blackThinkTime = ref(1000);

// Audio. AudioContext starts suspended until the user makes a gesture
// inside the page (browser autoplay policy). We arm it once on the
// first pointer/key event in this view so opponent moves arriving over
// WebSocket can actually play sound — calling resume() from a WS
// callback alone is silently ignored by Chrome / Safari.
let lastSoundedHistoryLen = 0;
let prevFenForSound = '';
let audioCtx: AudioContext | null = null;
let audioPrimed = false;

const ensureAudio = () => {
  if (!audioCtx) {
    try { audioCtx = new (window.AudioContext || (window as any).webkitAudioContext)(); }
    catch (e) { audioCtx = null; }
  }
  return audioCtx;
};

const primeAudio = () => {
  if (audioPrimed) return;
  const ctx = ensureAudio();
  if (!ctx) return;
  // resume() on suspended ctx only succeeds during a user gesture, so
  // this handler is the right place to do it.
  if (ctx.state === 'suspended') ctx.resume().catch(() => {});
  audioPrimed = true;
};

const playClick = (freq: number, dur: number, gain: number) => {
  if (!soundEnabled.value) return;
  const ctx = ensureAudio();
  if (!ctx) return;
  if (ctx.state === 'suspended') {
    // Likely no user gesture has happened yet. Skip the beep silently
    // rather than queuing inaudible audio nodes; the very next gesture
    // will prime us.
    return;
  }
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

const playMoveSound = (kind: string) => {
  if (kind === 'capture') playClick(220, 0.12, 0.15);
  else if (kind === 'check') { playClick(880, 0.08, 0.12); setTimeout(() => playClick(660, 0.10, 0.10), 90); }
  else playClick(520, 0.06, 0.10);
};

// Coalesce WS-triggered /api/state refetches. Two MovePlayed events
// arriving close together (engine-vs-engine) used to fire concurrent
// fetches that resolved out of order — the +2 response landing first
// confused the sound-trigger counter.
let stateFetchInFlight = false;
let stateFetchPending = false;
const refetchState = () => {
  if (stateFetchInFlight) { stateFetchPending = true; return; }
  stateFetchInFlight = true;
  api.getState(props.id).then(updateState).catch(() => {}).finally(() => {
    stateFetchInFlight = false;
    if (stateFetchPending) { stateFetchPending = false; refetchState(); }
  });
};

// Computed
const statusMsg = computed(() => {
  if (!state.value) return 'Loading…';
  const s = state.value;
  if (s.status === 'checkmate') return 'Checkmate — ' + (s.turn === 'w' ? 'Black' : 'White') + ' wins';
  if (s.status === 'stalemate') return 'Stalemate — draw';
  if (s.status === 'draw50') return 'Draw — 50-move rule';
  if (s.status === 'draw_repetition') return 'Draw — threefold repetition';
  if (s.status === 'draw_insufficient') return 'Draw — insufficient material';
  if (s.status === 'resign') {
    const winner = s.result === '1-0' ? 'White' : s.result === '0-1' ? 'Black' : '';
    return winner ? `${winner} wins by resignation` : 'Game ended by resignation';
  }
  if (s.status === 'timeout') return 'Time out — game over';

  let msg = (s.turn === 'w' ? 'White' : 'Black') + ' to move';
  if (s.thinking) msg += ' (engine thinking…)';
  else if (s.engine_to_move) msg += ' (engine’s turn)';
  if (s.in_check) msg += ' — check!';
  return msg;
});

const historyPairs = computed(() => {
  if (!state.value || !state.value.history) return [];
  const display = (state.value.history_san && state.value.history_san.length === state.value.history.length)
    ? state.value.history_san : state.value.history;
  const res: any[] = [];
  for (let i = 0; i < state.value.history.length; i += 2) {
    const pair = [{ san: display[i], lan: state.value.history[i], idx: i }];
    if (state.value.history[i+1]) {
      pair.push({ san: display[i+1], lan: state.value.history[i+1], idx: i+1 });
    }
    res.push(pair);
  }
  return res;
});

// Methods
const updateState = (s: StateJSON) => {
  state.value = s;
  whitePlayerType.value = s.engine_white ? 'e' : 'h';
  blackPlayerType.value = s.engine_black ? 'e' : 'h';
  whiteThinkTime.value = s.white_think_time;
  blackThinkTime.value = s.black_think_time;

  // First payload after mount: figure out which color the current user
  // is playing (if any) and flip the board so their pieces are on the
  // near side. After this we leave 'flipped' alone — the manual Flip
  // button keeps working.
  if (!didAutoOrient && authStore.user) {
    const me = authStore.user.id;
    if (s.white_user_id === me) myColor.value = 'white';
    else if (s.black_user_id === me) myColor.value = 'black';
    if (myColor.value === 'black') flipped.value = true;
    didAutoOrient = true;
  }

  const newLen = s.history ? s.history.length : 0;
  // Use ">" (not strict ===+1) so an out-of-order fetch resolution that
  // skips a ply still fires sound. Math.max guard prevents a stale
  // earlier response from rewinding the counter and double-sounding.
  if (newLen > lastSoundedHistoryLen) {
    let kind = 'move';
    if (s.in_check) kind = 'check';
    else {
      const count = (f: string) => f.split(' ')[0].split('').filter(c => c >= 'A' && c !== '/').length;
      if (count(prevFenForSound) > count(s.fen)) kind = 'capture';
    }
    playMoveSound(kind);
  }
  lastSoundedHistoryLen = Math.max(lastSoundedHistoryLen, newLen);
  prevFenForSound = s.fen;
};

const connectWS = () => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const apiBase = (import.meta.env.VITE_API_URL as string) || '';
  const host = apiBase.replace(/^https?:\/\//, '') || window.location.host;
  const url = `${protocol}//${host}/ws?game_id=${props.id}`;

  ws = new WebSocket(url);
  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      // Backend emits a few different event types over game.evt.{id}:
      //   - 'state' / 'StateUpdated'  full StateJSON snapshot (every HTTP
      //                                mutation in cmd/game/handlers.go)
      //   - 'MovePlayed'              delta event from the engine-result
      //                                consumer path; payload is partial
      //   - 'GameStarted'             new game row was written; no state
      //                                snapshot in the payload
      // For the delta/no-payload variants we re-fetch /api/state, which
      // gives us the canonical full snapshot.
      if (data.type === 'state' || data.type === 'StateUpdated') {
        updateState(data.payload);
      } else if (data.type === 'MovePlayed' || data.type === 'GameStarted') {
        refetchState();
      } else if (data.type === 'hint') {
        onHintReceived(data.payload);
      }
    } catch (e) {
      console.error('Failed to parse WS message', e);
    }
  };
  ws.onclose = () => {
    setTimeout(connectWS, 3000);
  };
};

const onHintReceived = (data: any) => {
  if (data) {
    hint.value = { from: data.from, to: data.to };
    hintInfo.value = `Hint: ${data.from}→${data.to}${data.promo ? '=' + data.promo.toUpperCase() : ''} (${data.score}, depth ${data.depth})`;
  } else {
    hint.value = null;
    hintInfo.value = 'No move available.';
  }
};

const resign = async () => {
  if (!confirm('Are you sure you want to resign?')) return;
  try {
    const res = await api.resign(props.id);
    updateState(res);
    toastStore.info('You resigned.');
  } catch (e) {
    toastStore.error('Failed to resign');
  }
};

const onSquare = async (sq: Square) => {
  if (!state.value || state.value.thinking || state.value.engine_to_move || state.value.status !== 'ongoing') return;
  // In a PvP game, ignore clicks while it's the opponent's turn. The
  // backend rejects mismatched moves with 409 "it is not your turn"
  // but silent-no-op is the better UX.
  if (myColor.value !== null) {
    const myFirst = myColor.value === 'white' ? 'w' : 'b';
    if (state.value.turn !== myFirst) return;
  }

  if (selected.value === sq.name) { selected.value = null; return; }
  if (state.value.legal_moves.some(m => m.startsWith(sq.name))) { selected.value = sq.name; return; }
  if (selected.value) {
    const cands = state.value.legal_moves.filter(m => m.substring(0,2) === selected.value && m.substring(2,4) === sq.name);
    if (cands.length === 1 && cands[0].length === 4) {
      const mv = cands[0];
      selected.value = null;
      sendMove(mv);
    } else if (cands.length > 1) {
      pendingPromo.value = { from: selected.value, to: sq.name };
      selected.value = null;
    } else {
      selected.value = null;
    }
  }
};

const sendMove = async (mv: string) => {
  hint.value = null;
  hintInfo.value = '';
  try {
    updateState(await api.move(props.id, mv));
  } catch (e: any) {
    toastStore.error(e.message);
  }
};

const completePromo = (promo: string) => {
  if (pendingPromo.value) {
    const mv = pendingPromo.value.from + pendingPromo.value.to + promo;
    pendingPromo.value = null;
    sendMove(mv);
  }
};

const newGame = async () => {
  try {
    const fresh = await api.newGame(props.id, whitePlayerType.value === 'e', blackPlayerType.value === 'e');
    // Reset the sound counter BEFORE updateState fires. After New Game
    // history.length goes back to 0; without this reset, the
    // "newLen > lastSoundedHistoryLen" guard stays false until enough
    // moves accumulate to exceed the previous game's count, leaving
    // the next several plies silent.
    lastSoundedHistoryLen = 0;
    prevFenForSound = fresh.fen;
    updateState(fresh);
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    toastStore.success('Game reset');
  } catch (e: any) {
    toastStore.error(e.message);
  }
};

const undoMove = async () => {
  try {
    const after = await api.undo(props.id);
    // Same reasoning as newGame: history shrank, so the sound counter
    // needs to drop with it or the next move(s) won't beep.
    lastSoundedHistoryLen = after.history?.length ?? 0;
    prevFenForSound = after.fen;
    updateState(after);
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    toastStore.info('Move undone');
  } catch (e: any) {
    toastStore.error(e.message);
  }
};

const getHint = async () => {
  if (!state.value) return;
  hintInfo.value = 'thinking…';
  const movetime = state.value.turn === 'w' ? whiteThinkTime.value : blackThinkTime.value;
  try {
    await api.getHint(props.id, movetime);
    // Result will be handled by WebSocket event
  } catch (e: any) {
    hintInfo.value = '';
    toastStore.error('Hint failed: ' + e.message);
  }
};

const updateSettings = async () => {
  // Both /api/set_players (durable) and /api/temp/set_players take the
  // same shape; api.setPlayers routes by ID prefix.
  try {
    updateState(await api.setPlayers(
      props.id,
      whitePlayerType.value === 'e',
      blackPlayerType.value === 'e',
      whiteThinkTime.value,
      blackThinkTime.value
    ));
  } catch (e: any) {
    toastStore.error('Failed to update settings');
  }
};

const updatePlayerType = async (side: 'white' | 'black', type: string) => {
  if (side === 'white') whitePlayerType.value = type;
  else blackPlayerType.value = type;
  updateSettings();
};

const updateThinkTime = async (side: 'white' | 'black', time: number) => {
  if (side === 'white') whiteThinkTime.value = time;
  else blackThinkTime.value = time;
  updateSettings();
};

const setSoundEnabled = (val: boolean) => {
  soundEnabled.value = val;
  localStorage.setItem('chess-muted', val ? '0' : '1');
};

const openReplay = () => { window.open(`/api/replay.html?game_id=${props.id}`, '_blank'); };

watch(flipped, (val) => localStorage.setItem('chess-flipped', val ? '1' : '0'));

const onKeyDown = (e: KeyboardEvent) => {
  primeAudio();
  const tag = (e.target && (e.target as any).tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  if (e.key === 'f' || e.key === 'F') flipped.value = !flipped.value;
};

onMounted(async () => {
  try {
    const s = await api.getState(props.id);
    lastSoundedHistoryLen = s.history ? s.history.length : 0;
    prevFenForSound = s.fen;
    updateState(s);
    connectWS();
  } catch (e: any) {
    error.value = e.message || 'Unknown error';
  }

  window.addEventListener('keydown', onKeyDown);
  window.addEventListener('pointerdown', primeAudio);
  window.addEventListener('touchstart', primeAudio);
});

onUnmounted(() => {
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
  window.removeEventListener('keydown', onKeyDown);
  window.removeEventListener('pointerdown', primeAudio);
  window.removeEventListener('touchstart', primeAudio);
});
</script>

<style scoped>
#app-container { display: flex; gap: 32px; align-items: flex-start; justify-content: center; padding: 32px 20px; min-height: calc(100vh - 64px); width: 100%; }

@media (max-width: 1000px) {
  #side { display: none; }
  #app-container { padding: 10px; gap: 0; }
}

#side { width: 340px; }
#side h2 { margin: 0 0 4px; font-size: 22px; letter-spacing: 0.5px; }
.game-id { font-size: 10px; color: #666; margin-bottom: 12px; }
#status { font-size: 16px; font-weight: 600; margin-bottom: 16px; padding: 10px 12px; background: #2b2b2b; border-radius: 4px; min-height: 22px; border-left: 3px solid #4a6b8a; }

.loading { display: flex; align-items: center; justify-content: center; height: calc(100vh - 64px); font-size: 18px; color: #888; }

.error-container { display: flex; align-items: center; justify-content: center; height: calc(100vh - 64px); padding: 20px; }
.error-box { background: #2b2b2b; padding: 40px; border-radius: 12px; text-align: center; max-width: 400px; box-shadow: 0 10px 30px rgba(0,0,0,0.5); }
.error-box h3 { color: #ff7575; margin: 0 0 16px; }
.error-box p { color: #aaa; margin-bottom: 24px; line-height: 1.5; }
</style>
