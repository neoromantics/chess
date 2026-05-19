<template>
  <div class="studies-page">
    <header class="page-header">
      <h2>Studies</h2>
      <p class="hint">
        Saved positions and exploration lines. Save a setup from the
        board editor, or save a game from the side panel after playing.
      </p>
    </header>

    <section class="card">
      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="error" class="error">{{ error }}</p>
      <p v-else-if="!studies.length" class="muted">
        No studies yet. Save a position from the board editor or a game
        from the side panel and it'll appear here.
      </p>
      <!-- Studies grouped by start_fen. Multi-study groups get a header
           with a mini board + "N lines from this position" so the user
           can see at a glance which positions they've explored multiple
           continuations from. Single-study positions render as a flat
           row (no group chrome) — clustering a group of one would be
           visual noise. -->
      <ul v-else class="study-groups">
        <li v-for="g in groups" :key="g.startFen" :class="['study-group', { multi: g.studies.length > 1 }]">
          <div v-if="g.studies.length > 1" class="group-header">
            <div class="group-preview" :style="{ '--sq': '18px' }">
              <ChessBoard
                :state="boardStateFor(g.startFen)"
                :flipped="false"
                :selected="null"
                :hint="null"
                :edit-board="null"
              />
            </div>
            <div class="group-summary">
              <strong>{{ g.studies.length }} lines from this position</strong>
              <span class="muted">{{ positionLabel(g.startFen) }}</span>
            </div>
          </div>
          <ul class="study-list">
            <li v-for="st in g.studies" :key="st.id" class="study-row">
              <div class="study-meta" @click="open(st)" role="button" tabindex="0">
                <strong class="study-name">{{ st.name }}</strong>
                <span v-if="hasMoves(st)" class="muted">
                  {{ plyCount(st) }} {{ plyCount(st) === 1 ? 'ply' : 'plies' }}
                </span>
                <span v-else class="muted">setup only</span>
                <!-- Divergence hint: where this study branches off from
                     its closest sibling in the group. Surfaced inline
                     so the user can see at a glance whether two saved
                     lines share an opening or diverge from move 1. -->
                <span v-if="divergenceMove(st, g.studies) !== null" class="diverge-badge"
                  :title="`Shares opening with another line up through this point`">
                  diverges at move {{ divergenceMove(st, g.studies) }}
                </span>
                <span v-if="st.source_game_id" class="muted source" :title="`from game ${st.source_game_id}`">
                  · from a game
                </span>
                <span class="muted ts">· {{ formatDate(st.created_at) }}</span>
              </div>
              <div class="study-actions">
                <button class="btn-secondary" @click="open(st)">Open</button>
                <button class="btn-secondary" :disabled="busyId === st.id" @click="rename(st)">Rename</button>
                <button class="btn-danger" :disabled="busyId === st.id" @click="onDelete(st)">Delete</button>
              </div>
            </li>
          </ul>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import ChessBoard from '../components/ChessBoard.vue';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';
import { usePromptStore } from '../stores/prompt';
import type { Study, StateJSON } from '../types';
import { plyCountOf, mainChainOf, commonPrefixLength, moveNumberOfPly, STANDARD_START_FEN } from '../util/chess';

const router = useRouter();
const toastStore = useToastStore();
const confirmStore = useConfirmStore();
const promptStore = usePromptStore();

const studies = ref<Study[]>([]);
const loading = ref(true);
const error = ref<string | null>(null);
const busyId = ref<string | null>(null);

const refresh = async () => {
  loading.value = true;
  error.value = null;
  try {
    studies.value = await api.listStudies();
  } catch (e: any) {
    error.value = e?.message || 'Failed to load studies.';
  } finally {
    loading.value = false;
  }
};

onMounted(refresh);

const open = (st: Study) => router.push(`/study/${st.id}`);

// Tree walker lives in util/chess.ts so this view and StudyView
// stay aligned on what "main chain" means as v1 → v2 branching lands.
const plyCount = (st: Study): number => plyCountOf(st.tree);

const hasMoves = (st: Study): boolean => plyCount(st) > 0;

const formatDate = (iso: string): string => {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
};

const rename = async (st: Study) => {
  const name = await promptStore.ask({
    title: 'Rename study',
    defaultValue: st.name,
    confirmLabel: 'Save',
  });
  if (!name || name === st.name) return;
  busyId.value = st.id;
  try {
    const updated = await api.updateStudy(st.id, { name, tree: st.tree });
    const idx = studies.value.findIndex(s => s.id === st.id);
    if (idx >= 0) studies.value[idx] = updated;
    toastStore.success('Renamed');
  } catch (e: any) {
    toastStore.error('Could not rename: ' + (e?.message || e));
  } finally {
    busyId.value = null;
  }
};

// Bucket studies by start_fen, preserving the order the list arrived
// in (backend returns ORDER BY created_at DESC, so a group lands
// where its newest member was). Each multi-study group gets a header
// summarizing the shared position; single-study positions render as
// flat rows like before.
interface Group { startFen: string; studies: Study[] }
const groups = computed<Group[]>(() => {
  const map = new Map<string, Group>();
  for (const st of studies.value) {
    const key = st.start_fen;
    const g = map.get(key);
    if (g) g.studies.push(st);
    else map.set(key, { startFen: key, studies: [st] });
  }
  return Array.from(map.values());
});

// LAN main-chain for a study, used to compare prefixes across siblings.
// Memoized per render via the groups computed below — cheap enough at
// typical group sizes that explicit memoization isn't worth it.
const lanChain = (st: Study): string[] => mainChainOf(st.tree).map(n => n.move || '');

// Where this study diverges from its closest sibling in the group.
// Returns the move number (1-indexed display) where the first
// disagreeing ply occurs. Returns null when this study has no shared
// opening with any sibling — for v1 we render nothing in that case
// rather than the noisier "diverges at move 1" badge.
const divergenceMove = (st: Study, siblings: Study[]): number | null => {
  if (siblings.length < 2) return null;
  const mine = lanChain(st);
  let maxShared = 0;
  for (const other of siblings) {
    if (other.id === st.id) continue;
    const shared = commonPrefixLength(mine, lanChain(other));
    if (shared > maxShared) maxShared = shared;
  }
  if (maxShared === 0) return null;
  // First divergent ply is at index maxShared; convert to its 1-indexed
  // move number for the human-readable badge.
  return moveNumberOfPly(maxShared + 1);
};

// Compact label for the group header. Distinguishes the standard
// starting position from custom setups; full FEN inspection is one
// click away (the group header doesn't render it inline, the FEN
// disclosure inside each study viewer does).
const positionLabel = (fen: string): string =>
  fen === STANDARD_START_FEN ? 'Standard starting position' : 'Custom position';

// Synthesize a minimal StateJSON for ChessBoard so the group preview
// can render the start position. Same trick StudyView uses — every
// field padded with safe defaults; legal_moves empty so the board
// can't accidentally accept a click. Cast through `as StateJSON` to
// skip the dozen game-specific fields a preview doesn't need.
const boardStateFor = (fen: string): StateJSON => {
  const turn = fen.split(' ')[1] === 'b' ? 'b' : 'w';
  return {
    fen,
    turn,
    legal_moves: [],
    history: [],
    history_san: [],
    last_move: null,
    in_check: false,
    thinking: false,
    status: 'ongoing',
    result: '*',
    engine_white: false,
    engine_black: false,
    engine_to_move: false,
    white_think_time: 0,
    black_think_time: 0,
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
};

const onDelete = async (st: Study) => {
  const confirmed = await confirmStore.ask({
    title: 'Delete study?',
    message: `"${st.name}" will be permanently removed.`,
    confirmLabel: 'Delete',
    danger: true,
  });
  if (!confirmed) return;
  busyId.value = st.id;
  try {
    await api.deleteStudy(st.id);
    studies.value = studies.value.filter(s => s.id !== st.id);
    toastStore.success('Deleted');
  } catch (e: any) {
    toastStore.error('Could not delete: ' + (e?.message || e));
  } finally {
    busyId.value = null;
  }
};
</script>

<style scoped>
.studies-page { max-width: 880px; margin: 0 auto; padding: 32px 20px; }
.page-header h2 { margin: 0 0 6px; font-size: 26px; }
.page-header .hint { color: #999; font-size: 14px; line-height: 1.5; margin: 0 0 18px; }
.card {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 10px;
  padding: 18px;
}
.muted { color: #888; font-size: 13px; }
.error { color: #d35454; font-size: 13px; }

.study-groups { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 16px; }
.study-group.multi {
  background: #1f1f1f;
  border: 1px solid #2f2f2f;
  border-radius: 10px;
  padding: 12px;
}
.group-header {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 4px 4px 10px;
  border-bottom: 1px solid #2f2f2f;
  margin-bottom: 10px;
}
.group-preview {
  flex: 0 0 auto;
  /* Pointer-events off + cursor default: the mini board is purely
   * visual here, the click target is the study row below. */
  pointer-events: none;
}
.group-summary { display: flex; flex-direction: column; gap: 2px; }
.group-summary strong { color: #e8e8e8; font-size: 14px; }
.group-summary .muted { font-size: 12px; }

.study-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; }
.study-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  background: #2a2a2a;
  border: 1px solid #333;
  border-radius: 8px;
  padding: 12px 14px;
  transition: border-color 120ms ease;
}
.study-row:hover { border-color: #4a6b8a; }
.study-meta {
  flex: 1 1 auto;
  display: flex;
  align-items: baseline;
  gap: 8px;
  cursor: pointer;
  min-width: 0;
}
.study-name { font-size: 15px; color: #eee; }
.study-meta .ts { font-size: 12px; }
.study-meta .source { font-size: 12px; }
.diverge-badge {
  font-size: 11px;
  color: #cbd6e0;
  background: #2a3a4a;
  border: 1px solid #3a4d62;
  border-radius: 4px;
  padding: 1px 6px;
  white-space: nowrap;
}
.study-actions { display: flex; gap: 6px; flex: 0 0 auto; }
.btn-secondary, .btn-danger {
  background: #2f2f2f;
  border: 1px solid #3a3a3a;
  color: #ddd;
  padding: 6px 12px;
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
}
.btn-secondary:hover { background: #383838; }
.btn-danger { color: #e09090; border-color: #5a3030; }
.btn-danger:hover { background: #3a2020; }
.btn-secondary:disabled, .btn-danger:disabled { opacity: 0.5; cursor: wait; }
</style>
