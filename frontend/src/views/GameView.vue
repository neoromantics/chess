<template>
  <div v-if="error" class="error-container">
    <div class="error-box">
      <h3>Failed to load game</h3>
      <p>{{ error }}</p>
      <router-link to="/" class="btn-primary">Back to Dashboard</router-link>
    </div>
  </div>
  <div v-else-if="!state" class="loading">Loading game...</div>
  <div v-else id="app-container" :class="{ editing: editMode }">
    <ChessBoard 
      :state="state"
      :edit-mode="editMode"
      :edit-board="editBoard"
      :edit-picked-up="editPickedUp"
      :flipped="flipped"
      :selected="selected"
      :hint="hint"
      @square-click="onSquare"
      @square-right-click="onRightClick"
    />

    <div id="side">
      <h2>Game Session</h2>
      <div class="game-id">ID: {{ id }}</div>
      <div id="status">{{ statusMsg }}</div>

      <SidePanel 
        :state="state"
        :edit-mode="editMode"
        :white-player-type="whitePlayerType"
        :black-player-type="blackPlayerType"
        :white-think-time="whiteThinkTime"
        :black-think-time="blackThinkTime"
        :touch-move-enabled="touchMoveEnabled"
        :auto-assess="autoAssess"
        :sound-enabled="soundEnabled"
        :hint-info="hintInfo"
        :assess-info="assessInfo"
        :assess-color="assessColor"
        :history-pairs="historyPairs"
        :fen-input="fenInput"
        :draw-offer-pending="drawOfferPending"
        :takeback-offer-pending="takebackOfferPending"
        @update:white-player-type="updatePlayerType('white', $event)"
        @update:black-player-type="updatePlayerType('black', $event)"
        @update:white-think-time="updateThinkTime('white', $event)"
        @update:black-think-time="updateThinkTime('black', $event)"
        @update:touch-move="setTouchMove"
        @update:auto-assess="autoAssess = $event"
        @update:sound-enabled="setSoundEnabled"
        @update:fen-input="fenInput = $event"
        @new-game="newGame"
        @get-hint="getHint"
        @run-assess="runAssess"
        @undo="undoMove"
        @resign="resign"
        @offer-draw="offerDraw"
        @accept-draw="acceptDraw"
        @decline-draw="declineDraw"
        @offer-takeback="offerTakeback"
        @accept-takeback="acceptTakeback"
        @decline-takeback="declineTakeback"
        @open-replay="openReplay"
        @toggle-flip="flipped = !flipped"
        @save-game="saveGame"
        @load-game="loadGameFile"
        @load-fen="loadFen"
        @toggle-edit="toggleEditMode"
      />

      <EditPanel 
        v-if="editMode"
        :edit-palette="editPalette"
        :edit-turn="editTurn"
        :edit-castling="editCastling"
        @update:edit-palette="setEditPalette"
        @update:edit-turn="editTurn = $event"
        @clear="editClear"
        @start-pos="editStartPos"
        @apply="editApply"
        @cancel="editCancel"
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
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue';
import ChessBoard from '../components/ChessBoard.vue';
import SidePanel from '../components/SidePanel.vue';
import EditPanel from '../components/EditPanel.vue';
import PromoModal from '../components/PromoModal.vue';
import { PIECE, ASSESS_COLORS, ASSESS_SYMBOL, parseBoard } from '../constants';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { StateJSON, Square } from '../types';

const props = defineProps<{
  id: string;
}>();

const toastStore = useToastStore();
const state = ref<StateJSON | null>(null);
const error = ref<string | null>(null);
const selected = ref<string | null>(null);
const flipped = ref(localStorage.getItem('chess-flipped') === '1');
const soundEnabled = ref(localStorage.getItem('chess-muted') !== '1');
const autoAssess = ref(false);
const assessments = reactive<Record<number, any>>({});
const drawOfferPending = ref(false);
const takebackOfferPending = ref(false);
const hint = ref<{ from: string; to: string } | null>(null);
const hintInfo = ref('');
const assessInfo = ref('');
const assessColor = ref('#ddd');
const fenInput = ref('');
const pendingPromo = ref<{ from: string; to: string } | null>(null);

let ws: WebSocket | null = null;

// Players
const whitePlayerType = ref('h');
const blackPlayerType = ref('e');
const whiteThinkTime = ref(1000);
const blackThinkTime = ref(1000);

// Editor
const editMode = ref(false);
const editBoard = ref<(string | null)[][] | null>(null);
const editPalette = ref('select');
const editTurn = ref('w');
const editCastling = reactive({ K: true, Q: true, k: true, q: true });
const editPickedUp = ref<{ r: number; f: number; pc: string } | null>(null);

// Audio
let lastSoundedHistoryLen = 0;
let prevFenForSound = '';
let audioCtx: AudioContext | null = null;

const ensureAudio = () => {
  if (!audioCtx) {
    try { audioCtx = new (window.AudioContext || (window as any).webkitAudioContext)(); }
    catch (e) { audioCtx = null; }
  }
  return audioCtx;
};

const playClick = (freq: number, dur: number, gain: number) => {
  if (!soundEnabled.value) return;
  const ctx = ensureAudio();
  if (!ctx) return;
  if (ctx.state === 'suspended') ctx.resume();
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

// Computed
const touchMoveEnabled = computed(() => state.value?.touch_move || false);

const statusMsg = computed(() => {
  if (editMode.value) {
    if (editPalette.value === 'select') {
      return editPickedUp.value ? 'Select: click destination to drop' : 'Select: click a piece to pick it up';
    }
    if (editPalette.value === 'delete') return 'Delete: click pieces to remove';
    return 'Paint ' + PIECE[editPalette.value] + ': click squares to place';
  }
  if (!state.value) return 'Loading…';
  const s = state.value;
  if (s.status === 'checkmate') return 'Checkmate — ' + (s.turn === 'w' ? 'Black' : 'White') + ' wins';
  if (s.status === 'stalemate') return 'Stalemate — draw';
  if (s.status === 'draw50') return 'Draw — 50-move rule';
  if (s.status === 'draw_repetition') return 'Draw — threefold repetition';
  if (s.status === 'draw_insufficient') return 'Draw — insufficient material';
  if (s.status === 'touch_lost') return 'Touch-move loss — ' + (s.turn === 'w' ? 'Black' : 'White') + ' wins';
  
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
    const pair = [{ san: display[i], lan: state.value.history[i], assess: assessments[i], idx: i }];
    if (state.value.history[i+1]) {
      pair.push({ san: display[i+1], lan: state.value.history[i+1], assess: assessments[i+1], idx: i+1 });
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

  if (s.assessments) {
    s.assessments.forEach((a: any) => {
      assessments[a.index] = a;
    });
  }
  
  const newLen = s.history ? s.history.length : 0;
  if (newLen === lastSoundedHistoryLen + 1) {
    let kind = 'move';
    if (s.in_check) kind = 'check';
    else {
      const count = (f: string) => f.split(' ')[0].split('').filter(c => c >= 'A' && c !== '/').length;
      if (count(prevFenForSound) > count(s.fen)) kind = 'capture';
    }
    playMoveSound(kind);
  }
  lastSoundedHistoryLen = newLen;
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
      if (data.type === 'state') {
        updateState(data.payload);
      } else if (data.type === 'hint') {
        onHintReceived(data.payload);
      } else if (data.type === 'assess') {
        onAssessReceived(data.payload);
      } else if (data.type === 'draw_offered') {
        drawOfferPending.value = true;
        toastStore.info('Opponent offered a draw.');
      } else if (data.type === 'takeback_offered') {
        takebackOfferPending.value = true;
        toastStore.info('Opponent requested a takeback.');
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

const onAssessReceived = (a: any) => {
  assessments[a.index] = a;
  assessColor.value = ASSESS_COLORS[a.label] || '#ddd';
  let txt = `${a.move}: ${a.label}`;
  if (a.label !== 'Best' && a.label !== 'Brilliant') txt += ` (-${a.cp_loss}cp; best ${a.best_move})`;
  else if (a.label === 'Brilliant') txt += ` (+${-a.cp_loss}cp vs engine pick ${a.best_move})`;
  else txt += ` (${a.user_score})`;
  assessInfo.value = txt;
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

const offerDraw = async () => {
  try {
    await api.offerDraw(props.id);
    toastStore.success('Draw offered to opponent');
  } catch (e) {
    toastStore.error('Failed to offer draw');
  }
};

const acceptDraw = async () => {
  try {
    const res = await api.acceptDraw(props.id);
    updateState(res);
    drawOfferPending.value = false;
    toastStore.info('Draw accepted.');
  } catch (e) {
    toastStore.error('Failed to accept draw');
  }
};

const declineDraw = async () => {
  try {
    await api.declineDraw(props.id);
    drawOfferPending.value = false;
    toastStore.info('Draw declined.');
  } catch (e) {
    toastStore.error('Failed to decline draw');
  }
};

const offerTakeback = async () => {
  try {
    await api.offerTakeback(props.id);
    toastStore.success('Takeback request sent to opponent');
  } catch (e) {
    toastStore.error('Failed to request takeback');
  }
};

const acceptTakeback = async () => {
  try {
    const res = await api.acceptTakeback(props.id);
    updateState(res);
    takebackOfferPending.value = false;
    toastStore.info('Takeback accepted.');
  } catch (e) {
    toastStore.error('Failed to accept takeback');
  }
};

const declineTakeback = async () => {
  try {
    await api.declineTakeback(props.id);
    takebackOfferPending.value = false;
    toastStore.info('Takeback declined.');
  } catch (e) {
    toastStore.error('Failed to decline takeback');
  }
};

const onSquare = async (sq: Square) => {
  if (editMode.value && editBoard.value) {
    const pc = editBoard.value[sq.r][sq.f];
    if (editPalette.value === 'select') {
      if (editPickedUp.value) {
        const src = editPickedUp.value;
        editBoard.value[src.r][src.f] = null;
        editBoard.value[sq.r][sq.f] = src.pc;
        editPickedUp.value = null;
      } else if (pc) {
        editPickedUp.value = { r: sq.r, f: sq.f, pc };
      }
    } else if (editPalette.value === 'delete') {
      editBoard.value[sq.r][sq.f] = null;
    } else {
      editBoard.value[sq.r][sq.f] = editPalette.value;
    }
    return;
  }

  if (!state.value || state.value.thinking || state.value.engine_to_move || state.value.status !== 'ongoing') return;

  if (state.value.touch_move) {
    const touched = state.value.touched_square;
    if (touched) {
      const cands = state.value.legal_moves.filter(m => m.substring(0,2) === touched && m.substring(2,4) === sq.name);
      if (!cands.length) return;
      if (cands.length === 1 && cands[0].length === 4) sendMove(cands[0]);
      else { pendingPromo.value = { from: touched, to: sq.name }; }
    } else {
      try {
        updateState(await api.touch(props.id, sq.name));
      } catch (e: any) {
        toastStore.error(e.message);
      }
    }
    return;
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

const onRightClick = (sq: Square) => {
  if (editMode.value && editBoard.value) {
    editBoard.value[sq.r][sq.f] = null;
    if (editPickedUp.value?.r === sq.r && editPickedUp.value?.f === sq.f) editPickedUp.value = null;
  }
};

const sendMove = async (mv: string) => {
  hint.value = null;
  hintInfo.value = '';
  assessInfo.value = '';
  try {
    updateState(await api.move(props.id, mv));
    if (autoAssess.value) runAssess();
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
    updateState(await api.newGame(props.id, whitePlayerType.value === 'e', blackPlayerType.value === 'e'));
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    assessInfo.value = '';
    Object.keys(assessments).forEach(k => delete assessments[parseInt(k)]);
    toastStore.success('Game reset');
  } catch (e: any) {
    toastStore.error(e.message);
  }
};

const undoMove = async () => {
  try {
    updateState(await api.undo(props.id));
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    assessInfo.value = '';
    if (state.value) {
      delete assessments[state.value.history.length];
    }
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

const runAssess = async (idx?: number, fromHistory = false) => {
  if (!state.value) return;
  assessInfo.value = 'assessing…';
  assessColor.value = '#888';
  const movetime = Math.min(state.value.turn === 'w' ? whiteThinkTime.value : blackThinkTime.value, 800);
  try {
    await api.assess(props.id, movetime, idx);
    // Result will be handled by WebSocket event
  } catch (e: any) {
    assessInfo.value = '';
    toastStore.error('Assessment failed');
  }
};

const updateSettings = async () => {
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

const setTouchMove = async (enabled: boolean) => {
  try {
    updateState(await api.setTouchMove(props.id, enabled));
    toastStore.info(`Touch-move ${enabled ? 'enabled' : 'disabled'}`);
  } catch (e: any) {
    toastStore.error('Failed to update touch-move');
  }
};

const setSoundEnabled = (val: boolean) => {
  soundEnabled.value = val;
  localStorage.setItem('chess-muted', val ? '0' : '1');
};

const toggleEditMode = () => {
  if (!state.value) return;
  if (editMode.value) {
    editMode.value = false;
    editBoard.value = null;
    editPickedUp.value = null;
  } else {
    editMode.value = true;
    editBoard.value = parseBoard(state.value.fen);
    const parts = state.value.fen.split(' ');
    editTurn.value = parts[1] || 'w';
    const ca = parts[2] || '-';
    editCastling.K = ca.includes('K');
    editCastling.Q = ca.includes('Q');
    editCastling.k = ca.includes('k');
    editCastling.q = ca.includes('q');
    editPalette.value = 'select';
    editPickedUp.value = null;
  }
};

const setEditPalette = (val: string) => {
  editPalette.value = val;
  editPickedUp.value = null;
};

const editClear = () => { editBoard.value = Array.from({length: 8}, () => Array(8).fill(null)); };
const editStartPos = () => { editBoard.value = parseBoard("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR"); };
const editCancel = () => { editMode.value = false; };
const editApply = async () => {
  if (!editBoard.value) return;
  let boardStr = '';
  for (let r = 0; r < 8; r++) {
    let empty = 0, row = '';
    for (let f = 0; f < 8; f++) {
      const pc = editBoard.value[r][f];
      if (!pc) empty++;
      else { if (empty) { row += empty; empty = 0; } row += pc; }
    }
    if (empty) row += empty;
    boardStr += row + (r < 7 ? '/' : '');
  }
  const findOne = (p: string) => {
    if (!editBoard.value) return null;
    for (let r=0; r<8; r++) for (let f=0; f<8; f++) if (editBoard.value[r][f] === p) return {r,f};
    return null;
  };
  const wK = findOne('K'), bK = findOne('k');
  const has = (p: string, r: number, f: number) => editBoard.value && editBoard.value[r] && editBoard.value[r][f] === p;
  const valid = {
    K: editCastling.K && wK && wK.r === 7 && wK.f === 4 && has('R', 7, 7),
    Q: editCastling.Q && wK && wK.r === 7 && wK.f === 4 && has('R', 7, 0),
    k: editCastling.k && bK && bK.r === 0 && bK.f === 4 && has('r', 0, 7),
    q: editCastling.q && bK && bK.r === 0 && bK.f === 4 && has('r', 0, 0),
  };
  let castling = (valid.K?'K':'')+(valid.Q?'Q':'')+(valid.k?'k':'')+(valid.q?'q':'');
  const fen = `${boardStr} ${editTurn.value} ${castling||'-'} - 0 1`;
  try {
    updateState(await api.loadGame(props.id, {
      start_fen: fen, moves: [], 
      engine_white: whitePlayerType.value === 'e', 
      engine_black: blackPlayerType.value === 'e'
    }));
    editMode.value = false;
    toastStore.success('Position applied');
  } catch (e: any) {
    toastStore.error('Failed to apply position');
  }
};

const saveGame = () => { window.location.href = api.getSaveUrl(props.id); };
const loadGameFile = async (event: Event) => {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (!file) return;
  const text = await file.text();
  try {
    updateState(await api.loadGame(props.id, text));
    selected.value = null;
    hint.value = null;
    Object.keys(assessments).forEach(k => delete assessments[parseInt(k)]);
    toastStore.success('Game loaded');
  } catch (e: any) {
    toastStore.error('Failed to load file');
  }
  (event.target as HTMLInputElement).value = '';
};

const loadFen = async () => {
  try {
    updateState(await api.loadGame(props.id, {
      start_fen: fenInput.value.trim(), moves: [], 
      engine_white: whitePlayerType.value === 'e', 
      engine_black: blackPlayerType.value === 'e'
    }));
    fenInput.value = '';
    toastStore.success('FEN loaded');
  } catch (e: any) {
    toastStore.error('Failed to load FEN');
  }
};

const openReplay = () => { window.open(`/api/replay.html?game_id=${props.id}`, '_blank'); };

watch(flipped, (val) => localStorage.setItem('chess-flipped', val ? '1' : '0'));

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
  
  window.addEventListener('keydown', (e) => {
    const tag = (e.target && (e.target as any).tagName) || '';
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    if (e.key === 'f' || e.key === 'F') flipped.value = !flipped.value;
  });
});

onUnmounted(() => {
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
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
