import { defineStore } from 'pinia';

// In-theme replacement for window.prompt(). Same shape as the confirm
// store but takes text input instead of returning yes/no. Resolves to
// the trimmed input string when the user clicks confirm / hits Enter,
// or null when they cancel / backdrop-click / press Esc.

export interface PromptOptions {
  title?: string;
  message?: string;
  // Optional starting value for the input (e.g. existing name on a
  // rename action). Empty default = blank field.
  defaultValue?: string;
  // Optional grey placeholder shown when the field is empty.
  placeholder?: string;
  confirmLabel?: string;
  cancelLabel?: string;
}

interface PendingRequest extends Required<Omit<PromptOptions, 'title' | 'message' | 'placeholder'>> {
  title: string;
  message: string;
  placeholder: string;
  resolve: (value: string | null) => void;
}

export const usePromptStore = defineStore('prompt', {
  state: () => ({
    pending: null as PendingRequest | null,
  }),
  actions: {
    ask(opts: PromptOptions): Promise<string | null> {
      // Same self-cancel rule as the confirm store: if a prior prompt
      // is still open, resolve it as cancelled and let the new one
      // take over. Two stacked prompts would be a UI bug.
      if (this.pending) this.pending.resolve(null);
      return new Promise<string | null>((resolve) => {
        this.pending = {
          title: opts.title ?? 'Enter a value',
          message: opts.message ?? '',
          defaultValue: opts.defaultValue ?? '',
          placeholder: opts.placeholder ?? '',
          confirmLabel: opts.confirmLabel ?? 'OK',
          cancelLabel: opts.cancelLabel ?? 'Cancel',
          resolve,
        };
      });
    },
    resolve(value: string | null) {
      if (!this.pending) return;
      const p = this.pending;
      this.pending = null;
      p.resolve(value);
    },
  },
});
