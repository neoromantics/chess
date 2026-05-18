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
    <!-- Visible cue when the browser is gating audio behind a user
         gesture. Triggered by a queued sound from playMoveSound — i.e.
         the engine just moved but we have no permission to play it.
         Clicking the banner runs primeAudio which resumes the context
         (because the click IS a gesture) and flushes the parked sound. -->
    <div v-if="audioBlocked" class="audio-blocked-banner" @click="primeAudio">
      Tap to enable sound — your browser is muting moves until you
      interact with the page.
    </div>
    <!-- Board renders from displayState, which equals state.value
         except while the user is scrubbing the move list (selectedPly
         != null), at which point it returns a synthetic StateJSON
         carrying the historical frame's FEN. Live state.value keeps
         updating in the background; clicking "Live" snaps back. -->
    <ChessBoard
      :state="displayState"
      :flipped="flipped"
      :selected="selected"
      :hint="hint"
      :edit-board="editMode ? editBoard : null"
      @square-click="onSquare"
      @square-context="onSquareContext"
    />

    <div id="side" :class="{ 'side-collapsed': panelCollapsed }">
      <div class="side-header">
        <h2>Game Session</h2>
        <!-- Collapses the bulky control panel so the player can focus
             on the board. Status + clock stay visible because the
             clock is operationally critical during an ongoing game.
             Persisted to localStorage so the choice survives reloads
             and across games. -->
        <button
          class="panel-toggle"
          :title="panelCollapsed ? 'Show controls' : 'Hide controls (focus on board)'"
          @click="togglePanel"
        >{{ panelCollapsed ? '◂' : '▸' }}</button>
      </div>
      <div class="game-id">ID: {{ id }}</div>
      <div id="status">{{ statusMsg }}</div>

      <ClockDisplay :state="state" />

      <SidePanel
        v-if="!editMode && !panelCollapsed"
        :state="state"
        :game-id="id"
        :spectator="isSpectator"
        :owner="isOwner"
        :white-player-type="whitePlayerType"
        :black-player-type="blackPlayerType"
        :white-think-time="whiteThinkTime"
        :black-think-time="blackThinkTime"
        :sound-enabled="soundEnabled"
        :touch-move="touchMove"
        :hint-info="hintInfo"
        :history-pairs="historyPairs"
        :can-offer-draw="canOfferDraw && !isSpectator"
        :incoming-draw="incomingDrawFrom !== null"
        :outgoing-draw="outgoingDrawSent"
        :can-request-takeback="canRequestTakeback && !isSpectator"
        :incoming-takeback="incomingTakebackFrom !== null"
        :outgoing-takeback="outgoingTakebackSent"
        :incoming-rematch="incomingRematchFrom !== null"
        :outgoing-rematch="outgoingRematchSent"
        :can-edit-position="canEditPosition && !isSpectator"
        :pgn-download-url="pgnDownloadUrl"
        :assessments="assessments"
        :analyzing="analyzing"
        :viewer-count="viewerCount"
        :selected-ply="selectedPly"
        :scrub-enabled="scrubEnabled"
        @select-ply="onSelectPly"
        @clear-scrub="clearScrub"
        @update:white-player-type="updatePlayerType('white', $event)"
        @update:black-player-type="updatePlayerType('black', $event)"
        @update:white-think-time="updateThinkTime('white', $event)"
        @update:black-think-time="updateThinkTime('black', $event)"
        @update:sound-enabled="setSoundEnabled"
        @update:touch-move="setTouchMove"
        @new-game="newGame"
        @get-hint="getHint"
        @undo="undoMove"
        @resign="resign"
        @draw-offer="drawOffer"
        @draw-accept="drawAccept"
        @draw-decline="drawDecline"
        @takeback-offer="takebackOffer"
        @takeback-accept="takebackAccept"
        @takeback-decline="takebackDecline"
        @rematch-offer="rematchOffer"
        @rematch-accept="rematchAccept"
        @rematch-decline="rematchDecline"
        @open-replay="openReplay"
        @toggle-flip="flipped = !flipped"
        @edit-position="enterEditMode"
        @load-pgn="loadPgn"
        @analyze="analyze"
        @set-visibility="setVisibility"
      />

      <EditPanel
        v-else-if="editMode && !panelCollapsed"
        :palette="editPalette"
        :turn="editTurn"
        :castling="editCastling"
        @update:palette="editPalette = $event"
        @update:turn="editTurn = $event"
        @update:castling="editCastling = $event"
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
import { ref, computed, onMounted, onUnmounted, watch } from 'vue';
import ChessBoard from '../components/ChessBoard.vue';
import SidePanel from '../components/SidePanel.vue';
import EditPanel from '../components/EditPanel.vue';
import PromoModal from '../components/PromoModal.vue';
import ClockDisplay from '../components/ClockDisplay.vue';
import { api } from '../api';
import { parseBoard } from '../constants';
import { useToastStore } from '../stores/toast';
import { useAuthStore } from '../stores/auth';
import { useConfirmStore } from '../stores/confirm';
import { useUserEventsStore } from '../stores/userEvents';
import { useRouter, useRoute } from 'vue-router';
import { StateJSON, Square, ReplayFrame } from '../types';

const props = defineProps<{
  id: string;
  // True when the route is /watch/:id. The board is read-only and
  // all mutation calls are short-circuited client-side (backend
  // enforces the same via userOwnsGame, but suppressing the buttons
  // is the friendlier UX). Also enabled at runtime when we land on
  // /game/:id and discover the caller isn't a participant — that's
  // possible if a logged-in user opens a friend's public game by
  // typing the URL.
  spectator?: boolean;
}>();

const toastStore = useToastStore();
const authStore = useAuthStore();
const confirmStore = useConfirmStore();
const userEventsStore = useUserEventsStore();
const router = useRouter();
const route = useRoute();

// True once we've auto-oriented the board for the current user. Without
// this guard the orientation would re-flip on every state update,
// fighting the user if they manually toggle Flip.
let didAutoOrient = false;

// Highest snapshot Rev applied so far. Drops late-arriving stale
// updates (HTTP response landing after a WS event that was already
// for a newer state) — the classic engine-toggle "board reverts to
// pre-move position" race. Reset when navigating to a different game.
let lastSnapshotRev = 0;

// User's color in this game, or null in engine-only / spectator views.
// Computed once we've seen the first StateJSON.
const myColor = ref<'white' | 'black' | null>(null);

// True for /watch/:id and for signed-in users who happen to load a
// public game where they aren't a participant. Drives the read-only
// UI: no action buttons, no click handlers, no input modals. Backend
// enforces the same via userOwnsGame on every mutation endpoint, so
// this is purely a UX layer.
const isSpectator = computed(() => {
  if (props.spectator) return true;
  if (!state.value) return false;
  const me = authStore.user?.id;
  if (!me) return state.value.white_user_id !== null || state.value.black_user_id !== null;
  return state.value.white_user_id !== me && state.value.black_user_id !== me;
});
// Owner-only controls (visibility toggle, settings) gate on this.
// Spectators can see the snapshot but can't change the game.
const isOwner = computed(() => !isSpectator.value && (state.value?.white_user_id !== null || state.value?.black_user_id !== null));
const state = ref<StateJSON | null>(null);
const error = ref<string | null>(null);
const selected = ref<string | null>(null);
const flipped = ref(localStorage.getItem('chess-flipped') === '1');
const soundEnabled = ref(localStorage.getItem('chess-muted') !== '1');
const touchMove = ref(localStorage.getItem('chess-touch-move') === '1');
// Cross-game user preference: hide the SidePanel/EditPanel so the
// board takes more visual real estate. Status + clock stay rendered
// (clock is operationally critical during ongoing play). Persisted
// to localStorage so reloads + game-to-game navigation keep the
// chosen layout.
const panelCollapsed = ref(localStorage.getItem('chess-panel-collapsed') === '1');
const togglePanel = () => {
  panelCollapsed.value = !panelCollapsed.value;
  localStorage.setItem('chess-panel-collapsed', panelCollapsed.value ? '1' : '0');
};
const hint = ref<{ from: string; to: string } | null>(null);
const hintInfo = ref('');
const pendingPromo = ref<{ from: string; to: string } | null>(null);

let ws: WebSocket | null = null;

// === Move-list scrub state ===
// selectedPly === null → board shows live state.value as usual.
// selectedPly === k    → board shows the position AFTER move k (k=0
//                        is the starting position). Live updates keep
//                        flowing into state.value in the background;
//                        when the user clicks "Live" / hits Esc we
//                        snap back to it.
// Only enabled when status !== 'ongoing'. The board's click-to-move
// handler is already gated on that status so phantom move sends are
// impossible while scrubbing.
const selectedPly = ref<number | null>(null);
const replayFrames = ref<ReplayFrame[] | null>(null);
let replayFramesGameID: string | null = null;
let replayFramesPlies = 0;
const scrubEnabled = computed(() => !!state.value && state.value.status !== 'ongoing');
// Re-render the board from a historical frame by stitching its fen +
// last-move into the rest of the live state. legal_moves cleared so
// any stray click can't synthesise a move; `thinking` cleared so the
// spinner doesn't bleed into scrub.
const displayState = computed((): StateJSON | null => {
  if (!state.value) return null;
  if (selectedPly.value === null) return state.value;
  if (!replayFrames.value) return state.value;
  const frame = replayFrames.value[selectedPly.value];
  if (!frame) return state.value;
  return {
    ...state.value,
    fen: frame.fen,
    last_move: (frame.from && frame.to) ? { from: frame.from, to: frame.to } : null,
    legal_moves: [],
    in_check: false,
    thinking: false,
  } as StateJSON;
});

// Players
const whitePlayerType = ref('h');
const blackPlayerType = ref('e');
const whiteThinkTime = ref(1000);
const blackThinkTime = ref(1000);

// Draw-offer transient UI state. Cleared on game end, decline, or
// accept. Server is the source of truth (ephemeral Redis key); we just
// mirror its broadcasts here so the SPA renders the right banner.
const incomingDrawFrom = ref<number | null>(null); // user_id of offerer
const outgoingDrawSent = ref(false);
const incomingTakebackFrom = ref<number | null>(null);
const outgoingTakebackSent = ref(false);
// Rematch transient state. Cleared when the user navigates away to the
// new game's room (rematch_accepted → router.push), or on decline.
const incomingRematchFrom = ref<number | null>(null);
const outgoingRematchSent = ref(false);

// Live spectator count, pushed by the gateway hub via WS `viewer_count`
// events. Includes the caller themselves; SPA shows raw N for honesty
// rather than trying to subtract the player(s) we'd need per-client
// metadata to know about.
const viewerCount = ref<number>(0);
const canOfferDraw = computed(() => {
  // PvP-only, ongoing, and we're not the offerer waiting for a response.
  if (!state.value || state.value.status !== 'ongoing') return false;
  if (state.value.white_user_id === null || state.value.black_user_id === null) return false;
  return true;
});
const canRequestTakeback = computed(() => {
  // PvP, casual, ongoing, and we've actually played a move.
  if (!state.value || state.value.status !== 'ongoing') return false;
  if (state.value.white_user_id === null || state.value.black_user_id === null) return false;
  if (state.value.rated) return false;
  if (!state.value.history || state.value.history.length === 0) return false;
  return true;
});

// Board editor — engine games only (server enforces the same; the
// button just stays hidden in PvP). Setting up a position wipes
// history and restarts the engine if it's the engine's turn in the
// new setup. See cmd/game/editor.go for the backend contract.
const canEditPosition = computed(() => {
  if (!state.value) return false;
  return state.value.white_user_id === null || state.value.black_user_id === null;
});
const pgnDownloadUrl = computed(() => api.pgnDownloadUrl(props.id));

// Move-assessment results stream in over WS after the user hits
// "Analyze". Keyed by ply index. Cleared on every state mutation that
// changes the history (new game / undo / load PGN) since the indices
// no longer refer to the same moves.
type Assessment = { ply: number; played: string; best: string; score: number; depth: number; cp_loss: number; class: string };
const assessments = ref<Record<number, Assessment>>({});
const analyzing = ref(false);
const editMode = ref(false);
const editBoard = ref<(string | null)[][] | null>(null);
const editPalette = ref('P');
const editTurn = ref('w');
const editCastling = ref<Record<string, boolean>>({ K: true, Q: true, k: true, q: true });

// Audio. AudioContext starts suspended until the user makes a gesture
// inside the page (browser autoplay policy). We arm it once on the
// first pointer/key event in this view so opponent moves arriving over
// WebSocket can actually play sound — calling resume() from a WS
// callback alone is silently ignored by Chrome / Safari.
let lastSoundedHistoryLen = 0;
let prevFenForSound = '';
let audioCtx: AudioContext | null = null;
// When a move sound is requested while the AudioContext is still
// suspended (common right after a page refresh — the engine reply lands
// before the user has clicked anything), we stash the latest kind here
// and replay it on the first successful resume(). Reactive so the
// "Tap to enable sound" banner shows up the moment a parked sound
// proves audio is blocked, and disappears on the next successful
// flush. Without the visible cue, a user who just sits and watches
// (engine-vs-engine, or a refresh during the engine's turn) gets
// silent moves and assumes sound is broken — even though pendingSound
// is sitting there waiting for a click.
const pendingSound = ref<string | null>(null);

const ensureAudio = () => {
  if (!audioCtx) {
    try { audioCtx = new (window.AudioContext || (window as any).webkitAudioContext)(); }
    catch (e) { audioCtx = null; }
  }
  return audioCtx;
};

const flushPendingSound = () => {
  if (!pendingSound.value) return;
  const kind = pendingSound.value;
  pendingSound.value = null;
  playMoveSound(kind);
};

const primeAudio = () => {
  const ctx = ensureAudio();
  if (!ctx) return;
  if (ctx.state === 'running') {
    flushPendingSound();
    return;
  }
  // resume() on suspended ctx only succeeds during a user gesture, so
  // this handler is the right place to do it. Keep retrying on every
  // gesture until it actually transitions to running — synchronous
  // "primed" flag was lying when an early resume() rejected.
  if (ctx.state === 'suspended') {
    ctx.resume().then(flushPendingSound).catch(() => {});
  }
};

// Surfaces "Tap to enable sound" UI: true iff a sound was queued and
// the browser is still gating us behind a gesture.
const audioBlocked = computed(() => pendingSound.value !== null && soundEnabled.value);

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
  if (!soundEnabled.value) return;
  const ctx = ensureAudio();
  // If audio is still gated (no user gesture since load/refresh), park
  // the latest sound and let primeAudio flush it once resume() succeeds.
  // Last-write-wins is intentional: only the most recent move is worth
  // catching up on once the speaker comes alive.
  if (ctx && ctx.state === 'suspended') {
    pendingSound.value = kind;
    return;
  }
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

// Periodic poll while the engine is on the clock. The WS is the
// primary delivery channel for engine moves, but in two failure modes
// it's insufficient on its own:
//   - WS stays open while the engine search dies silently. The lazy
//     retrigger in handleState only fires when /api/state is hit, so
//     without a poll the SPA would sit on `thinking=true` forever.
//   - A WS event lands during a transient reconnect we don't observe.
// Polling every 4s while engine_to_move OR thinking is true bounds
// the worst-case stuck window to ~4s; clears as soon as a human-to-
// move snapshot lands. Pulses through the same coalescer above so we
// don't stack concurrent fetches.
let enginePollHandle: number | undefined;
const enginePollIntervalMs = 4000;
const ensureEnginePoll = () => {
  const s = state.value;
  const active = !!s && (s.thinking || s.engine_to_move) && s.status === 'ongoing';
  if (active) {
    if (enginePollHandle) return;
    enginePollHandle = window.setInterval(refetchState, enginePollIntervalMs);
  } else if (enginePollHandle) {
    clearInterval(enginePollHandle);
    enginePollHandle = undefined;
  }
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
  if (s.status === 'timeout') {
    const winner = s.result === '1-0' ? 'White' : s.result === '0-1' ? 'Black' : '';
    return winner ? `${winner} wins on time` : 'Time out — game over';
  }
  if (s.status === 'draw_agreement') return 'Draw by agreement';

  let msg = (s.turn === 'w' ? 'White' : 'Black') + ' to move';
  // Only label the wait as "engine thinking…" when the SPA actually
  // sees an engine on the board. Bot-fallback games mask the engine
  // flags (see snapshotFromRecord + cmd/game/bots.go), so this falls
  // through to "opponent thinking…" — which is also the truthful read
  // for the user as far as they're concerned.
  const enginePresent = s.engine_white || s.engine_black;
  if (s.thinking) msg += enginePresent ? ' (engine thinking…)' : ' (opponent thinking…)';
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
  // Drop stale snapshots. Rev is set server-side from rec.UpdatedAt
  // and is strictly increasing per game row (the per-game lock
  // serializes writes). Without this, a fast WS StateUpdated event
  // could land before the in-flight HTTP response for an earlier
  // state, and updateState would happily overwrite the newer board
  // with the older one. Legacy snapshots without a rev (rev === 0
  // or undefined) skip the check — better to apply than to silently
  // miss state on a partial-deploy mismatch.
  if (s.rev && s.rev <= lastSnapshotRev) return;
  if (s.rev) lastSnapshotRev = s.rev;

  state.value = s;
  whitePlayerType.value = s.engine_white ? 'e' : 'h';
  blackPlayerType.value = s.engine_black ? 'e' : 'h';
  whiteThinkTime.value = s.white_think_time;
  blackThinkTime.value = s.black_think_time;

  // Hydrate persisted assessments. Backend only sends `s.assessments`
  // when its length matches the move list (stale rows are filtered
  // server-side), so we can blindly overwrite — a missing field means
  // "no analysis on file" and we leave the in-memory map alone for
  // the user to refresh via the Analyze button. The user-triggered
  // analyze flow always clears assessments.value first (see resetters
  // below), so we won't merge stale streaming results into hydrated
  // persisted ones.
  if (s.assessments && s.assessments.length > 0) {
    const next: Record<number, Assessment> = {};
    for (const a of s.assessments) next[a.ply] = a;
    assessments.value = next;
  }

  // First payload after mount: figure out which color the current user
  // is playing (if any) and orient the board so their pieces are on the
  // near side. After this we leave 'flipped' alone — the manual Flip
  // button keeps working.
  //
  // Assign unconditionally (not "only flip to true if black"): the
  // previous version inherited whatever flipped was in localStorage, so
  // a user who played black last game then opened a new game as white
  // saw the board from black's side until they flipped manually.
  if (!didAutoOrient && authStore.user) {
    const me = authStore.user.id;
    if (s.white_user_id === me) myColor.value = 'white';
    else if (s.black_user_id === me) myColor.value = 'black';
    if (myColor.value !== null) {
      flipped.value = myColor.value === 'black';
    }
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

  // Any terminal status invalidates pending draw banners — the server
  // also deletes its draw-offer:{id} key on finish, but the SPA needs
  // to clear its own mirrors so reload doesn't show ghost prompts.
  if (s.status !== 'ongoing') {
    incomingDrawFrom.value = null;
    outgoingDrawSent.value = false;
    incomingTakebackFrom.value = null;
    outgoingTakebackSent.value = false;
  }

  ensureEnginePoll();
};

const connectWS = () => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const apiBase = (import.meta.env.VITE_API_URL as string) || '';
  const host = apiBase.replace(/^https?:\/\//, '') || window.location.host;
  const url = `${protocol}//${host}/ws?game_id=${props.id}`;

  ws = new WebSocket(url);
  ws.onopen = () => {
    // Re-fetch state on every successful connect, not just the initial
    // one. /ws and /api/state are routed independently; if a
    // StateUpdated event landed during the 3s reconnect window (e.g.
    // the engine moved while the SPA was offline), the per-game
    // pub/sub channel had no subscriber and the event was dropped.
    // The next state-poll catches us up.
    refetchState();
  };
  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      // Backend emits a few different event types over game.evt.{id}:
      //   - 'StateUpdated'            full StateJSON snapshot (every HTTP
      //                                mutation in cmd/game/handlers.go)
      //   - 'MovePlayed'              delta event from the engine-result
      //                                consumer path; payload is partial
      //   - 'GameStarted'             new game row was written; no state
      //                                snapshot in the payload
      // For the delta/no-payload variants we re-fetch /api/state, which
      // gives us the canonical full snapshot.
      if (data.type === 'StateUpdated') {
        updateState(data.payload);
      } else if (data.type === 'MovePlayed' || data.type === 'GameStarted') {
        refetchState();
      } else if (data.type === 'hint') {
        onHintReceived(data.payload);
      } else if (data.type === 'DrawOffered') {
        // Show the prompt on the OPPONENT's view; the offerer already
        // sees the "Draw offer sent — waiting" banner via outgoingDrawSent.
        const fromId = data.payload?.from_user_id;
        if (authStore.user && fromId && fromId !== authStore.user.id) {
          incomingDrawFrom.value = fromId;
        }
      } else if (data.type === 'DrawDeclined') {
        // Either we declined someone else's offer, or our outgoing
        // offer was declined. Clear both bits and surface a toast for
        // the declined side.
        if (outgoingDrawSent.value) {
          toastStore.info('Opponent declined the draw.');
        }
        incomingDrawFrom.value = null;
        outgoingDrawSent.value = false;
      } else if (data.type === 'DrawAccepted') {
        // Game ends. StateUpdated with status=draw_agreement arrives
        // alongside; clear local banner state and let the snapshot
        // update render the terminal status.
        incomingDrawFrom.value = null;
        outgoingDrawSent.value = false;
      } else if (data.type === 'TakebackRequested') {
        const fromId = data.payload?.from_user_id;
        if (authStore.user && fromId && fromId !== authStore.user.id) {
          incomingTakebackFrom.value = fromId;
        }
      } else if (data.type === 'TakebackDeclined') {
        if (outgoingTakebackSent.value) {
          toastStore.info('Opponent declined the takeback.');
        }
        incomingTakebackFrom.value = null;
        outgoingTakebackSent.value = false;
      } else if (data.type === 'TakebackAccepted') {
        // StateUpdated rolls in alongside; just clear the banner state.
        incomingTakebackFrom.value = null;
        outgoingTakebackSent.value = false;
      } else if (data.type === 'RematchOffered') {
        const fromId = data.payload?.from_user_id;
        if (authStore.user && fromId && fromId !== authStore.user.id) {
          incomingRematchFrom.value = fromId;
        }
      } else if (data.type === 'RematchDeclined') {
        if (outgoingRematchSent.value) {
          toastStore.info('Opponent declined the rematch.');
        }
        incomingRematchFrom.value = null;
        outgoingRematchSent.value = false;
      } else if (data.type === 'RematchAccepted') {
        // Both participants of the old game navigate to the new room.
        // Spectators (anyone not in {white_user_id, black_user_id}) get
        // a passive toast instead — they can refresh if they want to
        // watch the new game.
        const newId = data.payload?.new_game_id;
        const w = data.payload?.white_user_id;
        const b = data.payload?.black_user_id;
        const me = authStore.user?.id;
        incomingRematchFrom.value = null;
        outgoingRematchSent.value = false;
        if (newId && me && (me === w || me === b)) {
          router.push(`/game/${newId}`);
        } else if (newId) {
          toastStore.info('Players started a rematch.');
        }
      } else if (data.type === 'viewer_count') {
        // Pushed by the gateway hub on every WS subscribe/unsubscribe
        // for this channel. Count is players + spectators (the per-game
        // pub/sub layer doesn't distinguish them).
        const n = data.payload?.count;
        if (typeof n === 'number' && n >= 0) viewerCount.value = n;
      } else if (data.type === 'Assessment') {
        const a = data.payload as Assessment;
        assessments.value[a.ply] = a;
        // analyzing stays "true" until we've received as many results
        // as the move list length; SidePanel uses it to disable the
        // button + show a counter.
        if (state.value?.history && Object.keys(assessments.value).length >= state.value.history.length) {
          analyzing.value = false;
        }
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
  const ok = await confirmStore.ask({
    title: 'Resign',
    message: 'Are you sure you want to resign? Your opponent will be awarded the win.',
    confirmLabel: 'Resign',
    danger: true,
  });
  if (!ok) return;
  try {
    const res = await api.resign(props.id);
    updateState(res);
    toastStore.info('You resigned.');
  } catch (e) {
    toastStore.error('Failed to resign');
  }
};

const drawOffer = async () => {
  try {
    await api.drawOffer(props.id);
    outgoingDrawSent.value = true;
    toastStore.info('Draw offer sent.');
  } catch (e: any) {
    toastStore.error('Could not offer draw: ' + (e?.message || e));
  }
};

const drawAccept = async () => {
  try {
    const snap = await api.drawAccept(props.id);
    if (snap) updateState(snap);
    incomingDrawFrom.value = null;
    outgoingDrawSent.value = false;
    toastStore.success('Draw agreed.');
  } catch (e: any) {
    toastStore.error('Could not accept draw: ' + (e?.message || e));
  }
};

const drawDecline = async () => {
  try {
    await api.drawDecline(props.id);
    incomingDrawFrom.value = null;
    toastStore.info('Draw declined.');
  } catch (e: any) {
    toastStore.error('Could not decline draw: ' + (e?.message || e));
  }
};

const takebackOffer = async () => {
  try {
    await api.takebackOffer(props.id);
    outgoingTakebackSent.value = true;
    toastStore.info('Takeback requested.');
  } catch (e: any) {
    toastStore.error('Could not request takeback: ' + (e?.message || e));
  }
};

const takebackAccept = async () => {
  try {
    const snap = await api.takebackAccept(props.id);
    if (snap) updateState(snap);
    incomingTakebackFrom.value = null;
    outgoingTakebackSent.value = false;
    toastStore.success('Takeback granted.');
  } catch (e: any) {
    toastStore.error('Could not accept takeback: ' + (e?.message || e));
  }
};

const takebackDecline = async () => {
  try {
    await api.takebackDecline(props.id);
    incomingTakebackFrom.value = null;
    toastStore.info('Takeback declined.');
  } catch (e: any) {
    toastStore.error('Could not decline takeback: ' + (e?.message || e));
  }
};

const rematchOffer = async () => {
  try {
    await api.rematchOffer(props.id);
    outgoingRematchSent.value = true;
    toastStore.info('Rematch offer sent.');
  } catch (e: any) {
    toastStore.error('Could not offer rematch: ' + (e?.message || e));
  }
};

const rematchAccept = async () => {
  try {
    const { game_id } = await api.rematchAccept(props.id);
    incomingRematchFrom.value = null;
    outgoingRematchSent.value = false;
    // Navigate from the HTTP response so the accepter doesn't have to
    // wait on the RematchAccepted WS broadcast. The offerer takes the
    // event-driven path (same handler 30 lines up) — both land in the
    // new room within a tick of each other.
    if (game_id) router.push(`/game/${game_id}`);
  } catch (e: any) {
    toastStore.error('Could not accept rematch: ' + (e?.message || e));
  }
};

const rematchDecline = async () => {
  try {
    await api.rematchDecline(props.id);
    incomingRematchFrom.value = null;
    toastStore.info('Rematch declined.');
  } catch (e: any) {
    toastStore.error('Could not decline rematch: ' + (e?.message || e));
  }
};

const onSquare = async (sq: Square) => {
  // Edit-mode click: place/erase a piece. Bypasses every play-mode
  // guard because we're not advancing a move, we're painting the
  // setup grid.
  if (editMode.value && editBoard.value) {
    if (editPalette.value === 'delete') {
      editBoard.value[sq.r][sq.f] = null;
    } else {
      editBoard.value[sq.r][sq.f] = editPalette.value;
    }
    return;
  }
  // Spectators can hover and inspect but never move. Backend rejects
  // mutations from non-participants too; this just keeps the click
  // silent instead of showing a phantom selection.
  if (isSpectator.value) return;
  if (!state.value || state.value.thinking || state.value.engine_to_move || state.value.status !== 'ongoing') return;
  // In a PvP game, ignore clicks while it's the opponent's turn. The
  // backend rejects mismatched moves with 409 "it is not your turn"
  // but silent-no-op is the better UX.
  if (myColor.value !== null) {
    const myFirst = myColor.value === 'white' ? 'w' : 'b';
    if (state.value.turn !== myFirst) return;
  }

  // Touch-move (FIDE 4.3): if a piece on your side is touched and has
  // at least one legal move, you must move it. We enforce this on the
  // client: once selected, ignore clicks on anything that isn't one of
  // its legal destinations. The first click can still pick a different
  // piece because nothing's been "touched" yet — selection is the
  // committing action. There's no take-back: hit Undo if you fluffed.
  const hasLegalMoves = (from: string) => state.value!.legal_moves.some(m => m.startsWith(from));
  const isDestOfSelected = (from: string, to: string) =>
    state.value!.legal_moves.some(m => m.substring(0,2) === from && m.substring(2,4) === to);

  if (selected.value === sq.name) {
    if (touchMove.value) return; // can't unselect a touched piece under FIDE rules
    selected.value = null; return;
  }
  if (state.value.legal_moves.some(m => m.startsWith(sq.name))) {
    // Trying to switch pieces while one is already touched — not allowed
    // under touch-move. Without the rule, this is the normal "change
    // your mind" path.
    if (touchMove.value && selected.value && hasLegalMoves(selected.value) && !isDestOfSelected(selected.value, sq.name)) {
      return;
    }
    selected.value = sq.name; return;
  }
  if (selected.value) {
    const cands = state.value.legal_moves.filter(m => m.substring(0,2) === selected.value && m.substring(2,4) === sq.name);
    if (cands.length === 1 && cands[0].length === 4) {
      const mv = cands[0];
      selected.value = null;
      sendMove(mv);
    } else if (cands.length > 1) {
      pendingPromo.value = { from: selected.value, to: sq.name };
      selected.value = null;
    } else if (!touchMove.value) {
      // Under touch-move, an illegal-destination click is a no-op so
      // the touched piece stays selected and must still be moved.
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
  // Finished durable engine games are kept around for review (move-list
  // scrubbing, replay, analyze, PGN download). Clicking "New Game" from
  // such a game spawns a fresh game in its own row and navigates there,
  // rather than overwriting the finished one in place. Ongoing games
  // and temp games still take the legacy reset path — ongoing because
  // the user explicitly wants to abandon-and-restart this slot, temp
  // because they're already ephemeral.
  const isTemp = props.id.startsWith('temp-');
  const isFinished = !!state.value && state.value.status !== 'ongoing';
  const isPvP = !!state.value && state.value.white_user_id !== null && state.value.black_user_id !== null;
  if (isFinished && !isPvP && !isTemp) {
    try {
      const { game_id } = await api.createGame({
        engine_white: whitePlayerType.value === 'e',
        engine_black: blackPlayerType.value === 'e',
        white_think_time: whiteThinkTime.value,
        black_think_time: blackThinkTime.value,
      });
      router.push(`/game/${game_id}`);
    } catch (e: any) {
      toastStore.error(e.message);
    }
    return;
  }
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
    assessments.value = {};
    // Drop any scrub state from the previous (now-discarded) history.
    // Replay frames are tied to the old history length so they're
    // invalid the moment a reset shrinks history back to 0.
    selectedPly.value = null;
    replayFrames.value = null;
    replayFramesGameID = null;
    replayFramesPlies = 0;
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
    assessments.value = {};
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

const setTouchMove = (val: boolean) => {
  touchMove.value = val;
  localStorage.setItem('chess-touch-move', val ? '1' : '0');
  // If the rule was just enabled mid-think and the user already had a
  // piece selected (touched), keep that selection — it's now locked
  // per the new rule. No corrective action needed.
};

const openReplay = () => { window.open(`/api/replay.html?game_id=${props.id}`, '_blank'); };

const setVisibility = async (isPublic: boolean) => {
  if (!state.value || isSpectator.value) return;
  try {
    const res = await api.setVisibility(props.id, isPublic);
    state.value = { ...state.value, is_public: res.is_public };
    toastStore.info(res.is_public
      ? 'Game is now public — share /watch/' + props.id
      : 'Game is now private');
  } catch (e: any) {
    toastStore.error(e.message || 'Failed to update visibility');
  }
};

// ===== Board editor =====

const onSquareContext = (sq: Square) => {
  if (!editMode.value || !editBoard.value) return;
  editBoard.value[sq.r][sq.f] = null;
};

const enterEditMode = () => {
  if (!state.value) return;
  editBoard.value = parseBoard(state.value.fen);
  const parts = state.value.fen.split(' ');
  editTurn.value = parts[1] === 'b' ? 'b' : 'w';
  const ca = parts[2] || '';
  editCastling.value = {
    K: ca.includes('K'), Q: ca.includes('Q'),
    k: ca.includes('k'), q: ca.includes('q'),
  };
  editPalette.value = 'P';
  editMode.value = true;
};

const editCancel = () => {
  editMode.value = false;
  editBoard.value = null;
};

const editClear = () => {
  editBoard.value = Array.from({ length: 8 }, () => Array(8).fill(null));
};

const editStartPos = () => {
  editBoard.value = parseBoard('rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR');
  editTurn.value = 'w';
  editCastling.value = { K: true, Q: true, k: true, q: true };
};

const analyze = async () => {
  if (analyzing.value) return;
  try {
    assessments.value = {};
    analyzing.value = true;
    await api.analyze(props.id);
    toastStore.info('Analyzing… results will stream in.');
  } catch (e: any) {
    analyzing.value = false;
    toastStore.error('Analyze failed: ' + (e?.message || e));
  }
};

const loadPgn = async (pgn: string) => {
  try {
    const snap = await api.loadPgn(props.id, pgn);
    lastSoundedHistoryLen = snap.history?.length ?? 0;
    prevFenForSound = snap.fen;
    updateState(snap);
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    toastStore.success('PGN loaded');
  } catch (e: any) {
    toastStore.error('Failed to load PGN: ' + (e?.message || e));
  }
};

const editApply = async () => {
  if (!editBoard.value) return;
  // Build the board half of the FEN.
  let boardStr = '';
  for (let r = 0; r < 8; r++) {
    let empty = 0, row = '';
    for (let f = 0; f < 8; f++) {
      const pc = editBoard.value[r][f];
      if (!pc) { empty++; continue; }
      if (empty) { row += empty; empty = 0; }
      row += pc;
    }
    if (empty) row += empty;
    boardStr += row + (r < 7 ? '/' : '');
  }
  // Castling: validate against king/rook squares so we don't ship a
  // FEN the engine will reject.
  const findOne = (p: string) => {
    if (!editBoard.value) return null;
    for (let r = 0; r < 8; r++) for (let f = 0; f < 8; f++) {
      if (editBoard.value[r][f] === p) return { r, f };
    }
    return null;
  };
  const has = (p: string, r: number, f: number) =>
    !!(editBoard.value && editBoard.value[r] && editBoard.value[r][f] === p);
  const wK = findOne('K');
  const bK = findOne('k');
  const valid = {
    K: editCastling.value.K && wK && wK.r === 7 && wK.f === 4 && has('R', 7, 7),
    Q: editCastling.value.Q && wK && wK.r === 7 && wK.f === 4 && has('R', 7, 0),
    k: editCastling.value.k && bK && bK.r === 0 && bK.f === 4 && has('r', 0, 7),
    q: editCastling.value.q && bK && bK.r === 0 && bK.f === 4 && has('r', 0, 0),
  };
  const castling = (valid.K ? 'K' : '') + (valid.Q ? 'Q' : '') + (valid.k ? 'k' : '') + (valid.q ? 'q' : '');
  const fen = `${boardStr} ${editTurn.value} ${castling || '-'} - 0 1`;
  try {
    const snap = await api.setPosition(props.id, fen);
    lastSoundedHistoryLen = 0;
    prevFenForSound = snap.fen;
    updateState(snap);
    selected.value = null;
    hint.value = null;
    hintInfo.value = '';
    editMode.value = false;
    editBoard.value = null;
    toastStore.success('Position applied');
  } catch (e: any) {
    toastStore.error('Failed to apply position: ' + (e?.message || e));
  }
};

watch(flipped, (val) => localStorage.setItem('chess-flipped', val ? '1' : '0'));

// Reset the snapshot-rev gate when navigating between games (router
// reuses GameView with different props.id). Without this, an older
// game's high rev would block a fresh game's first snapshot.
// Also tear down the per-game WebSocket and re-fetch state — the
// router reuses the component across /game/:id transitions, so
// onMounted does NOT fire again on a rematch / match-found redirect.
// Without this re-init the user lands on the new game's URL but
// keeps the old board state and the old WS feed.
watch(() => props.id, async (newId, oldId) => {
  if (!newId || newId === oldId) return;
  lastSnapshotRev = 0;
  didAutoOrient = false;
  // Drop per-game transient UI state so the new game starts clean.
  state.value = null;
  selected.value = null;
  incomingDrawFrom.value = null;
  outgoingDrawSent.value = false;
  incomingTakebackFrom.value = null;
  outgoingTakebackSent.value = false;
  incomingRematchFrom.value = null;
  outgoingRematchSent.value = false;
  viewerCount.value = 0;
  assessments.value = {};
  analyzing.value = false;
  // Drop the scrub state too so the new game starts on its live frame.
  selectedPly.value = null;
  replayFrames.value = null;
  replayFramesGameID = null;
  replayFramesPlies = 0;
  // Close the old WS before opening the new one. Null the onclose
  // first so the auto-reconnect timer doesn't try to reopen the old
  // game's channel after we've moved on.
  if (ws) {
    ws.onclose = null;
    ws.close();
    ws = null;
  }
  if (enginePollHandle) {
    clearInterval(enginePollHandle);
    enginePollHandle = undefined;
  }
  try {
    const s = await api.getState(newId);
    lastSoundedHistoryLen = s.history ? s.history.length : 0;
    prevFenForSound = s.fen;
    updateState(s);
    connectWS();
  } catch (e: any) {
    error.value = e.message || 'Unknown error';
  }
});

const onKeyDown = (e: KeyboardEvent) => {
  primeAudio();
  const tag = (e.target && (e.target as any).tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  if (e.key === 'f' || e.key === 'F') { flipped.value = !flipped.value; return; }
  // Scrub keys only fire when scrubbing is allowed (finished games).
  // Arrows step through plies, Home/End jump to the bookends, Esc snaps
  // back to live. ply 0 is the starting position; ply N (history.length)
  // is functionally the live state of a finished game.
  if (!scrubEnabled.value) return;
  const total = state.value?.history?.length ?? 0;
  if (e.key === 'Escape') { clearScrub(); return; }
  if (e.key === 'ArrowLeft') {
    e.preventDefault();
    const cur = selectedPly.value === null ? total : selectedPly.value;
    if (cur > 0) onSelectPly(cur - 1);
    return;
  }
  if (e.key === 'ArrowRight') {
    e.preventDefault();
    const cur = selectedPly.value === null ? total : selectedPly.value;
    if (cur < total) onSelectPly(cur + 1);
    return;
  }
  if (e.key === 'Home') { e.preventDefault(); onSelectPly(0); return; }
  if (e.key === 'End')  { e.preventDefault(); clearScrub(); return; }
};

// Fetch the replay-frame list lazily — only when the user actually
// asks to scrub. Cached per game-id + ply count so a fresh analyze
// or a rematch invalidates the cache automatically.
const ensureReplayFrames = async (): Promise<ReplayFrame[] | null> => {
  const total = state.value?.history?.length ?? 0;
  if (replayFrames.value
      && replayFramesGameID === props.id
      && replayFramesPlies === total) {
    return replayFrames.value;
  }
  try {
    const frames = await api.getReplayFrames(props.id);
    replayFrames.value = frames;
    replayFramesGameID = props.id;
    replayFramesPlies = total;
    return frames;
  } catch (e: any) {
    toastStore.error('Failed to load replay: ' + (e?.message || e));
    return null;
  }
};

const onSelectPly = async (ply: number) => {
  if (!scrubEnabled.value || !state.value) return;
  const total = state.value.history?.length ?? 0;
  if (ply < 0) ply = 0;
  if (ply > total) ply = total;
  // Clicking the final ply on a finished game is functionally "live" —
  // surface that distinction in the URL bar by clearing the scrub
  // state instead of pinning to ply = history.length.
  if (ply === total) { clearScrub(); return; }
  const frames = await ensureReplayFrames();
  if (!frames) return;
  // Clear any live-mode selection/hint highlights so the board reads
  // cleanly as the historical position.
  selected.value = null;
  hint.value = null;
  selectedPly.value = ply;
};

const clearScrub = () => {
  selectedPly.value = null;
  selected.value = null;
  hint.value = null;
};

// Hints fan out on the requester's user.evt channel — never on
// game.evt — so the opponent in a PvP game doesn't see them. Engine
// and temp games still work because the requester is always also a
// participant. unsubscribeHint is set after register so onUnmounted
// can detach it cleanly.
let unsubscribeHint: (() => void) | null = null;

onMounted(async () => {
  try {
    const s = await api.getState(props.id);
    lastSoundedHistoryLen = s.history ? s.history.length : 0;
    prevFenForSound = s.fen;
    updateState(s);
    connectWS();
    // ?mode=setup arrives from Landing's "Set Up Board" tile. The
    // game row was just created with the standard starting position;
    // enter the editor immediately so the user lands inside it rather
    // than having to find the Setup button in the side panel. We
    // clear the query param afterwards so a router-replace bounce
    // (rematch, new-game) doesn't re-trigger the editor.
    if (route.query.mode === 'setup') {
      enterEditMode();
      router.replace({ path: route.path, query: {} });
    }
  } catch (e: any) {
    error.value = e.message || 'Unknown error';
  }

  unsubscribeHint = userEventsStore.on('hint', (payload) => {
    onHintReceived(payload);
  });

  window.addEventListener('keydown', onKeyDown);
  window.addEventListener('pointerdown', primeAudio);
  window.addEventListener('touchstart', primeAudio);
});

onUnmounted(() => {
  if (ws) {
    ws.onclose = null;
    ws.close();
  }
  if (unsubscribeHint) {
    unsubscribeHint();
    unsubscribeHint = null;
  }
  if (enginePollHandle) {
    clearInterval(enginePollHandle);
    enginePollHandle = undefined;
  }
  window.removeEventListener('keydown', onKeyDown);
  window.removeEventListener('pointerdown', primeAudio);
  window.removeEventListener('touchstart', primeAudio);
});
</script>

<style scoped>
/* Audio-blocked banner. Spans the full app width above the board so
   it's impossible to miss. Click anywhere on it to dismiss + flush
   the queued move sound. */
.audio-blocked-banner {
  position: fixed;
  top: 64px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 100;
  background: #3a3220;
  color: #e4b15a;
  border: 1px solid #56492a;
  border-radius: 6px;
  padding: 10px 18px;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px rgba(0,0,0,0.4);
  max-width: 90vw;
  text-align: center;
}
.audio-blocked-banner:hover { background: #463b27; }

#app-container { display: flex; gap: 32px; align-items: flex-start; justify-content: center; padding: 32px 20px; min-height: calc(100vh - 64px); width: 100%; }

/* Below 1000px the SidePanel stacks under the board instead of
 * sitting beside it. Earlier this breakpoint *hid* the panel entirely,
 * which meant a mobile user couldn't resign, undo, see moves, or open
 * settings — they could only tap pieces. Stacking keeps every control
 * reachable; the board still gets the full viewport width since the
 * --sq sizing on App.vue's <1000px branch already accounts for it. */
@media (max-width: 1000px) {
  #app-container { padding: 10px; gap: 12px; flex-direction: column; align-items: center; }
  #side { width: 100%; max-width: 560px; padding: 0 4px; }
}

#side { width: 340px; transition: width 120ms ease; }
/* Collapsed: keep status + clock + the toggle button visible; the
 * bulkier SidePanel/EditPanel are v-if'd out at the template level.
 * A narrower fixed width keeps the layout stable so the board doesn't
 * re-center jarringly each time the user toggles. */
#side.side-collapsed { width: 180px; }
@media (max-width: 1000px) {
  /* On stacked layouts (mobile), collapse just removes the panel
   * — width is already 100% — so leave width alone there. */
  #side.side-collapsed { width: 100%; }
}
.side-header { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
#side h2 { margin: 0 0 4px; font-size: 22px; letter-spacing: 0.5px; }
.panel-toggle {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  color: #aaa;
  width: 28px;
  height: 28px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
}
.panel-toggle:hover { border-color: #5a5a5a; color: #e0e0e0; background: #333; }
.game-id { font-size: 10px; color: #666; margin-bottom: 12px; }
#status { font-size: 16px; font-weight: 600; margin-bottom: 16px; padding: 10px 12px; background: #2b2b2b; border-radius: 4px; min-height: 22px; border-left: 3px solid #4a6b8a; }

.loading { display: flex; align-items: center; justify-content: center; height: calc(100vh - 64px); font-size: 18px; color: #888; }

.error-container { display: flex; align-items: center; justify-content: center; height: calc(100vh - 64px); padding: 20px; }
.error-box { background: #2b2b2b; padding: 40px; border-radius: 12px; text-align: center; max-width: 400px; box-shadow: 0 10px 30px rgba(0,0,0,0.5); }
.error-box h3 { color: #ff7575; margin: 0 0 16px; }
.error-box p { color: #aaa; margin-bottom: 24px; line-height: 1.5; }
</style>
