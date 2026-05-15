<template>
  <div class="dashboard-container">
    <div class="main-content">
      <div class="dashboard-grid">
        <!-- Left: Matchmaking & Play -->
        <section class="play-section">
          <div class="section-header">
            <h2>Play</h2>
            <div class="user-rating" v-if="authStore.user">
              Rating: <strong>{{ Math.round(authStore.user.rating || 1500) }}</strong>
            </div>
          </div>

          <div class="matchmaking-card">
            <h3>Quick Pairing</h3>
            <p class="subtitle">Join the queue to find an opponent</p>
            
            <div class="tc-grid">
              <button 
                v-for="tc in timeControls" 
                :key="tc.id" 
                @click="selectedTC = tc.id"
                :class="['tc-btn', { active: selectedTC === tc.id }]"
                :disabled="searching"
              >
                <div class="tc-name">{{ tc.label }}</div>
                <div class="tc-desc">{{ tc.desc }}</div>
              </button>
            </div>

            <div class="mm-actions">
              <button 
                v-if="!searching" 
                @click="joinQueue" 
                class="btn-play-now"
                :disabled="!selectedTC"
              >
                Find Game
              </button>
              <div v-else class="searching-status">
                <div class="spinner"></div>
                <span>Searching for match...</span>
                <button @click="leaveQueue" class="btn-cancel-mm">Cancel</button>
              </div>
            </div>
          </div>

          <div class="engine-play-card">
            <h3>Play Computer</h3>
            <div class="new-game-controls">
              <label class="inline-label">Engine think
                <select v-model.number="newGameThinkTime">
                  <option :value="100">0.1s (weak)</option>
                  <option :value="300">0.3s</option>
                  <option :value="1000">1s</option>
                  <option :value="3000">3s</option>
                  <option :value="10000">10s (strong)</option>
                </select>
              </label>
              <button @click="createNewGame" class="btn-primary" :disabled="loading">
                {{ loading ? 'Starting...' : 'New Engine Game' }}
              </button>
            </div>
          </div>
        </section>

        <!-- Right: Active Games -->
        <section class="sessions-section">
          <div class="section-header">
            <h2>Active Games</h2>
            <span class="badge">{{ games.length }}</span>
          </div>

          <div v-if="games.length === 0" class="empty-state">
            <div class="empty-icon">♟</div>
            <p>No active games.</p>
          </div>

          <div class="game-list" v-else>
            <div v-for="g in games" :key="g.id" class="game-card">
              <div class="game-main">
                <div class="game-info">
                  <span :class="['game-label', g.status]">
                    {{ g.white_user_id && g.black_user_id ? 'PvP' : 'Engine' }} · {{ formatStatus(g.status) }}
                  </span>
                  <span class="game-id">{{ truncateId(g.id) }}</span>
                  <span class="game-date">{{ formatDate(g.updated_at) }}</span>
                </div>
                <div class="game-actions">
                  <router-link :to="'/game/' + g.id" class="btn-action">Resume</router-link>
                  <button type="button" @click.stop.prevent="deleteGame(g.id)" class="btn-delete" title="Delete game">×</button>
                </div>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';
import { useUserEventsStore } from '../stores/userEvents';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();
const userEvents = useUserEventsStore();

const games = ref<any[]>([]);
const loading = ref(false);
const searching = ref(false);
const selectedTC = ref('15+10');

const timeControls = [
  // Single rapid time control while we land core multiplayer
  // correctness; bullet/blitz come back later. See ROADMAP.md.
  { id: '15+10', label: '15+10', desc: 'Rapid' },
];

const THINK_KEY = 'chess-new-game-think';
const newGameThinkTime = ref(Number(localStorage.getItem(THINK_KEY)) || 1000);
watch(newGameThinkTime, (v) => localStorage.setItem(THINK_KEY, String(v)));

const loadGames = async () => {
  try {
    const res = await api.listGames();
    games.value = res.map(g => typeof g === 'string' ? { id: g, status: 'ongoing', updated_at: new Date().toISOString() } : g);
  } catch (e) {
    toastStore.error('Failed to load games');
  }
};

const joinQueue = async () => {
  if (!selectedTC.value) return;
  try {
    await api.joinQueue(selectedTC.value);
    searching.value = true;
    toastStore.info(`Searching for ${selectedTC.value} match...`);
  } catch (e: any) {
    toastStore.error('Failed to join queue: ' + e.message);
  }
};

const leaveQueue = async () => {
  if (!selectedTC.value) return;
  try {
    await api.leaveQueue(selectedTC.value);
    searching.value = false;
    toastStore.info('Matchmaking cancelled');
  } catch (e: any) {
    toastStore.error('Failed to leave queue');
  }
};

const createNewGame = async () => {
  loading.value = true;
  try {
    const res = await api.createGame({
      white_think_time: newGameThinkTime.value,
      black_think_time: newGameThinkTime.value,
    });
    toastStore.success('New game created');
    router.push(`/game/${res.game_id}`);
  } catch (e: any) {
    if (String(e?.message).includes('authentication required')) {
      router.push('/login');
    } else {
      toastStore.error('Failed to create game');
    }
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

let unsubs: (() => void)[] = [];

onMounted(async () => {
  await authStore.init();
  await loadGames();
  
  userEvents.connect();
  unsubs.push(userEvents.on('match_found', (payload: any) => {
    searching.value = false;
    toastStore.success(`Match found! You are playing ${payload.color}`);
    router.push(`/game/${payload.game_id}`);
  }));
});

onUnmounted(() => {
  unsubs.forEach(fn => fn());
});
</script>

<style scoped>
.dashboard-container { max-width: 1200px; margin: 0 auto; padding: 40px 20px; }

.dashboard-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 40px; }

@media (max-width: 900px) {
  .dashboard-grid { grid-template-columns: 1fr; }
}

section { margin-bottom: 40px; }

.section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; gap: 16px; }
h2 { margin: 0; font-size: 22px; color: #fff; font-weight: 700; }
h3 { margin: 0 0 16px; font-size: 16px; color: #aaa; text-transform: uppercase; letter-spacing: 1px; }

.user-rating { background: #333; padding: 4px 12px; border-radius: 20px; font-size: 14px; color: #ccc; }
.user-rating strong { color: #4ec77a; }

.matchmaking-card, .engine-play-card { background: #2b2b2b; padding: 24px; border-radius: 12px; border: 1px solid #3d3d3d; margin-bottom: 20px; }

.subtitle { font-size: 14px; color: #777; margin-bottom: 20px; }

.tc-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-bottom: 24px; }
.tc-btn { background: #1e1e1e; border: 1px solid #444; padding: 12px; border-radius: 8px; text-align: center; transition: all 0.2s; }
.tc-btn:hover:not(:disabled) { border-color: #4a6b8a; background: #252525; }
.tc-btn.active { border-color: #4a6b8a; background: rgba(74, 107, 138, 0.1); box-shadow: inset 0 0 0 1px #4a6b8a; }
.tc-btn:disabled { opacity: 0.5; cursor: not-allowed; }

.tc-name { font-weight: 700; font-size: 16px; color: #fff; margin-bottom: 2px; }
.tc-desc { font-size: 11px; color: #666; }

.btn-play-now { width: 100%; background: #4a6b8a; color: white; border: none; padding: 14px; border-radius: 8px; cursor: pointer; font-weight: 700; font-size: 16px; transition: background 0.2s; }
.btn-play-now:hover:not(:disabled) { background: #5a7b9a; }
.btn-play-now:disabled { opacity: 0.5; cursor: not-allowed; }

.searching-status { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 10px; background: rgba(78, 199, 122, 0.05); border-radius: 8px; border: 1px dashed #4ec77a33; }
.searching-status span { font-size: 14px; color: #4ec77a; font-weight: 600; }

.spinner { width: 24px; height: 24px; border: 3px solid rgba(78, 199, 122, 0.1); border-top-color: #4ec77a; border-radius: 50%; animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.btn-cancel-mm { background: transparent; border: 1px solid #ff7575; color: #ff7575; padding: 6px 16px; border-radius: 4px; cursor: pointer; font-size: 13px; font-weight: 600; }
.btn-cancel-mm:hover { background: rgba(255, 117, 117, 0.1); }

.engine-play-card h3 { margin-bottom: 20px; }
.new-game-controls { display: flex; align-items: center; justify-content: space-between; gap: 12px; }

.badge { background: #444; color: #fff; padding: 2px 8px; border-radius: 10px; font-size: 12px; font-weight: 700; }

.game-list { display: flex; flex-direction: column; gap: 12px; }
.game-card { background: #2b2b2b; border-radius: 8px; padding: 16px; border: 1px solid #3d3d3d; transition: all 0.1s; }
.game-card:hover { border-color: #555; background: #323232; }

.game-main { display: flex; justify-content: space-between; align-items: center; }
.game-info { display: flex; flex-direction: column; gap: 4px; }
.game-label { font-size: 12px; font-weight: 700; color: #aaa; text-transform: uppercase; letter-spacing: 0.5px; }
.game-label.ongoing { color: #4ec77a; }
.game-id { font-family: ui-monospace, monospace; font-size: 11px; color: #555; }
.game-date { font-size: 11px; color: #666; }

.game-actions { display: flex; align-items: center; gap: 12px; }
.btn-action { background: #2d5a2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-size: 13px; font-weight: 700; text-decoration: none; }
.btn-action:hover { background: #3a6b3a; }

.btn-delete { background: transparent; border: none; color: #444; font-size: 20px; padding: 0 4px; line-height: 1; border-radius: 4px; cursor: pointer; }
.btn-delete:hover { color: #ff7575; background: rgba(255, 117, 117, 0.05); }

.empty-state { text-align: center; padding: 60px 20px; background: #222; border-radius: 12px; color: #555; border: 1px dashed #333; }
.empty-icon { font-size: 32px; margin-bottom: 12px; opacity: 0.1; }
</style>
