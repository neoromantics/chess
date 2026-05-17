<template>
  <div class="match-wrap">
    <header class="match-header">
      <h1>Match a Player</h1>
      <p class="lede">
        Get paired with another human via the queue, or
        <router-link to="/invites" class="inline-link">invite a friend by username</router-link>.
      </p>
    </header>

    <section class="card pairing">
      <div class="row tc-row">
        <button
          v-for="tc in timeControls"
          :key="tc.id"
          @click="!searching && (selectedTC = tc.id)"
          :class="['tc-pill', { active: selectedTC === tc.id }]"
          :disabled="searching"
        >
          <span class="tc-name">{{ tc.label }}</span>
          <span class="tc-desc">{{ tc.desc }}</span>
        </button>
      </div>

      <div class="actions">
        <button v-if="!searching" @click="joinQueue" :disabled="!selectedTC" class="btn-find">
          Find Game
        </button>
        <div v-else class="searching">
          <div class="spinner"></div>
          <span>{{ searchingLabel }}</span>
          <button @click="leaveQueue" class="btn-cancel">Cancel</button>
        </div>
      </div>
    </section>

    <section v-if="activeGames.length > 0" class="card recents">
      <div class="card-header">
        <h2>Your active games</h2>
        <span class="muted">{{ activeGames.length }}</span>
      </div>
      <ul class="game-list">
        <li v-for="g in activeGames" :key="g.id">
          <router-link :to="`/game/${g.id}`" class="game-link">
            <span class="game-type">{{ g.white_user_id && g.black_user_id ? 'PvP' : 'Engine' }}</span>
            <span class="game-id">{{ shortId(g.id) }}</span>
            <span class="game-when">{{ formatDate(g.updated_at) }}</span>
            <span class="game-status">{{ formatStatus(g.status) }}</span>
          </router-link>
          <button class="btn-delete" :title="'Delete ' + g.id" @click="deleteGame(g.id)">×</button>
        </li>
      </ul>
    </section>

    <p class="past-games-hint">
      Looking for finished games?
      <router-link to="/profile" class="inline-link">View past games on your profile</router-link>.
    </p>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';
import { useConfirmStore } from '../stores/confirm';
import { useUserEventsStore } from '../stores/userEvents';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();
const confirmStore = useConfirmStore();
const userEvents = useUserEventsStore();

const selectedTC = ref('10+0');
const searching = ref(false);
const games = ref<any[]>([]);

// Wait-progress label. Backend's engine-fallback cutoff is 60s; this
// gives the user a sense of how long they've been queued without
// surfacing the literal "engine fallback at 60s" mechanic. ~30s is
// long enough to feel patient, short enough that an empty queue
// doesn't read as broken before the message arrives.
const searchStartedAt = ref<number>(0);
const searchElapsed = ref(0);
let searchTickerHandle: number | undefined;
const searchingLabel = computed(() => {
  if (searchElapsed.value < 30) return 'Looking for an opponent…';
  if (searchElapsed.value < 60) return 'Still searching — most pairings take a moment.';
  return 'No opponent yet — we may match you with a practice partner shortly.';
});

// Trimmed to the modal Blitz + Rapid pair so the matchmaking pool
// stays concentrated. Every extra TC is a separate Redis queue, and
// with our active-player count splitting 10 ways meant most queues
// were always empty. Add entries back here AND to validTimeControl
// + supportedTCs (cmd/game/invites.go, matchmaker.go) when the
// playerbase justifies it.
const timeControls = [
  { id: '3+0',  label: '3+0',  desc: 'Blitz' },
  { id: '10+0', label: '10+0', desc: 'Rapid' },
];

const loadGames = async () => {
  try {
    const res = await api.listGames();
    games.value = res || [];
  } catch (e: any) {
    // Surface the failure instead of silently leaving both sections
    // v-if'd off — a 400 from /api/games hid a real backend regression
    // for an unknown stretch of time before e6fb7c4.
    toastStore.error('Failed to load games: ' + (e?.message || e));
  }
};

const activeGames = computed(() => games.value.filter((g: any) => g.status === 'ongoing'));

const startSearchTicker = () => {
  searchStartedAt.value = Date.now();
  searchElapsed.value = 0;
  if (searchTickerHandle) clearInterval(searchTickerHandle);
  searchTickerHandle = window.setInterval(() => {
    searchElapsed.value = Math.floor((Date.now() - searchStartedAt.value) / 1000);
  }, 1000);
};
const stopSearchTicker = () => {
  if (searchTickerHandle) {
    clearInterval(searchTickerHandle);
    searchTickerHandle = undefined;
  }
  searchElapsed.value = 0;
};

const joinQueue = async () => {
  if (!selectedTC.value) return;
  try {
    await api.joinQueue(selectedTC.value);
    searching.value = true;
    startSearchTicker();
    toastStore.info(`Searching for ${selectedTC.value} match…`);
  } catch (e: any) {
    toastStore.error('Failed to join queue: ' + (e?.message || e));
  }
};

const leaveQueue = async () => {
  try {
    await api.leaveQueue(selectedTC.value);
    searching.value = false;
    stopSearchTicker();
    toastStore.info('Matchmaking cancelled');
  } catch {
    toastStore.error('Failed to leave queue');
  }
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

let unsubs: (() => void)[] = [];

onMounted(async () => {
  await authStore.init();
  if (!authStore.user) {
    router.replace({ path: '/signup', query: { next: '/match' } });
    return;
  }
  await loadGames();
  userEvents.connect();
  unsubs.push(userEvents.on('match_found', (payload: any) => {
    searching.value = false;
    stopSearchTicker();
    toastStore.success(`Match found! You are playing ${payload.color}`);
    router.push(`/game/${payload.game_id}`);
  }));
});

onUnmounted(() => {
  unsubs.forEach(fn => fn());
  stopSearchTicker();
});
</script>

<style scoped>
.match-wrap {
  max-width: 720px;
  margin: 0 auto;
  padding: 56px 20px 40px;
  display: flex;
  flex-direction: column;
  gap: 28px;
}

.match-header h1 {
  margin: 0 0 8px;
  font-size: 32px;
  font-weight: 700;
  letter-spacing: -0.3px;
}
.lede {
  color: #aaa;
  margin: 0;
  font-size: 15px;
  line-height: 1.55;
}
.inline-link { color: #9bb3cc; text-decoration: none; }
.inline-link:hover { text-decoration: underline; }

.past-games-hint {
  margin: 0;
  color: #888;
  font-size: 13px;
  text-align: center;
  padding: 6px 0;
}

.card {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 12px;
  padding: 24px;
}

.tc-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 10px;
  margin-bottom: 18px;
}
.tc-pill {
  background: #1f1f1f;
  border: 1px solid #3a3a3a;
  border-radius: 8px;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  cursor: pointer;
  color: #ddd;
  text-align: center;
  font: inherit;
  transition: border-color 120ms ease, background-color 120ms ease;
}
.tc-pill:hover:not(:disabled) { border-color: #4a6b8a; background: #252525; }
.tc-pill.active { border-color: #4a6b8a; background: rgba(74,107,138,0.12); box-shadow: inset 0 0 0 1px #4a6b8a; }
.tc-pill:disabled { opacity: 0.45; cursor: not-allowed; }
.tc-name { font-weight: 700; font-size: 17px; color: #fff; }
.tc-desc { font-size: 11px; color: #888; }

.actions { display: flex; align-items: center; gap: 14px; }
.btn-find {
  background: #2d5a2d;
  color: #fff;
  border: 1px solid #3a703a;
  padding: 12px 28px;
  border-radius: 6px;
  font-weight: 700;
  font-size: 15px;
  cursor: pointer;
  transition: background-color 120ms ease;
}
.btn-find:hover:not(:disabled) { background: #347a34; }
.btn-find:disabled { opacity: 0.5; cursor: not-allowed; }

.searching { display: flex; align-items: center; gap: 12px; font-size: 14px; color: #ccc; }
.spinner { width: 18px; height: 18px; border: 2px solid rgba(255,255,255,0.1); border-top-color: #6a8aa6; border-radius: 50%; animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.btn-cancel { background: transparent; border: 1px solid #555; color: #ccc; padding: 6px 14px; border-radius: 6px; cursor: pointer; }
.btn-cancel:hover { background: #333; }

.recents .card-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 14px; }
.recents h2 { margin: 0; font-size: 17px; color: #ddd; font-weight: 600; }
.muted { color: #777; font-size: 13px; }

.game-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.game-list li { display: flex; align-items: center; gap: 8px; }
.game-link {
  flex: 1 1 auto;
  display: grid;
  grid-template-columns: 60px 100px 1fr 120px;
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
.game-link:hover { border-color: #4a6b8a; }
.game-type { font-weight: 600; color: #9bb3cc; font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; }
.game-id { font-family: ui-monospace, Menlo, monospace; font-size: 12px; color: #888; }
.game-when { color: #888; font-size: 12px; }
.game-status { color: #aaa; font-size: 12px; text-align: right; }
.game-link.past { grid-template-columns: 60px 70px 1fr 120px; }
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

@media (max-width: 600px) {
  .game-link { grid-template-columns: 60px 1fr; }
  .game-when, .game-status { display: none; }
}
</style>
