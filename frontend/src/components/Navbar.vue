<template>
  <nav class="navbar">
    <div class="nav-content">
      <router-link to="/" class="nav-brand">Chess</router-link>

      <div class="nav-links">
        <template v-if="authStore.user">
          <router-link v-if="authStore.user.is_admin" to="/admin" class="btn-text">Admin</router-link>

          <!-- Notification bell. Replaces the standalone "Invites" text
               link: same destination on full-click (the "View all"
               button or the bell itself when there's nothing pending),
               but the dropdown lets the user act on incoming invites
               without leaving whatever page they're on. Invites are
               already DB-backed, so the count survives reloads. -->
          <div class="bell-wrap" v-click-outside="closeBell">
            <button
              class="bell-btn"
              :class="{ active: bellOpen, has: inviteStore.pendingCount > 0 }"
              @click="toggleBell"
              :aria-label="inviteStore.pendingCount > 0 ? `${inviteStore.pendingCount} pending invites` : 'No pending invites'"
            >
              <svg
                class="bell-icon"
                aria-hidden="true"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="1.8"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <path d="M6 8a6 6 0 0 1 12 0c0 5 2 6 2 8H4c0-2 2-3 2-8z" />
                <path d="M10 19a2 2 0 1 0 4 0" />
              </svg>
              <span v-if="inviteStore.pendingCount > 0" class="badge">{{ inviteStore.pendingCount }}</span>
            </button>
            <div v-if="bellOpen" class="bell-panel">
              <header class="bell-header">
                <span class="bell-title">Invites</span>
                <span class="muted">{{ inviteStore.pendingCount }} pending</span>
              </header>
              <ul v-if="receivedPending.length" class="bell-list">
                <li v-for="inv in receivedPending" :key="inv.id" class="bell-item">
                  <div class="bell-item-text">
                    <strong>{{ inv.from_username || 'Unknown' }}</strong>
                    <span class="muted"> · {{ inv.time_control }}{{ inv.rated ? ' · rated' : '' }}</span>
                  </div>
                  <div class="bell-actions">
                    <button class="bell-btn-accept" :disabled="actingOn === inv.id" @click="onAccept(inv)">Accept</button>
                    <button class="bell-btn-decline" :disabled="actingOn === inv.id" @click="onDecline(inv)">Decline</button>
                  </div>
                </li>
              </ul>
              <p v-else class="bell-empty">No invites right now.</p>
              <footer class="bell-footer">
                <router-link to="/invites" class="bell-link" @click="closeBell">View all invites →</router-link>
              </footer>
            </div>
          </div>

          <router-link to="/study/" class="btn-text">Studies</router-link>
          <router-link to="/profile" class="user-link">
            <span class="user-greeting">Hi, <strong>{{ authStore.user.username }}</strong></span>
          </router-link>
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
import { computed, ref, type DirectiveBinding } from 'vue';
import { useAuthStore } from '../stores/auth';
import { useInviteStore } from '../stores/invites';
import { useRouter } from 'vue-router';
import { useToastStore } from '../stores/toast';
import type { InviteWire } from '../types';

const authStore = useAuthStore();
const inviteStore = useInviteStore();
const router = useRouter();
const toastStore = useToastStore();

const bellOpen = ref(false);
const actingOn = ref<string | null>(null);

const receivedPending = computed(() =>
  inviteStore.received.filter((i) => i.status === 'pending')
);

const toggleBell = () => {
  bellOpen.value = !bellOpen.value;
};
const closeBell = () => {
  bellOpen.value = false;
};

// vClickOutside — closes the dropdown when the user clicks elsewhere.
// Registered locally rather than as a plugin because no other
// component needs it; lifting to a plugin would be premature.
const vClickOutside = {
  mounted(el: HTMLElement, binding: DirectiveBinding<() => void>) {
    const handler = (e: MouseEvent) => {
      if (!el.contains(e.target as Node)) binding.value();
    };
    (el as any)._clickOutside = handler;
    document.addEventListener('click', handler);
  },
  unmounted(el: HTMLElement) {
    const handler = (el as any)._clickOutside;
    if (handler) document.removeEventListener('click', handler);
  },
};

const onAccept = async (inv: InviteWire) => {
  if (actingOn.value) return;
  actingOn.value = inv.id;
  try {
    const accepted = await inviteStore.accept(inv.id);
    closeBell();
    if (accepted?.game_id) {
      router.push(`/game/${accepted.game_id}`);
    }
  } catch (e: any) {
    toastStore.error('Could not accept invite: ' + (e?.message || e));
  } finally {
    actingOn.value = null;
  }
};

const onDecline = async (inv: InviteWire) => {
  if (actingOn.value) return;
  actingOn.value = inv.id;
  try {
    await inviteStore.decline(inv.id);
    toastStore.info('Invite declined');
  } catch (e: any) {
    toastStore.error('Could not decline invite: ' + (e?.message || e));
  } finally {
    actingOn.value = null;
  }
};

const handleLogout = async () => {
  await authStore.logout();
  toastStore.info('Logged out successfully');
  router.push('/login');
};
</script>

<style scoped>
/* Matches the standalone replay-nav so the chrome stays visually
   consistent across the SPA and the standalone replay bundle. Both
   live at 48px tall with the same border / padding / font weights;
   if either is reskinned, update the other to match. The 16px height
   reduction also frees up board space on short laptop screens — see
   the --sq computation in App.vue. */
.navbar { background: #2b2b2b; border-bottom: 1px solid #3d3d3d; height: 48px; display: flex; align-items: center; position: sticky; top: 0; z-index: 100; }
.nav-content { width: 100%; max-width: 1200px; margin: 0 auto; padding: 0 20px; display: flex; justify-content: space-between; align-items: center; }

.nav-brand { font-size: 18px; font-weight: 700; color: #fff; text-decoration: none; letter-spacing: -0.2px; }
.nav-brand:hover { color: #fff; }

.nav-links { display: flex; align-items: center; gap: 18px; }
.user-link { text-decoration: none; }
.user-greeting { font-size: 13px; color: #aaa; transition: color 0.2s; }
.user-link:hover .user-greeting { color: white; }

.btn-text { background: transparent; border: none; color: #aaa; font-size: 13px; cursor: pointer; padding: 4px 6px; transition: color 120ms ease; }
.btn-text:hover { color: white; }

.btn-primary-small { background: #4a6b8a; color: white; border: none; padding: 5px 14px; border-radius: 4px; cursor: pointer; font-weight: 600; font-size: 13px; text-decoration: none; }
.btn-primary-small:hover { background: #5a7b9a; }

.badge { background: #d4544c; color: white; font-size: 11px; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; display: inline-flex; align-items: center; justify-content: center; font-weight: 700; }

/* Notification bell + dropdown. The bell sits inline with the rest of
   the nav links; the dropdown is positioned absolutely beneath it so
   it overlays whatever's underneath instead of nudging layout. */
.bell-wrap { position: relative; display: inline-block; }
.bell-btn {
  background: transparent;
  border: 1px solid transparent;
  color: #aaa;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 14px;
  line-height: 1;
  position: relative;
  transition: background-color 120ms ease, border-color 120ms ease, color 120ms ease;
}
.bell-btn:hover { background: #333; color: #fff; }
.bell-btn.active { background: #333; border-color: #4a6b8a; color: #fff; }
.bell-icon { width: 18px; height: 18px; display: inline-block; vertical-align: middle; color: inherit; }
.bell-btn.has { color: #f0d27a; }
.bell-btn.has:hover { color: #f5dc94; }
.bell-btn .badge { position: absolute; top: -4px; right: -4px; }

.bell-panel {
  position: absolute;
  top: 110%;
  right: 0;
  z-index: 200;
  width: 320px;
  max-width: calc(100vw - 24px);
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 8px;
  box-shadow: 0 10px 30px rgba(0,0,0,0.5);
  overflow: hidden;
}
.bell-header { display: flex; align-items: baseline; justify-content: space-between; padding: 12px 14px 8px; border-bottom: 1px solid #333; }
.bell-title { font-weight: 600; font-size: 14px; color: #ddd; }
.muted { color: #777; font-size: 12px; }
.bell-list { list-style: none; margin: 0; padding: 6px 0; max-height: 320px; overflow-y: auto; }
.bell-item { display: flex; align-items: center; gap: 10px; padding: 8px 14px; border-bottom: 1px solid #2a2a2a; }
.bell-item:last-child { border-bottom: none; }
.bell-item-text { flex: 1 1 auto; min-width: 0; font-size: 13px; color: #ddd; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bell-actions { display: inline-flex; gap: 6px; flex: 0 0 auto; }
.bell-btn-accept, .bell-btn-decline {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  cursor: pointer;
  border: 1px solid;
  font-weight: 600;
  transition: background-color 120ms ease, border-color 120ms ease;
}
.bell-btn-accept { background: #2d4a2d; border-color: #3a703a; color: #b6e3b6; }
.bell-btn-accept:hover:not(:disabled) { background: #3a6b3a; }
.bell-btn-decline { background: transparent; border-color: #555; color: #aaa; }
.bell-btn-decline:hover:not(:disabled) { background: #333; color: #ddd; }
.bell-btn-accept:disabled, .bell-btn-decline:disabled { opacity: 0.5; cursor: not-allowed; }

.bell-empty { color: #888; font-style: italic; padding: 14px; margin: 0; font-size: 13px; text-align: center; }
.bell-footer { border-top: 1px solid #333; padding: 10px 14px; text-align: right; }
.bell-link { color: #9bb3cc; text-decoration: none; font-size: 12px; font-weight: 600; }
.bell-link:hover { text-decoration: underline; }
</style>
