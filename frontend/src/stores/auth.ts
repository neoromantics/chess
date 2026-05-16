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
    user: null as {
      id: number;
      username: string;
      rating: number;
      rd: number;
      volatility: number;
      is_premium: boolean;
      bio: string;
      // Flipped manually by SQL (no signup path sets it); gates the
      // /admin route + the conditional navbar link. Backend re-checks
      // on every /api/admin/* call, so the SPA flag is UX, not auth.
      is_admin?: boolean;
    } | null,
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

      // Live rating refresh — backend publishes 'rating_updated' on
      // user.evt.{id} after every rated game finalizes.
      events.on('rating_updated', (payload: any) => {
        if (this.user && typeof payload?.rating === 'number') {
          this.user.rating = payload.rating;
        }
      });
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
