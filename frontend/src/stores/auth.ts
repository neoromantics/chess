import { defineStore } from 'pinia';
import { api } from '../api';

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
    },
    setUser(user: any) {
      this.user = user;
    },
    async logout() {
      try {
        await api.logout();
      } finally {
        this.user = null;
      }
    }
  }
});
