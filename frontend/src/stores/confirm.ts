import { defineStore } from 'pinia';

// Drop-in replacement for window.confirm. Browser-native confirm()
// renders the user-agent dialog (Chrome/Safari/Firefox each look
// different and none of them match the dark theme); this store backs
// a single mounted <ConfirmModal /> that resolves a Promise when the
// user clicks confirm / cancel / backdrop / presses Esc.

export interface ConfirmOptions {
  title?: string;
  message: string;
  confirmLabel?: string;
  cancelLabel?: string;
  // Renders the confirm button in red. Use for destructive actions
  // (delete, resign, leave) — anything the user can't easily undo.
  danger?: boolean;
}

interface PendingRequest extends Required<Omit<ConfirmOptions, 'title'>> {
  title: string;
  resolve: (value: boolean) => void;
}

export const useConfirmStore = defineStore('confirm', {
  state: () => ({
    pending: null as PendingRequest | null,
  }),
  actions: {
    ask(opts: ConfirmOptions): Promise<boolean> {
      // If a previous prompt is still open, auto-cancel it so the new
      // one can take over. Two stacked confirms would be a UI bug, but
      // resolving rather than rejecting keeps callers' .then chains sane.
      if (this.pending) this.pending.resolve(false);
      return new Promise<boolean>((resolve) => {
        this.pending = {
          title: opts.title ?? 'Confirm',
          message: opts.message,
          confirmLabel: opts.confirmLabel ?? 'Confirm',
          cancelLabel: opts.cancelLabel ?? 'Cancel',
          danger: opts.danger ?? false,
          resolve,
        };
      });
    },
    resolve(value: boolean) {
      if (!this.pending) return;
      const p = this.pending;
      this.pending = null;
      p.resolve(value);
    },
  },
});
