<template>
  <div v-if="error" class="error-box">
    <h3>Failed to load study</h3>
    <p>{{ error }}</p>
    <router-link to="/study/" class="back-link">← Back to studies</router-link>
  </div>
  <div v-else-if="loading" class="loading">Loading…</div>
  <div v-else-if="study" class="study-page">
    <header class="study-header">
      <router-link to="/study/" class="back-link">← Studies</router-link>
      <h2>{{ study.name }}</h2>
      <p class="muted">
        Saved {{ formatDate(study.created_at) }}
        <template v-if="study.source_game_id"> · from a game</template>
      </p>
    </header>

    <div class="layout">
      <!-- Board: renders the start position as a static snapshot.
           Click-to-move is gated server-side (no game row) and the
           synthetic state below carries empty legal_moves anyway, so
           clicks are no-ops. -->
      <ChessBoard :state="boardState" :flipped="false" :selected="null" :hint="null" :edit-board="null" />

      <aside class="aside">
        <section class="card">
          <h3>Moves</h3>
          <p v-if="!mainChain.length" class="muted">No moves saved — just a position.</p>
          <!-- Paired-per-row layout to match SidePanel's move list:
               `1. e4 e5` on one row, `2. Nf3 Nc6` on the next, etc.
               historyPairs groups the linear main chain into white/black
               pairs (trailing white move appears alone if the line ends
               on white). Comments hang off the individual moves so they
               don't disrupt the columns. -->
          <!-- "Start" jumps to position before any moves; same channel
               as clicking a move. positionsLoaded gates click handlers
               so we don't try to scrub before the fetched FENs land —
               otherwise the board would briefly flash an empty state. -->
          <div v-if="mainChain.length" class="scrub-row">
            <span class="muted">Ply {{ selectedIdx }}/{{ mainChain.length }}</span>
            <button class="btn ghost btn-sm" :disabled="!positionsLoaded || selectedIdx === 0" @click="selectedIdx = 0">Start</button>
          </div>
          <div v-else class="move-list">
            <div class="move-empty">No moves saved — just a position.</div>
          </div>
          <div v-if="mainChain.length" class="move-list">
            <div v-for="(pair, i) in movePairs" :key="i" class="move-row">
              <span class="move-num">{{ i + 1 }}.</span>
              <span v-for="(mv, j) in pair" :key="j" class="move-cell">
                <span
                  class="move-san"
                  :class="{ scrubbable: positionsLoaded, 'scrub-active': selectedIdx === (i * 2 + j + 1) }"
                  @click="positionsLoaded && (selectedIdx = i * 2 + j + 1)"
                >{{ mv.san || mv.move }}</span>
                <span v-if="mv.comment" class="comment">{{ mv.comment }}</span>
              </span>
            </div>
          </div>
        </section>

        <section class="card actions">
          <!-- Play-from-here is the one action that works for any
               viewer (signed-in or anonymous), because createGame
               is auth-gated server-side anyway — anonymous gets an
               auth error and the toast nudges them to sign in.
               Source-game link only renders for the owner; non-
               owners shouldn't be linked to a game they may not
               be able to read. -->
          <button class="btn primary" :disabled="forking" @click="playFromHere">
            {{ forking ? 'Creating game…' : 'Play from here' }}
          </button>
          <router-link
            v-if="isOwner && study.source_game_id"
            :to="`/game/${study.source_game_id}`"
            class="btn ghost"
          >Open source game</router-link>
          <button v-if="isOwner" class="btn ghost" :disabled="busy" @click="onRename">Rename</button>
          <button v-if="isOwner" class="btn danger" :disabled="busy" @click="onDelete">Delete</button>
        </section>

        <!-- Share card. Owner sees a public/private toggle + a copy-
             link button when public. Non-owners (viewing via a shared
             link) see nothing here — the card is owner-only. -->
        <section v-if="isOwner" class="card share-card">
          <h3>Share</h3>
          <div class="share-row">
            <label class="share-toggle">
              <input
                type="checkbox"
                :checked="study.is_public"
                :disabled="busy"
                @change="onToggleVisibility(($event.target as HTMLInputElement).checked)"
              />
              <span>{{ study.is_public ? 'Public — anyone with the link can view' : 'Private — only you' }}</span>
            </label>
          </div>
          <div v-if="study.is_public" class="share-row">
            <input class="share-url" :value="shareURL" readonly @focus="($event.target as HTMLInputElement).select()" />
            <button class="btn ghost btn-sm" @click="copyShareLink">{{ copyState }}</button>
          </div>
        </section>

        <section class="card">
          <details>
            <summary>Start FEN</summary>
            <code class="fen">{{ study.start_fen }}</code>
          </details>
        </section>
      </aside>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ChessBoard from '../components/ChessBoard.vue';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';
import { usePromptStore } from '../stores/prompt';
import { useAuthStore } from '../stores/auth';
import type { Study, StudyTreeNode, StateJSON } from '../types';
import { STANDARD_START_FEN, buildPGNFromMoves, mainChainOf } from '../util/chess';

const route = useRoute();
const router = useRouter();
const toastStore = useToastStore();
const confirmStore = useConfirmStore();
const promptStore = usePromptStore();
const authStore = useAuthStore();

// True when the viewer owns this study — the only viewer who sees
// Rename / Delete / share toggle. Anonymous viewers (signed-out) on
// a public study always evaluate false here.
const isOwner = computed(() => !!authStore.user && !!study.value && study.value.user_id === authStore.user.id);

// Permalink for the share input. Uses the current origin so the link
// works whether we're on prod (https://vcm-50800.vm.duke.edu) or local
// dev (http://localhost:5173 etc.); window.location.origin handles both.
const shareURL = computed(() => `${window.location.origin}/study/${study.value?.id ?? ''}`);
const copyState = ref('Copy link');

const study = ref<Study | null>(null);
const loading = ref(true);
const error = ref<string | null>(null);
const busy = ref(false);
const forking = ref(false);

// Position scrub state. positions[k] = FEN after the k-th move in the
// main chain (index 0 = start FEN, matches study.start_fen). Fetched
// from /api/studies/{id}/positions on mount — the server replays the
// LAN moves through pkg/core so we don't need a JS chess engine here.
// positionsLoaded gates the move-list click handlers + keyboard nav
// so a fast click during the fetch window doesn't try to read an
// undefined index.
const positions = ref<string[]>([]);
const positionsLoaded = ref(false);
const selectedIdx = ref(0);

onMounted(async () => {
  const id = route.params.id as string;
  if (!id) {
    error.value = 'Missing study id.';
    loading.value = false;
    return;
  }
  try {
    study.value = await api.getStudy(id);
    // Fetch positions in the background; the viewer renders the start
    // position immediately, then snaps to the end-of-line once positions
    // land. Defaulting to the saved end state (rather than start)
    // matches what the user expects when opening a study they saved —
    // the named "study" is the position they reached, not the position
    // they started from. They can Home / arrow back to scrub.
    api.getStudyPositions(id)
      .then((fs) => {
        positions.value = fs;
        positionsLoaded.value = true;
        selectedIdx.value = Math.max(0, fs.length - 1);
      })
      .catch((e) => toastStore.error('Could not load study positions: ' + (e?.message || e)));
  } catch (e: any) {
    error.value = e?.message || 'Failed to load.';
  } finally {
    loading.value = false;
  }
  window.addEventListener('keydown', onKey);
});

onUnmounted(() => {
  window.removeEventListener('keydown', onKey);
});

// Keyboard navigation mirrors GameView's scrub controls: ←/→ step one
// ply, Home/End jump to bookends. Skipped when an input or textarea
// has focus so typing into the rename modal doesn't move the board.
const onKey = (e: KeyboardEvent) => {
  if (!positionsLoaded.value) return;
  const tag = (e.target && (e.target as any).tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  const lastIdx = mainChain.value.length;
  if (e.key === 'ArrowLeft' && selectedIdx.value > 0) { e.preventDefault(); selectedIdx.value--; }
  else if (e.key === 'ArrowRight' && selectedIdx.value < lastIdx) { e.preventDefault(); selectedIdx.value++; }
  else if (e.key === 'Home') { e.preventDefault(); selectedIdx.value = 0; }
  else if (e.key === 'End') { e.preventDefault(); selectedIdx.value = lastIdx; }
};

// Tree walker lives in util/chess.ts so this view and StudiesList
// agree on what "main chain" means; comment over the helper covers
// the first-child-wins convention.
const mainChain = computed<StudyTreeNode[]>(() => mainChainOf(study.value?.tree));

// Group the main chain into white/black pairs for display. Mirrors
// GameView's historyPairs computation: every two plies form a row,
// and a trailing white-only move sits alone at the end if the line
// ended on white. Index 0 in mainChain is the first move (white),
// 1 is black's reply, etc. — same as state.history.
const movePairs = computed(() => {
  const res: { san?: string; move?: string; comment?: string }[][] = [];
  for (let i = 0; i < mainChain.value.length; i += 2) {
    const pair: { san?: string; move?: string; comment?: string }[] = [mainChain.value[i]];
    if (i + 1 < mainChain.value.length) pair.push(mainChain.value[i + 1]);
    res.push(pair);
  }
  return res;
});

// Synthesize a minimal StateJSON for ChessBoard. The component reads
// fen, last_move, in_check, turn, legal_moves — everything else is
// padded with safe defaults. No moves are legal here because there's
// no game row backing this position.
//
// FEN comes from positions[selectedIdx] when scrubbing is active;
// before the fetch lands (or if it fails), we render study.start_fen
// at index 0 so the board isn't blank during the load.
const boardState = computed<StateJSON | null>(() => {
  if (!study.value) return null;
  const fen = (positions.value.length > selectedIdx.value)
    ? positions.value[selectedIdx.value]
    : study.value.start_fen;
  const turn = fen.split(' ')[1] === 'b' ? 'b' : 'w';
  // Highlight the move that landed us here (when scrubbed past start).
  // mainChain[k-1] holds the move whose from/to we want; converting
  // LAN "e2e4" into squares is a substring split.
  let lastMove: { from: string; to: string } | null = null;
  if (selectedIdx.value > 0 && selectedIdx.value <= mainChain.value.length) {
    const lan = mainChain.value[selectedIdx.value - 1].move || '';
    if (lan.length >= 4) lastMove = { from: lan.slice(0, 2), to: lan.slice(2, 4) };
  }
  return {
    fen,
    turn,
    engine_white: false,
    engine_black: false,
    engine_to_move: false,
    status: 'ongoing',
    result: '*',
    in_check: false,
    legal_moves: [],
    history: [],
    history_san: [],
    last_move: lastMove,
    thinking: false,
    white_think_time: 1000,
    black_think_time: 1000,
    white_user_id: null,
    black_user_id: null,
    time_control: '',
    rated: false,
    is_public: false,
    white_clock_ms: 0,
    black_clock_ms: 0,
    clock_initial_ms: 0,
    clock_inc_ms: 0,
    clock_mover: '',
    clock_server_ms: 0,
  } as StateJSON;
});

const formatDate = (iso: string): string => {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
};

// "Play from here" forks the study into a new engine-game row at the
// CURRENTLY-SCRUBBED position, carrying the prefix chain as history.
// So if the user scrubbed to ply 7 and clicked Play, the new game
// starts at the standard position with moves 1-7 already played and
// the move list populated — the user picks up from there. Mirrors
// GameView's fork-from-ply behavior; one round-trip via load_pgn.
//
// When selectedIdx === 0 (start position): nothing to replay, fall
// back to setPosition (or skip both if start_fen is the standard).
const playFromHere = async () => {
  if (!study.value || forking.value) return;
  forking.value = true;
  try {
    const { game_id } = await api.createGame({});
    const prefix = mainChain.value.slice(0, selectedIdx.value);
    try {
      if (prefix.length > 0) {
        const sanMoves = prefix.map(n => n.san || n.move || '');
        await api.loadPgn(game_id, buildPGNFromMoves(study.value.start_fen, sanMoves));
      } else if (study.value.start_fen && study.value.start_fen !== STANDARD_START_FEN) {
        // No prefix, but non-standard start — set the position alone.
        await api.setPosition(game_id, study.value.start_fen);
      }
      // else: standard start + no prefix → fresh game already at the
      // standard position from createGame, no extra call needed.
    } catch (e: any) {
      toastStore.error('Created the game but couldn’t apply the position: ' + (e?.message || e));
    }
    router.push(`/game/${game_id}`);
  } catch (e: any) {
    toastStore.error('Could not create the game: ' + (e?.message || e));
  } finally {
    forking.value = false;
  }
};

const onRename = async () => {
  if (!study.value) return;
  const name = await promptStore.ask({
    title: 'Rename study',
    defaultValue: study.value.name,
    confirmLabel: 'Save',
  });
  if (!name || name === study.value.name) return;
  busy.value = true;
  try {
    study.value = await api.updateStudy(study.value.id, { name, tree: study.value.tree, position_label: study.value.position_label });
    toastStore.success('Renamed');
  } catch (e: any) {
    toastStore.error('Could not rename: ' + (e?.message || e));
  } finally {
    busy.value = false;
  }
};

// Owner-only toggle for the share-link gate. Optimistic-update via
// the returned Study (handler echoes the new is_public flag) so the
// UI doesn't need a second fetch.
const onToggleVisibility = async (next: boolean) => {
  if (!study.value || !isOwner.value || busy.value) return;
  busy.value = true;
  try {
    study.value = await api.setStudyVisibility(study.value.id, next);
    toastStore.success(next ? 'Study is now public.' : 'Study is now private.');
  } catch (e: any) {
    toastStore.error('Could not change visibility: ' + (e?.message || e));
  } finally {
    busy.value = false;
  }
};

// Copy-to-clipboard with a 1.5s "Copied!" affordance on the button.
// Falls back to a select-on-focus on browsers that block writeText
// (older Safari, non-secure contexts) — input is already readonly,
// the user can ⌘C from there.
const copyShareLink = async () => {
  try {
    await navigator.clipboard.writeText(shareURL.value);
    copyState.value = 'Copied!';
    setTimeout(() => { copyState.value = 'Copy link'; }, 1500);
  } catch {
    toastStore.error('Clipboard blocked — select the URL and copy manually.');
  }
};

const onDelete = async () => {
  if (!study.value) return;
  const confirmed = await confirmStore.ask({
    title: 'Delete study?',
    message: `"${study.value.name}" will be permanently removed.`,
    confirmLabel: 'Delete',
    danger: true,
  });
  if (!confirmed) return;
  busy.value = true;
  try {
    await api.deleteStudy(study.value.id);
    toastStore.success('Deleted');
    router.push('/study/');
  } catch (e: any) {
    toastStore.error('Could not delete: ' + (e?.message || e));
    busy.value = false;
  }
};
</script>

<style scoped>
/* No max-width on .study-page itself: the board's --sq (computed in
 * App.vue's :root) assumes the full viewport minus the side panel.
 * Capping the page at e.g. 1100px would force the board column below
 * its natural width and either overflow into the aside or push it
 * underneath. Use the same shape as GameView (flex, centered, full
 * width) so --sq's calc lands the same way. The header stays
 * left-aligned within a narrow container above the board so the
 * "← Studies" back link doesn't drift to the middle of the page. */
.study-page { padding: 12px 20px 24px; }
.study-header { max-width: 1100px; margin: 0 auto 18px; }
.study-header h2 { margin: 6px 0 4px; }
.back-link { color: #6a8aa6; font-size: 13px; text-decoration: none; }
.back-link:hover { text-decoration: underline; }
.muted { color: #888; font-size: 13px; }
.error-box { max-width: 480px; margin: 80px auto; padding: 24px; background: #2b2b2b; border-radius: 10px; }
.loading { text-align: center; padding: 80px; color: #888; }

.layout {
  display: flex;
  gap: 32px;
  align-items: flex-start;
  justify-content: center;
  width: 100%;
}
@media (max-width: 1000px) {
  /* Same breakpoint as GameView: below 1000px the side panel stacks
   * under the board instead of sitting beside it. */
  .layout { flex-direction: column; align-items: center; gap: 12px; }
  .aside { width: 100%; max-width: 560px; }
}

.aside { display: flex; flex-direction: column; gap: 12px; width: 340px; flex: 0 0 auto; }
.card {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 8px;
  padding: 14px;
}
.card h3 { margin: 0 0 10px; font-size: 13px; text-transform: uppercase; letter-spacing: 0.5px; color: #aaa; }

/* Mirrors SidePanel's move-list shape so the two views read the same. */
.move-list { font-family: ui-monospace, Menlo, monospace; font-size: 13px; max-height: 320px; overflow-y: auto; padding-right: 4px; }
.move-row { display: flex; align-items: baseline; gap: 6px; padding: 2px 0; line-height: 1.5; }
.move-num { color: #666; min-width: 28px; font-size: 12px; }
.move-cell { min-width: 60px; }
.move-san { color: #e0e0e0; padding: 0 2px; border-radius: 2px; }
.move-san.scrubbable { cursor: pointer; }
.move-san.scrubbable:hover { background: #2a2a2a; }
.move-san.scrub-active { background: #4a4a8a; color: #fff; font-weight: 600; }
.move-empty { color: #666; font-style: italic; padding: 8px 0; }
.scrub-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 6px 0; border-bottom: 1px solid #2f2f2f; margin-bottom: 6px; font-size: 12px; }
.scrub-row .btn-sm { padding: 2px 10px; font-size: 12px; flex: 0 0 auto; }
.comment { color: #888; font-style: italic; font-family: inherit; font-size: 12px; margin-left: 4px; }

.actions { display: flex; flex-direction: column; gap: 8px; }
.btn {
  display: inline-block;
  text-align: center;
  text-decoration: none;
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
.btn:disabled { opacity: 0.6; cursor: wait; }
.btn.primary { background: #2d5a2d; border-color: #3a703a; color: #fff; font-weight: 600; }
.btn.primary:hover { background: #347a34; }
.btn.ghost { background: transparent; color: #ccc; }
.btn.ghost:hover { background: #2a2a2a; }
.btn.danger { background: transparent; border-color: #5a3030; color: #e09090; }
.btn.danger:hover { background: #3a2020; }

.fen { display: block; padding: 8px; background: #1c1c1c; border-radius: 4px; font-family: ui-monospace, Menlo, monospace; font-size: 11px; color: #ccc; word-break: break-all; margin-top: 8px; }

/* Share card */
.share-card { display: flex; flex-direction: column; gap: 10px; }
.share-row { display: flex; align-items: center; gap: 8px; }
.share-toggle { display: flex; align-items: center; gap: 8px; cursor: pointer; font-size: 13px; color: #ddd; }
.share-toggle input { accent-color: #4a6b8a; }
.share-url { flex: 1 1 auto; min-width: 0; background: #1c1c1c; border: 1px solid #333; border-radius: 4px; color: #ccc; font-family: ui-monospace, Menlo, monospace; font-size: 12px; padding: 6px 8px; }
.share-card .btn-sm { padding: 5px 10px; font-size: 12px; flex: 0 0 auto; }
</style>
