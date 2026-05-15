<template>
  <div class="landing-wrap">
    <div class="hero">
      <h1>Chess</h1>
      <p class="tagline">
        Play and study chess. Sign in to save your games and rated PvP;
        anonymous play is unsaved and disappears after 10 minutes of
        inactivity.
      </p>
      <div v-if="!authReady" class="hint">Loading…</div>
    </div>

    <div v-if="authReady" class="cards">
      <button class="mode-card primary" :disabled="busyMode !== ''" @click="goEngine">
        <div class="mode-icon">♟</div>
        <div class="mode-body">
          <div class="mode-title">Play vs Engine</div>
          <div class="mode-sub">
            Train, study endgames, or play casually against the
            built-in engine. No sign-in required.
          </div>
          <div class="mode-meta">
            {{ authStore.user ? 'Saved to your profile' : 'Anonymous (10-minute session)' }}
          </div>
        </div>
        <div v-if="busyMode === 'engine'" class="spinner"></div>
      </button>

      <button class="mode-card primary" :disabled="busyMode !== ''" @click="goMatch">
        <div class="mode-icon">⚔</div>
        <div class="mode-body">
          <div class="mode-title">Match a Player</div>
          <div class="mode-sub">
            Get paired with another human via matchmaking, or invite a
            friend by username.
          </div>
          <div class="mode-meta">
            {{ authStore.user ? 'Open matchmaking' : 'Sign in required' }}
          </div>
        </div>
        <div v-if="busyMode === 'match'" class="spinner"></div>
      </button>

      <button class="mode-card secondary" disabled>
        <div class="mode-icon">⚙</div>
        <div class="mode-body">
          <div class="mode-title">Board Editor <span class="soon">soon</span></div>
          <div class="mode-sub">
            Set up arbitrary positions, paste FENs, study endgames
            from the start position of your choice.
          </div>
          <div class="mode-meta">Coming back in a future release.</div>
        </div>
      </button>
    </div>

    <div v-if="authReady && !authStore.user" class="auth-row">
      <router-link to="/login" class="auth-link">Log in</router-link>
      <span class="auth-sep">·</span>
      <router-link to="/signup" class="auth-link">Create account</router-link>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();

const authReady = ref(false);
// Locks both buttons while one is in flight so users can't double-click
// themselves into two simultaneous game creations.
const busyMode = ref<'' | 'engine' | 'match'>('');

onMounted(async () => {
  await authStore.init();
  authReady.value = true;
});

const goEngine = async () => {
  if (busyMode.value !== '') return;
  busyMode.value = 'engine';
  try {
    if (authStore.user) {
      // Signed-in: durable game saved to PG. createGame returns
      // {game_id} after the gateway dispatches the CreateGame
      // intent; the row exists by the time GameView's first
      // /api/state lands.
      const res = await api.createGame({});
      router.replace(`/game/${res.game_id}`);
    } else {
      // Anonymous: temp session (Redis-only, 10-min sliding TTL).
      // Idempotent — a returning visitor with an active cookie gets
      // the same game ID.
      const state = await api.tempSession();
      if (!state?.id) throw new Error('temp session did not return a game id');
      router.replace(`/play/${state.id}`);
    }
  } catch (e: any) {
    toastStore.error('Could not start a game: ' + (e?.message || e));
    busyMode.value = '';
  }
};

const goMatch = () => {
  if (busyMode.value !== '') return;
  busyMode.value = 'match';
  if (authStore.user) {
    router.replace('/match');
  } else {
    // Stash the intent so the post-signup redirect lands on the
    // matchmaking page instead of dropping the user back on /.
    router.replace({ path: '/signup', query: { next: '/match' } });
  }
};
</script>

<style scoped>
.landing-wrap {
  max-width: 920px;
  margin: 0 auto;
  padding: 56px 20px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 36px;
  min-height: calc(100vh - 64px);
}

.hero {
  text-align: center;
  max-width: 640px;
}
.hero h1 {
  font-size: 42px;
  margin: 0 0 12px;
  font-weight: 700;
  letter-spacing: -0.5px;
}
.tagline {
  color: #aaa;
  font-size: 16px;
  line-height: 1.6;
  margin: 0;
}
.hint {
  margin-top: 16px;
  color: #888;
  font-style: italic;
}

.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 16px;
  width: 100%;
}

.mode-card {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 16px;
  text-align: left;
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 12px;
  padding: 22px 22px;
  color: #e6e6e6;
  cursor: pointer;
  font: inherit;
  transition: border-color 120ms ease, background-color 120ms ease, transform 120ms ease;
}
.mode-card.primary:hover { border-color: #4a6b8a; background: #2f3540; transform: translateY(-1px); }
.mode-card:disabled { cursor: not-allowed; opacity: 0.55; }
.mode-card.primary:disabled { opacity: 0.7; }

.mode-icon {
  font-size: 32px;
  line-height: 1;
  flex: 0 0 auto;
  width: 44px;
  height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #1f1f1f;
  border-radius: 10px;
}
.mode-body { flex: 1 1 auto; min-width: 0; }
.mode-title { font-size: 18px; font-weight: 600; margin-bottom: 6px; }
.mode-sub { color: #aaa; font-size: 13px; line-height: 1.5; margin-bottom: 8px; }
.mode-meta { color: #6a8aa6; font-size: 12px; font-weight: 500; }
.mode-card.secondary .mode-meta { color: #888; }

.soon {
  display: inline-block;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.6px;
  text-transform: uppercase;
  padding: 2px 6px;
  border-radius: 4px;
  background: #3a3a3a;
  color: #aaa;
  margin-left: 6px;
  vertical-align: middle;
}

.spinner {
  position: absolute;
  top: 18px;
  right: 18px;
  width: 18px;
  height: 18px;
  border: 2px solid rgba(255,255,255,0.15);
  border-top-color: #6a8aa6;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }

.auth-row {
  color: #888;
  font-size: 14px;
}
.auth-link { color: #9bb3cc; text-decoration: none; }
.auth-link:hover { text-decoration: underline; }
.auth-sep { padding: 0 8px; color: #555; }
</style>
