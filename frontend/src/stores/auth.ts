import { defineStore } from 'pinia';
import { api } from '../api';
import { useUserEventsStore } from './userEvents';
import { useInviteStore } from './invites';

// authStore owns the user session and is the single trigger point for
// opening/closing the /ws/user WebSocket. We open it whenever we know
// the user is signed in (on init() or setUser()), and close it on
// logout(). Feature stores (invites, future: friends, presence) hang
// their handlers off userEventsStore so we only ever have one socket
// per tab regardless of how many stores want events.

export const useAuthStore = defineStore('auth', {
  state: () => ({
    user: null as { id: number; username: string } | null,
    initialized: false,
  }),
  actions: {
    async init() {
      if (this.initialized) return;
      try {
        const user = await api.getMe();
        this.user = user;
      } catch (e) {
        this.user = null;
      } finally {
        this.initialized = true;
      }
      if (this.user) this.activateUserChannel();
    },
    setUser(user: any) {
      this.user = user;
      this.activateUserChannel();
    },
    activateUserChannel() {
      const events = useUserEventsStore();
      events.reset();
      events.connect();
      // Seed the invite cache so reconnects don't drop pending state.
      const invites = useInviteStore();
      invites.init();
    },
    async logout() {
      try {
        await api.logout();
      } finally {
        this.user = null;
        useUserEventsStore().disconnect();
        useInviteStore().reset();
      }
    }
  }
});
