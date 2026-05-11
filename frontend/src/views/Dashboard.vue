<template>
  <div class="dashboard-container">
    <div class="main-content">
      <section class="sessions-section">
        <div class="section-header">
          <h2>Your Games</h2>
          <button @click="createNewGame" class="btn-primary" :disabled="loading">
            {{ loading ? 'Starting...' : 'New Game +' }}
          </button>
        </div>

        <div v-if="games.length === 0" class="empty-state">
          <div class="empty-icon">♟</div>
          <p>You don't have any active games yet.</p>
          <button @click="createNewGame" class="btn-secondary-outline">Start your first game</button>
        </div>

        <div class="game-list" v-else>
          <div v-for="g in games" :key="g.id" class="game-card">
            <div class="game-main">
              <div class="game-info">
                <span :class="['game-label', g.status]">Status: {{ formatStatus(g.status) }}</span>
                <span class="game-id">ID: {{ truncateId(g.id) }}</span>
                <span class="game-date">Updated: {{ formatDate(g.updated_at) }}</span>
              </div>
              <div class="game-actions">
                <router-link :to="'/game/' + g.id" class="btn-action">Resume</router-link>
                <button @click="deleteGame(g.id)" class="btn-delete" title="Delete game">×</button>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();
const games = ref<any[]>([]);
const loading = ref(false);

const loadGames = async () => {
  try {
    const res = await api.listGames();
    games.value = res.map(g => typeof g === 'string' ? { id: g, status: 'ongoing', updated_at: new Date().toISOString() } : g);
  } catch (e) {
    toastStore.error('Failed to load games');
  }
};

const createNewGame = async () => {
  loading.value = true;
  try {
    const res = await api.createGame();
    toastStore.success('New game created');
    router.push(`/game/${res.game_id}`);
  } catch (e) {
    toastStore.error('Failed to create game');
  } finally {
    loading.value = false;
  }
};

const deleteGame = async (id: string) => {
  if (!confirm('Are you sure you want to delete this game?')) return;
  try {
    await api.deleteGame(id);
    toastStore.info('Game deleted');
    await loadGames();
  } catch (e) {
    toastStore.error('Failed to delete game');
  }
};

const formatDate = (dateStr: string) => {
  return new Date(dateStr).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' });
};

const formatStatus = (status: string) => {
  return status.charAt(0).toUpperCase() + status.slice(1).replace('_', ' ');
};

const truncateId = (id: string) => {
  return id.split('-')[0] + '...';
};

onMounted(async () => {
  await authStore.init();
  await loadGames();
});
</script>

<style scoped>
.dashboard-container { max-width: 1000px; margin: 0 auto; padding: 40px 20px; }

.section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 24px; }
h2 { margin: 0; font-size: 20px; color: #ddd; }

.game-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 20px; }

.game-card { background: #2b2b2b; border-radius: 8px; padding: 16px; border-left: 4px solid #4a6b8a; transition: transform 0.1s, box-shadow 0.1s; }
.game-card:hover { transform: translateY(-2px); box-shadow: 0 4px 12px rgba(0,0,0,0.3); }

.game-main { display: flex; justify-content: space-between; align-items: center; }

.game-info { display: flex; flex-direction: column; gap: 4px; }
.game-label { font-size: 11px; font-weight: 700; color: #aaa; }
.game-label.ongoing { color: #4ec77a; }
.game-label.checkmate { color: #ff7575; }

.game-id { font-family: ui-monospace, monospace; font-size: 11px; color: #666; }
.game-date { font-size: 11px; color: #888; }

.game-actions { display: flex; align-items: center; gap: 12px; }

.btn-primary { background: #4a6b8a; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: 600; font-size: 14px; }
.btn-primary:hover { background: #5a7b9a; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-secondary-outline { background: transparent; border: 1px solid #555; color: #aaa; padding: 12px 24px; border-radius: 4px; cursor: pointer; margin-top: 16px; }
.btn-secondary-outline:hover { border-color: #4a6b8a; color: white; }

.btn-action { background: #2d5a2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-size: 14px; font-weight: 600; text-decoration: none; }
.btn-action:hover { background: #3a6b3a; }

.btn-delete { background: transparent; border: none; color: #666; font-size: 20px; padding: 0 4px; line-height: 1; border-radius: 4px; cursor: pointer; }
.btn-delete:hover { color: #ff7575; background: rgba(255, 117, 117, 0.1); }

.empty-state { text-align: center; padding: 80px 20px; background: #222; border-radius: 8px; color: #666; border: 1px dashed #333; }
.empty-icon { font-size: 48px; margin-bottom: 16px; opacity: 0.2; }
</style>
