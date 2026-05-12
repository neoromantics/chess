import { defineStore } from 'pinia';
import { api } from '../api';
import type { InviteWire } from '../types';
import { useUserEventsStore } from './userEvents';
import { useToastStore } from './toast';

// inviteStore is the single source of truth for an authenticated user's
// pending invites (both incoming and outgoing). The reconnect-handshake
// contract: when we mount or reconnect, refetch /api/invites/pending so
// any events missed while offline are picked up. Live WS events keep
// the lists in sync between fetches.

export const useInviteStore = defineStore('invites', {
  state: () => ({
    received: [] as InviteWire[],
    sent: [] as InviteWire[],
    initialized: false,
  }),
  getters: {
    pendingCount: (s) => s.received.filter(i => i.status === 'pending').length,
  },
  actions: {
    async init() {
      // Wire WS event handlers ONCE — subsequent calls just refetch.
      if (!this.initialized) {
        const events = useUserEventsStore();
        const toast = useToastStore();

        const upsertReceived = (inv: InviteWire) => {
          const idx = this.received.findIndex(x => x.id === inv.id);
          if (idx >= 0) this.received.splice(idx, 1, inv);
          else this.received.unshift(inv);
        };
        const upsertSent = (inv: InviteWire) => {
          const idx = this.sent.findIndex(x => x.id === inv.id);
          if (idx >= 0) this.sent.splice(idx, 1, inv);
          else this.sent.unshift(inv);
        };
        const drop = (id: string) => {
          this.received = this.received.filter(x => x.id !== id);
          this.sent = this.sent.filter(x => x.id !== id);
        };

        events.on('invite_created', (inv: InviteWire) => {
          upsertReceived(inv);
          toast.info(`${inv.from_username || 'Someone'} invited you to a ${inv.time_control} game`);
        });
        events.on('invite_sent', (inv: InviteWire) => {
          upsertSent(inv);
        });
        events.on('invite_accepted', (inv: InviteWire) => {
          drop(inv.id);
          // Both sides hear this event; the receiver-side handler in
          // Invites.vue or App.vue is what redirects to the game page.
        });
        events.on('invite_declined', (inv: InviteWire) => {
          drop(inv.id);
          toast.info(`${inv.to_username || 'Recipient'} declined your invite`);
        });
        events.on('invite_cancelled', (inv: InviteWire) => {
          drop(inv.id);
        });
        events.on('invite_expired', (inv: InviteWire) => {
          drop(inv.id);
        });

        this.initialized = true;
      }
      await this.refresh();
    },
    async refresh() {
      try {
        const { received, sent } = await api.listPendingInvites();
        this.received = received || [];
        this.sent = sent || [];
      } catch {
        // /ws/user reconnect will get us back in sync; non-fatal.
      }
    },
    async send(toUsername: string, timeControl: string, rated: boolean) {
      const inv = await api.sendInvite({ to_username: toUsername, time_control: timeControl, rated });
      // We'll also receive the invite_sent WS event; the upsert is
      // idempotent so this prefetch isn't a problem.
      const idx = this.sent.findIndex(x => x.id === inv.id);
      if (idx >= 0) this.sent.splice(idx, 1, inv);
      else this.sent.unshift(inv);
      return inv;
    },
    async accept(id: string) {
      return await api.acceptInvite(id);
    },
    async decline(id: string) {
      await api.declineInvite(id);
      this.received = this.received.filter(x => x.id !== id);
    },
    async cancel(id: string) {
      await api.cancelInvite(id);
      this.sent = this.sent.filter(x => x.id !== id);
    },
    reset() {
      this.received = [];
      this.sent = [];
      this.initialized = false;
    },
  },
});
