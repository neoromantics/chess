<template>
  <div class="landing">
    <div class="spinner"></div>
    <p>Setting up your game…</p>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';

const router = useRouter();
const authStore = useAuthStore();
const toastStore = useToastStore();

// Landing decides between three destinations:
//   - signed-in user        -> /dashboard
//   - anonymous visitor     -> /play/<temp-id> after creating a session
//   - anonymous visitor with an active temp game -> same /play/<id>
//     (POST /api/temp/session is idempotent and refreshes the TTL)
onMounted(async () => {
  await authStore.init();
  if (authStore.user) {
    router.replace('/dashboard');
    return;
  }
  try {
    const state = await api.tempSession();
    if (!state?.id) {
      throw new Error('temp session did not return a game id');
    }
    router.replace(`/play/${state.id}`);
  } catch (e: any) {
    toastStore.error('Could not start a game: ' + (e?.message || e));
  }
});
</script>

<style scoped>
.landing { display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: calc(100vh - 64px); gap: 16px; color: #888; }
.spinner { width: 32px; height: 32px; border: 3px solid rgba(255,255,255,0.1); border-top-color: #4a6b8a; border-radius: 50%; animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
