<template>
  <div class="profile-container">
    <div class="profile-card" v-if="authStore.user">
      <div class="profile-header">
        <div class="avatar-large">{{ authStore.user.username.charAt(0).toUpperCase() }}</div>
        <div class="user-info">
          <h1>{{ authStore.user.username }}</h1>
          <p class="bio" v-if="authStore.user.bio">{{ authStore.user.bio }}</p>
          <span class="badge premium" v-if="authStore.user.is_premium">PREMIUM</span>
          <span class="badge rating" v-if="authStore.user.rating != null">
            {{ Math.round(authStore.user.rating) }}
            <span class="rd" :title="`Rating deviation ${Math.round(authStore.user.rd)}`">±{{ Math.round(authStore.user.rd) }}</span>
          </span>
        </div>
      </div>

      <div class="stats-grid">
        <div class="stat-item">
          <div class="stat-value">{{ stats?.wins || 0 }}</div>
          <div class="stat-label">Wins</div>
        </div>
        <div class="stat-item">
          <div class="stat-value">{{ stats?.losses || 0 }}</div>
          <div class="stat-label">Losses</div>
        </div>
        <div class="stat-item">
          <div class="stat-value">{{ stats?.draws || 0 }}</div>
          <div class="stat-label">Draws</div>
        </div>
      </div>

      <div class="profile-actions">
        <button @click="logout" class="btn-logout">Logout</button>
      </div>
    </div>

    <!-- Active games. Listed here in addition to /match so the profile
         is a single landing for both "what am I playing now" and
         "what did I play before". Each link routes into the live game;
         delete tears the row down. Mirrors the Match.vue rendering
         deliberately so users see the same list shape across surfaces. -->
    <div v-if="authStore.user" class="past-card">
      <div class="past-header">
        <h2>Active games</h2>
        <span v-if="activeGames.length" class="muted">{{ activeGames.length }}</span>
      </div>
      <ul v-if="activeGames.length" class="game-list">
        <li v-for="g in activeGames" :key="g.id">
          <router-link :to="`/game/${g.id}`" class="game-link active">
            <span class="game-type">{{ g.white_user_id && g.black_user_id ? 'PvP' : 'Engine' }}</span>
            <span class="game-id">{{ shortId(g.id) }}</span>
            <span class="game-when">{{ formatDate(g.updated_at) }}</span>
            <span class="game-status">{{ formatStatus(g.status) }}</span>
          </router-link>
          <button class="btn-delete" :title="'Delete ' + g.id" @click="deleteGame(g.id)">×</button>
        </li>
      </ul>
      <p v-else class="empty">No games in progress.</p>
    </div>

    <!-- Past games. Moved here from /match so the matchmaking page
         stays focused on "find your next game" and the history lives
         where the user already goes for self-introspection. PGN-
         imported rows are flagged so it's clear they don't count
         toward the stat totals above. -->
    <div v-if="authStore.user" class="past-card">
      <div class="past-header">
        <h2>Past games</h2>
        <span v-if="finishedGames.length" class="muted">{{ finishedGames.length }}</span>
      </div>
      <ul v-if="finishedGames.length" class="game-list">
        <li v-for="g in finishedGames" :key="g.id">
          <a :href="`/api/replay.html?game_id=${g.id}`" target="_blank" class="game-link past">
            <span class="game-type">{{ g.white_user_id && g.black_user_id ? 'PvP' : 'Engine' }}</span>
            <span class="game-result" :class="resultClass(g)">{{ resultLabel(g) }}</span>
            <span v-if="g.imported" class="badge imported" title="Loaded from PGN — not counted in stats">Imported</span>
            <span class="game-when">{{ formatDate(g.updated_at) }}</span>
            <span class="game-status">{{ formatStatus(g.status) }}</span>
          </a>
          <button class="btn-delete" :title="'Delete ' + g.id" @click="deleteGame(g.id)">×</button>
        </li>
      </ul>
      <p v-else class="empty">No finished games yet — go play one.</p>
    </div>

    <div v-if="!authStore.user" class="loading-state">
      <div class="spinner"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();
const confirmStore = useConfirmStore();
const stats = ref<any>(null);
const games = ref<any[]>([]);

const loadStats = async () => {
  try {
    stats.value = await api.getUserStats();
  } catch (e) {
    console.error('Failed to load stats', e);
  }
};

const loadGames = async () => {
  try {
    games.value = (await api.listGames()) || [];
  } catch (e: any) {
    toastStore.error('Failed to load games: ' + (e?.message || e));
  }
};

const activeGames = computed(() => games.value.filter((g: any) => g.status === 'ongoing'));
// listGames returns updated_at DESC, so freshly-finished games sit at
// the top of this list — exactly what a "past games" view wants. Cap
// to 50 so a heavy user doesn't render a thousand-row table.
const finishedGames = computed(() => games.value.filter((g: any) => g.status !== 'ongoing').slice(0, 50));

const resultLabel = (g: any): string => {
  const me = authStore.user?.id;
  if (!me) return g.result || '*';
  if (g.result === '1/2-1/2') return 'Draw';
  if (g.result === '*' || !g.result) return '–';
  const winnerWhite = g.result === '1-0';
  const userIsWhite = g.white_user_id === me;
  const userIsBlack = g.black_user_id === me;
  if ((winnerWhite && userIsWhite) || (!winnerWhite && userIsBlack)) return 'Won';
  if (userIsWhite || userIsBlack) return 'Lost';
  return g.result;
};

const resultClass = (g: any): string => {
  const me = authStore.user?.id;
  if (!me || !g.result || g.result === '*') return '';
  if (g.result === '1/2-1/2') return 'draw';
  const winnerWhite = g.result === '1-0';
  if ((winnerWhite && g.white_user_id === me) || (!winnerWhite && g.black_user_id === me)) return 'won';
  return 'lost';
};

const deleteGame = async (id: string) => {
  const ok = await confirmStore.ask({
    title: 'Delete game',
    message: 'Delete this game from your history? This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true,
  });
  if (!ok) return;
  try {
    await api.deleteGame(id);
    toastStore.info('Game deleted');
    await loadGames();
  } catch {
    toastStore.error('Delete failed');
  }
};

const formatDate = (s: string) => new Date(s).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' });
const formatStatus = (s: string) => s.charAt(0).toUpperCase() + s.slice(1).replace('_', ' ');
const shortId = (id: string) => id.split('-')[0] + '…';

const logout = async () => {
  await authStore.logout();
  router.push('/login');
  toastStore.info('Logged out');
};

onMounted(async () => {
  await authStore.init();
  await Promise.all([loadStats(), loadGames()]);
});
</script>

<style scoped>
.profile-container { max-width: 800px; margin: 0 auto; padding: 60px 20px; display: flex; flex-direction: column; gap: 24px; }
.profile-card { background: #2b2b2b; border-radius: 16px; padding: 40px; border: 1px solid #3d3d3d; box-shadow: 0 10px 30px rgba(0,0,0,0.3); }

.profile-header { display: flex; align-items: center; gap: 30px; margin-bottom: 40px; }

.avatar-large { width: 100px; height: 100px; background: #4a6b8a; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 40px; font-weight: 700; color: white; }

.user-info h1 { margin: 0 0 8px; font-size: 32px; color: #fff; }
.bio { margin: 0 0 12px; color: #aaa; font-style: italic; }

.badge.premium { background: #d4af37; color: #1e1e1e; font-size: 10px; font-weight: 800; padding: 2px 8px; border-radius: 4px; letter-spacing: 1px; }
.badge.rating { background: #2a3340; color: #cfe1f5; border: 1px solid #3a4754; font-size: 12px; font-weight: 700; padding: 2px 8px; border-radius: 4px; margin-left: 6px; display: inline-flex; align-items: center; gap: 6px; }
.badge.rating .rd { color: #8a9bad; font-weight: 500; font-size: 11px; }
.badge.imported { background: #3a3220; color: #e4b15a; border: 1px solid #56492a; font-size: 10px; font-weight: 700; padding: 2px 6px; border-radius: 4px; letter-spacing: 0.4px; text-transform: uppercase; }

.stats-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 20px; margin-bottom: 40px; border-top: 1px solid #3d3d3d; border-bottom: 1px solid #3d3d3d; padding: 30px 0; }

.stat-item { text-align: center; }
.stat-value { font-size: 24px; font-weight: 700; color: #4ec77a; margin-bottom: 4px; }
.stat-label { font-size: 12px; color: #777; text-transform: uppercase; letter-spacing: 1px; }

.profile-actions { display: flex; justify-content: flex-end; }
.btn-logout { background: transparent; border: 1px solid #ff7575; color: #ff7575; padding: 10px 24px; border-radius: 6px; cursor: pointer; font-weight: 600; transition: all 0.2s; }
.btn-logout:hover { background: rgba(255, 117, 117, 0.1); }

/* Past games card. Compact list, mirrors the visual weight the
   matchmaking page used to give it. */
.past-card { background: #2b2b2b; border-radius: 16px; padding: 24px 32px; border: 1px solid #3d3d3d; }
.past-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 14px; }
.past-header h2 { margin: 0; font-size: 17px; color: #ddd; font-weight: 600; }
.muted { color: #777; font-size: 13px; }
.empty { color: #777; font-style: italic; padding: 8px 0; margin: 0; }

.game-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.game-list li { display: flex; align-items: center; gap: 8px; }
.game-link.past,
.game-link.active {
  flex: 1 1 auto;
  display: grid;
  align-items: center;
  gap: 10px;
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 6px;
  padding: 10px 12px;
  text-decoration: none;
  color: #ddd;
  font-size: 13px;
  transition: border-color 120ms ease;
}
.game-link.past { grid-template-columns: 60px 70px auto 1fr 120px; }
.game-link.active { grid-template-columns: 60px 100px 1fr 120px; }
.game-link.past:hover,
.game-link.active:hover { border-color: #4a6b8a; }
.game-id { font-family: ui-monospace, Menlo, monospace; font-size: 12px; color: #888; }
.game-type { font-weight: 600; color: #9bb3cc; font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; }
.game-when { color: #888; font-size: 12px; }
.game-status { color: #aaa; font-size: 12px; text-align: right; }
.game-result { font-weight: 700; font-size: 12px; }
.game-result.won { color: #6ec77a; }
.game-result.lost { color: #d4544c; }
.game-result.draw { color: #aaa; }

.btn-delete {
  background: transparent;
  border: 1px solid #3a3a3a;
  color: #888;
  width: 32px;
  height: 32px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 18px;
  line-height: 1;
}
.btn-delete:hover { border-color: #d4544c; color: #d4544c; }

.loading-state { display: flex; justify-content: center; align-items: center; min-height: 200px; }
.spinner { width: 40px; height: 40px; border: 4px solid rgba(255,255,255,0.1); border-top-color: #4a6b8a; border-radius: 50%; animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 600px) {
  .stats-grid { grid-template-columns: 1fr 1fr; gap: 30px; }
  .profile-header { flex-direction: column; text-align: center; }
  .game-link.past,
  .game-link.active { grid-template-columns: 60px 1fr; }
  .game-when, .game-status, .badge.imported, .game-id { display: none; }
}
</style>
