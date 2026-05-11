import { reactive } from 'vue';
import { api } from './api';

export const authStore = reactive({
  user: null as { id: number; username: string } | null,
  initialized: false,

  async init() {
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

  logout() {
    api.logout();
    this.user = null;
  }
});
