import { defineStore } from 'pinia';

export type ToastType = 'info' | 'success' | 'error' | 'warning';

export interface Toast {
  id: number;
  message: string;
  type: ToastType;
  duration: number;
}

export const useToastStore = defineStore('toast', {
  state: () => ({
    toasts: [] as Toast[],
    nextId: 1,
  }),
  actions: {
    add(message: string, type: ToastType = 'info', duration: number = 3000) {
      const id = this.nextId++;
      this.toasts.push({ id, message, type, duration });
      setTimeout(() => {
        this.remove(id);
      }, duration);
    },
    remove(id: number) {
      const index = this.toasts.findIndex(t => t.id === id);
      if (index !== -1) {
        this.toasts.splice(index, 1);
      }
    },
    error(message: string) {
      this.add(message, 'error', 5000);
    },
    success(message: string) {
      this.add(message, 'success', 3000);
    },
    info(message: string) {
      this.add(message, 'info', 3000);
    },
    warning(message: string) {
      this.add(message, 'warning', 4000);
    }
  }
});
