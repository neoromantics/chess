<template>
  <nav class="navbar">
    <div class="nav-content">
      <router-link to="/" class="nav-brand">
        <span class="logo">♞</span> Chess Platform
      </router-link>
      
      <div class="nav-links">
        <template v-if="authStore.user">
          <router-link to="/invites" class="btn-text invites-link">
            Invites
            <span v-if="inviteStore.pendingCount > 0" class="badge">{{ inviteStore.pendingCount }}</span>
          </router-link>
          <span class="user-greeting">Welcome, <strong>{{ authStore.user.username }}</strong></span>
          <button @click="handleLogout" class="btn-text">Logout</button>
        </template>
        <template v-else>
          <router-link to="/login" class="btn-text">Login</router-link>
          <router-link to="/signup" class="btn-primary-small">Sign Up</router-link>
        </template>
      </div>
    </div>
  </nav>
</template>

<script setup lang="ts">
import { useAuthStore } from '../stores/auth';
import { useInviteStore } from '../stores/invites';
import { useRouter } from 'vue-router';
import { useToastStore } from '../stores/toast';

const authStore = useAuthStore();
const inviteStore = useInviteStore();
const router = useRouter();
const toastStore = useToastStore();

const handleLogout = async () => {
  await authStore.logout();
  toastStore.info('Logged out successfully');
  router.push('/login');
};
</script>

<style scoped>
.navbar { background: #2b2b2b; border-bottom: 1px solid #333; height: 64px; display: flex; align-items: center; position: sticky; top: 0; z-index: 100; }
.nav-content { width: 100%; max-width: 1200px; margin: 0 auto; padding: 0 20px; display: flex; justify-content: space-between; align-items: center; }

.nav-brand { font-size: 20px; font-weight: 700; color: #ddd; text-decoration: none; display: flex; align-items: center; gap: 8px; }
.logo { font-size: 24px; color: #4a6b8a; }

.nav-links { display: flex; align-items: center; gap: 20px; }
.user-greeting { font-size: 14px; color: #aaa; }

.btn-text { background: transparent; border: none; color: #aaa; font-size: 14px; cursor: pointer; padding: 4px 8px; }
.btn-text:hover { color: white; }

.btn-primary-small { background: #4a6b8a; color: white; border: none; padding: 6px 16px; border-radius: 4px; cursor: pointer; font-weight: 600; font-size: 14px; text-decoration: none; }
.btn-primary-small:hover { background: #5a7b9a; }

.invites-link { display: inline-flex; align-items: center; gap: 6px; }
.badge { background: #d4544c; color: white; font-size: 11px; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; display: inline-flex; align-items: center; justify-content: center; font-weight: 700; }
</style>
