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
      <ul v-else class="study-list">
        <li v-for="st in studies" :key="st.id" class="study-row">
          <div class="study-meta" @click="open(st)" role="button" tabindex="0">
            <strong class="study-name">{{ st.name }}</strong>
            <span v-if="hasMoves(st)" class="muted">
              {{ plyCount(st) }} {{ plyCount(st) === 1 ? 'ply' : 'plies' }}
            </span>
            <span v-else class="muted">setup only</span>
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
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';
import { usePromptStore } from '../stores/prompt';
import type { Study, StudyTreeNode } from '../types';

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

// Walk down the tree's main chain (first child at each level) to count
// half-moves. v1 trees are linear so this is just the chain length;
// when branching lands the main-line convention will need a tiebreak
// (longest branch? user-marked main?) — for now first-child wins.
const plyCount = (st: Study): number => {
  let n = 0;
  let node: StudyTreeNode | undefined = st.tree;
  while (node && node.children && node.children.length > 0) {
    n++;
    node = node.children[0];
  }
  return n;
};

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
