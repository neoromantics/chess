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
          <ol v-else class="move-chain">
            <li v-for="(node, i) in mainChain" :key="i" class="move-item">
              <span class="num">{{ Math.floor(i / 2) + 1 }}{{ i % 2 === 0 ? '.' : '…' }}</span>
              <span class="san">{{ node.san || node.move }}</span>
              <span v-if="node.comment" class="comment">{{ node.comment }}</span>
            </li>
          </ol>
        </section>

        <section class="card actions">
          <button class="btn primary" :disabled="forking" @click="playFromHere">
            {{ forking ? 'Creating game…' : 'Play from here' }}
          </button>
          <router-link
            v-if="study.source_game_id"
            :to="`/game/${study.source_game_id}`"
            class="btn ghost"
          >Open source game</router-link>
          <button class="btn ghost" :disabled="busy" @click="onRename">Rename</button>
          <button class="btn danger" :disabled="busy" @click="onDelete">Delete</button>
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
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ChessBoard from '../components/ChessBoard.vue';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';
import type { Study, StudyTreeNode, StateJSON } from '../types';

const route = useRoute();
const router = useRouter();
const toastStore = useToastStore();
const confirmStore = useConfirmStore();

const study = ref<Study | null>(null);
const loading = ref(true);
const error = ref<string | null>(null);
const busy = ref(false);
const forking = ref(false);

onMounted(async () => {
  const id = route.params.id as string;
  if (!id) {
    error.value = 'Missing study id.';
    loading.value = false;
    return;
  }
  try {
    study.value = await api.getStudy(id);
  } catch (e: any) {
    error.value = e?.message || 'Failed to load.';
  } finally {
    loading.value = false;
  }
});

// Main chain is "follow the first child at each level." v1 trees are
// linear so this is the whole tree; when branching ships, the user
// will need a way to pick the displayed line.
const mainChain = computed<StudyTreeNode[]>(() => {
  const out: StudyTreeNode[] = [];
  let node: StudyTreeNode | undefined = study.value?.tree;
  while (node && node.children && node.children.length > 0) {
    node = node.children[0];
    out.push(node);
  }
  return out;
});

// Synthesize a minimal StateJSON for ChessBoard. The component reads
// fen, last_move, in_check, turn, legal_moves — everything else is
// padded with safe defaults. No moves are legal here because there's
// no game row backing this position.
const boardState = computed<StateJSON | null>(() => {
  if (!study.value) return null;
  const turn = study.value.start_fen.split(' ')[1] === 'b' ? 'b' : 'w';
  return {
    fen: study.value.start_fen,
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
    last_move: null,
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

// "Play from here" creates a new engine-game row at the study's start
// position. The user can then play out the line (or any other line)
// against the engine. We don't pre-apply the study's moves — playing
// out the saved variation manually is the point.
const playFromHere = async () => {
  if (!study.value || forking.value) return;
  forking.value = true;
  try {
    const { game_id } = await api.createGame({});
    try {
      await api.setPosition(game_id, study.value.start_fen);
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
  const name = window.prompt('New name', study.value.name);
  if (!name || name.trim() === '' || name.trim() === study.value.name) return;
  busy.value = true;
  try {
    study.value = await api.updateStudy(study.value.id, { name: name.trim(), tree: study.value.tree });
    toastStore.success('Renamed');
  } catch (e: any) {
    toastStore.error('Could not rename: ' + (e?.message || e));
  } finally {
    busy.value = false;
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
.study-page { max-width: 1100px; margin: 0 auto; padding: 24px 20px; }
.study-header { margin-bottom: 18px; }
.study-header h2 { margin: 6px 0 4px; }
.back-link { color: #6a8aa6; font-size: 13px; text-decoration: none; }
.back-link:hover { text-decoration: underline; }
.muted { color: #888; font-size: 13px; }
.error-box { max-width: 480px; margin: 80px auto; padding: 24px; background: #2b2b2b; border-radius: 10px; }
.loading { text-align: center; padding: 80px; color: #888; }

.layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 320px;
  gap: 24px;
  align-items: start;
}
@media (max-width: 880px) {
  .layout { grid-template-columns: 1fr; }
}

.aside { display: flex; flex-direction: column; gap: 12px; }
.card {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 8px;
  padding: 14px;
}
.card h3 { margin: 0 0 10px; font-size: 13px; text-transform: uppercase; letter-spacing: 0.5px; color: #aaa; }

.move-chain { list-style: none; padding: 0; margin: 0; font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
.move-item { display: flex; gap: 8px; padding: 2px 0; align-items: baseline; }
.move-item .num { color: #666; min-width: 36px; font-size: 12px; }
.move-item .san { color: #e0e0e0; }
.move-item .comment { color: #888; font-style: italic; font-family: inherit; font-size: 12px; }

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
</style>
