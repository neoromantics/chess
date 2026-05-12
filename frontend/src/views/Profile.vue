<template>
  <div class="profile-container">
    <div class="profile-card" v-if="authStore.user">
      <div class="profile-header">
        <div class="avatar-large">{{ authStore.user.username.charAt(0).toUpperCase() }}</div>
        <div class="user-info">
          <h1>{{ authStore.user.username }}</h1>
          <p class="bio" v-if="authStore.user.bio">{{ authStore.user.bio }}</p>
          <span class="badge premium" v-if="authStore.user.is_premium">PREMIUM</span>
        </div>
      </div>

      <div class="stats-grid">
        <div class="stat-item">
          <div class="stat-value">{{ Math.round(authStore.user.rating || 1500) }}</div>
          <div class="stat-label">Rating (±{{ Math.round(authStore.user.rd || 350) }})</div>
        </div>
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
    
    <div v-else class="loading-state">
      <div class="spinner"></div>
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
const stats = ref<any>(null);

const loadStats = async () => {
  try {
    stats.value = await api.getUserStats();
  } catch (e) {
    console.error('Failed to load stats', e);
  }
};

const logout = async () => {
  await authStore.logout();
  router.push('/login');
  toastStore.info('Logged out');
};

onMounted(async () => {
  await authStore.init();
  await loadStats();
});
</script>

<style scoped>
.profile-container { max-width: 800px; margin: 0 auto; padding: 60px 20px; }
.profile-card { background: #2b2b2b; border-radius: 16px; padding: 40px; border: 1px solid #3d3d3d; box-shadow: 0 10px 30px rgba(0,0,0,0.3); }

.profile-header { display: flex; align-items: center; gap: 30px; margin-bottom: 40px; }

.avatar-large { width: 100px; height: 100px; background: #4a6b8a; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 40px; font-weight: 700; color: white; }

.user-info h1 { margin: 0 0 8px; font-size: 32px; color: #fff; }
.bio { margin: 0 0 12px; color: #aaa; font-style: italic; }

.badge.premium { background: #d4af37; color: #1e1e1e; font-size: 10px; font-weight: 800; padding: 2px 8px; border-radius: 4px; letter-spacing: 1px; }

.stats-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 20px; margin-bottom: 40px; border-top: 1px solid #3d3d3d; border-bottom: 1px solid #3d3d3d; padding: 30px 0; }

.stat-item { text-align: center; }
.stat-value { font-size: 24px; font-weight: 700; color: #4ec77a; margin-bottom: 4px; }
.stat-label { font-size: 12px; color: #777; text-transform: uppercase; letter-spacing: 1px; }

.profile-actions { display: flex; justify-content: flex-end; }
.btn-logout { background: transparent; border: 1px solid #ff7575; color: #ff7575; padding: 10px 24px; border-radius: 6px; cursor: pointer; font-weight: 600; transition: all 0.2s; }
.btn-logout:hover { background: rgba(255, 117, 117, 0.1); }

.loading-state { display: flex; justify-content: center; align-items: center; min-height: 200px; }
.spinner { width: 40px; height: 40px; border: 4px solid rgba(255,255,255,0.1); border-top-color: #4a6b8a; border-radius: 50%; animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 600px) {
  .stats-grid { grid-template-columns: 1fr 1fr; gap: 30px; }
  .profile-header { flex-direction: column; text-align: center; }
}
</style>
