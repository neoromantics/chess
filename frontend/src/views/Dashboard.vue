<template>
  <div class="dashboard-container">
    <div class="header">
      <h1>Chess Platform</h1>
      <div class="user-info" v-if="authStore.user">
        <span>Logged in as <strong>{{ authStore.user.username }}</strong></span>
        <button @click="logout" class="btn-secondary">Logout</button>
      </div>
      <div v-else class="auth-links">
        <router-link to="/login" class="btn-primary">Login</router-link>
        <router-link to="/signup" class="btn-secondary">Sign Up</router-link>
      </div>
    </div>

    <div class="main-content">
      <section class="sessions-section">
        <div class="section-header">
          <h2>Your Games</h2>
          <button @click="createNewGame" class="btn-primary" :disabled="loading">
            {{ loading ? 'Starting...' : 'New Game +' }}
          </button>
        </div>

        <div v-if="games.length === 0" class="empty-state">
          <p>You don't have any active games yet. Start one now!</p>
        </div>

        <div class="game-list" v-else>
          <div v-for="g in games" :key="g.id" class="game-card">
            <div class="game-main">
              <div class="game-info">
                <span class="game-label">Status: {{ formatStatus(g.status) }}</span>
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
import { authStore } from '../auth';

const router = useRouter();
const games = ref<any[]>([]);
const loading = ref(false);

const loadGames = async () => {
  try {
    const res = await api.listGames();
    // Support both string IDs (guests) and rich records (logged in)
    games.value = res.map(g => typeof g === 'string' ? { id: g, status: 'ongoing', updated_at: new Date().toISOString() } : g);
  } catch (e) {
    console.error('Failed to load games', e);
  }
};

const createNewGame = async () => {
  loading.value = true;
  try {
    const res = await api.createGame();
    router.push(`/game/${res.game_id}`);
  } catch (e) {
    alert('Failed to create game');
  } finally {
    loading.value = false;
  }
};

const deleteGame = async (id: string) => {
  if (!confirm('Are you sure you want to delete this game?')) return;
  try {
    await api.deleteGame(id);
    await loadGames();
  } catch (e) {
    alert('Failed to delete game');
  }
};

const logout = async () => {
  await authStore.logout();
  router.push('/login');
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
.dashboard-container { max-width: 900px; margin: 0 auto; padding: 40px 20px; }

.header { display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #333; padding-bottom: 20px; margin-bottom: 40px; }
h1 { margin: 0; font-size: 28px; color: #ddd; }

.user-info { display: flex; align-items: center; gap: 16px; font-size: 14px; }
.auth-links { display: flex; gap: 12px; }

.section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 24px; }
h2 { margin: 0; font-size: 20px; color: #aaa; }

.game-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 16px; }

.game-card { background: #2b2b2b; border-radius: 8px; padding: 16px; border-left: 4px solid #4a6b8a; transition: transform 0.1s; }
.game-card:hover { transform: translateY(-2px); }

.game-main { display: flex; justify-content: space-between; align-items: center; }

.game-info { display: flex; flex-direction: column; gap: 4px; }
.game-label { font-size: 11px; font-weight: 700; color: #4ec77a; }
.game-id { font-family: ui-monospace, monospace; font-size: 11px; color: #666; }
.game-date { font-size: 11px; color: #888; }

.game-actions { display: flex; align-items: center; gap: 12px; }

.btn-primary { background: #4a6b8a; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: 600; text-decoration: none; }
.btn-primary:hover { background: #5a7b9a; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-secondary { background: transparent; border: 1px solid #555; color: #ddd; padding: 8px 16px; border-radius: 4px; cursor: pointer; text-decoration: none; }
.btn-secondary:hover { background: #333; }

.btn-action { background: #2d5a2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-size: 14px; font-weight: 600; text-decoration: none; }
.btn-action:hover { background: #3a6b3a; }

.btn-delete { background: transparent; border: none; color: #666; font-size: 20px; padding: 0 4px; line-height: 1; border-radius: 4px; }
.btn-delete:hover { color: #ff7575; background: rgba(255, 117, 117, 0.1); }

.empty-state { text-align: center; padding: 60px; background: #222; border-radius: 8px; color: #666; border: 1px dashed #333; }
</style>
