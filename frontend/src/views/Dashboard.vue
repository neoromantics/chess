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
          <h2>Your Active Games</h2>
          <button @click="createNewGame" class="btn-primary" :disabled="loading">
            {{ loading ? 'Starting...' : 'New Game +' }}
          </button>
        </div>

        <div v-if="games.length === 0" class="empty-state">
          <p>You don't have any active games yet. Start one now!</p>
        </div>

        <div class="game-list" v-else>
          <div v-for="id in games" :key="id" class="game-card">
            <div class="game-info">
              <span class="game-label">Game Session</span>
              <span class="game-id">{{ id }}</span>
            </div>
            <router-link :to="'/game/' + id" class="btn-action">Resume</router-link>
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
const games = ref<string[]>([]);
const loading = ref(false);

const loadGames = async () => {
  try {
    games.value = await api.listGames();
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

const logout = async () => {
  await authStore.logout();
  router.push('/login');
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

.game-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }

.game-card { background: #2b2b2b; border-radius: 8px; padding: 20px; display: flex; justify-content: space-between; align-items: center; border-left: 4px solid #4a6b8a; }

.game-info { display: flex; flex-direction: column; gap: 4px; }
.game-label { font-size: 10px; text-transform: uppercase; color: #666; font-weight: 700; }
.game-id { font-family: ui-monospace, monospace; font-size: 12px; color: #ddd; }

.btn-primary { background: #4a6b8a; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: 600; text-decoration: none; }
.btn-primary:hover { background: #5a7b9a; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

.btn-secondary { background: transparent; border: 1px solid #555; color: #ddd; padding: 8px 16px; border-radius: 4px; cursor: pointer; text-decoration: none; }
.btn-secondary:hover { background: #333; }

.btn-action { background: #2d5a2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-size: 14px; font-weight: 600; text-decoration: none; }
.btn-action:hover { background: #3a6b3a; }

.empty-state { text-align: center; padding: 60px; background: #222; border-radius: 8px; color: #666; border: 1px dashed #333; }
</style>
