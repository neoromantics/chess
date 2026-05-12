<template>
  <div class="invites-page">
    <header class="page-header">
      <h2>Invites</h2>
      <p class="hint">Challenge a friend by username. Invites expire after 60 seconds.</p>
    </header>

    <section class="card">
      <h3>Send a challenge</h3>
      <form class="send-form" @submit.prevent="onSend">
        <div class="form-row">
          <label class="field">
            <span>Username</span>
            <input
              v-model="form.toUsername"
              type="text"
              placeholder="e.g. magnus"
              autocomplete="off"
              @input="onTypeUsername"
              @focus="onTypeUsername"
              @blur="onBlurAutocomplete"
            >
            <ul v-if="autocomplete.length > 0" class="autocomplete">
              <li v-for="u in autocomplete" :key="u.id" @mousedown.prevent="pickUser(u)">
                <strong>{{ u.username }}</strong>
                <span class="muted">{{ u.display_name || '' }} · {{ Math.round(u.rating) }}</span>
              </li>
            </ul>
          </label>
          <label class="field">
            <span>Time control</span>
            <select v-model="form.timeControl">
              <option value="1+0">Bullet · 1+0</option>
              <option value="2+1">Bullet · 2+1</option>
              <option value="3+2">Blitz · 3+2</option>
              <option value="5+0">Blitz · 5+0</option>
              <option value="10+5">Rapid · 10+5</option>
              <option value="15+10">Rapid · 15+10</option>
              <option value="corr-1d">Correspondence · 1 day/move</option>
            </select>
          </label>
          <label class="field rated">
            <input type="checkbox" v-model="form.rated">
            <span>Rated</span>
          </label>
        </div>
        <button type="submit" class="btn-primary" :disabled="!canSend || sending">
          {{ sending ? 'Sending...' : 'Send invite' }}
        </button>
      </form>
    </section>

    <section class="card">
      <h3>Received ({{ inviteStore.received.length }})</h3>
      <p v-if="inviteStore.received.length === 0" class="muted">No incoming invites right now.</p>
      <ul class="invite-list">
        <li v-for="inv in inviteStore.received" :key="inv.id" class="invite-row">
          <div class="invite-meta">
            <strong>{{ inv.from_username || `user #${inv.from_user_id}` }}</strong>
            <span class="muted">{{ inv.time_control }} · {{ inv.rated ? 'rated' : 'casual' }}</span>
            <span class="ttl" :title="inv.expires_at">expires in {{ secondsUntil(inv.expires_at) }}s</span>
          </div>
          <div class="invite-actions">
            <button @click="accept(inv)" class="btn-accept" :disabled="busyId === inv.id">Accept</button>
            <button @click="decline(inv)" class="btn-decline" :disabled="busyId === inv.id">Decline</button>
          </div>
        </li>
      </ul>
    </section>

    <section class="card">
      <h3>Sent ({{ inviteStore.sent.length }})</h3>
      <p v-if="inviteStore.sent.length === 0" class="muted">No outgoing invites right now.</p>
      <ul class="invite-list">
        <li v-for="inv in inviteStore.sent" :key="inv.id" class="invite-row">
          <div class="invite-meta">
            <strong>{{ inv.to_username || `user #${inv.to_user_id}` }}</strong>
            <span class="muted">{{ inv.time_control }} · {{ inv.rated ? 'rated' : 'casual' }}</span>
            <span class="ttl">expires in {{ secondsUntil(inv.expires_at) }}s</span>
          </div>
          <div class="invite-actions">
            <button @click="cancel(inv)" class="btn-decline" :disabled="busyId === inv.id">Cancel</button>
          </div>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import type { InviteWire, UserSummary } from '../types';
import { useInviteStore } from '../stores/invites';
import { useUserEventsStore } from '../stores/userEvents';
import { useToastStore } from '../stores/toast';

const router = useRouter();
const inviteStore = useInviteStore();
const userEvents = useUserEventsStore();
const toast = useToastStore();

const form = ref({
  toUsername: '',
  timeControl: '5+0',
  rated: false,
});
const sending = ref(false);
const busyId = ref<string | null>(null);
const autocomplete = ref<UserSummary[]>([]);
const now = ref(Date.now());
let nowTimer: number | undefined;
let searchTimer: number | undefined;
let acceptListenerUnsub: (() => void) | null = null;

const canSend = computed(() => form.value.toUsername.trim().length >= 3);

function secondsUntil(iso: string) {
  const t = new Date(iso).getTime();
  return Math.max(0, Math.floor((t - now.value) / 1000));
}

function onTypeUsername() {
  const q = form.value.toUsername.trim();
  if (searchTimer) window.clearTimeout(searchTimer);
  if (q.length < 2) {
    autocomplete.value = [];
    return;
  }
  // Debounce the network roundtrip so we don't flood SearchUsers on
  // every keystroke. 200ms is the sweet spot between feel and chatter.
  searchTimer = window.setTimeout(async () => {
    try {
      autocomplete.value = await api.searchUsers(q);
    } catch {
      autocomplete.value = [];
    }
  }, 200);
}

function onBlurAutocomplete() {
  // Delay hiding so the mousedown handler on the suggestion has time
  // to fire before the input loses focus. (mousedown fires before
  // blur, but the v-if would unmount the <li> before mouseup.)
  window.setTimeout(() => { autocomplete.value = []; }, 120);
}

function pickUser(u: UserSummary) {
  form.value.toUsername = u.username;
  autocomplete.value = [];
}

async function onSend() {
  sending.value = true;
  try {
    await inviteStore.send(form.value.toUsername.trim(), form.value.timeControl, form.value.rated);
    toast.success(`Invite sent to ${form.value.toUsername}`);
    form.value.toUsername = '';
  } catch (e: any) {
    toast.error(e?.message || 'Failed to send invite');
  } finally {
    sending.value = false;
  }
}

async function accept(inv: InviteWire) {
  busyId.value = inv.id;
  try {
    const updated = await inviteStore.accept(inv.id);
    if (updated.game_id) router.push(`/game/${updated.game_id}`);
  } catch (e: any) {
    toast.error(e?.message || 'Failed to accept invite');
  } finally {
    busyId.value = null;
  }
}

async function decline(inv: InviteWire) {
  busyId.value = inv.id;
  try {
    await inviteStore.decline(inv.id);
  } catch (e: any) {
    toast.error(e?.message || 'Failed to decline');
  } finally {
    busyId.value = null;
  }
}

async function cancel(inv: InviteWire) {
  busyId.value = inv.id;
  try {
    await inviteStore.cancel(inv.id);
  } catch (e: any) {
    toast.error(e?.message || 'Failed to cancel');
  } finally {
    busyId.value = null;
  }
}

onMounted(async () => {
  await inviteStore.init();
  nowTimer = window.setInterval(() => { now.value = Date.now(); }, 1000);

  // The sender side of an accepted invite needs to be redirected too —
  // the recipient was the one who hit Accept, so the redirect flow above
  // only covers them. This listener handles the from-side jump.
  acceptListenerUnsub = userEvents.on('invite_accepted', (inv: InviteWire) => {
    if (inv.game_id && !router.currentRoute.value.path.startsWith('/game/')) {
      router.push(`/game/${inv.game_id}`);
    }
  });
});

onUnmounted(() => {
  if (nowTimer) window.clearInterval(nowTimer);
  if (searchTimer) window.clearTimeout(searchTimer);
  if (acceptListenerUnsub) acceptListenerUnsub();
});
</script>

<style scoped>
.invites-page { max-width: 1000px; margin: 0 auto; padding: 32px 20px; display: flex; flex-direction: column; gap: 20px; }
.page-header h2 { margin: 0 0 6px; }
.page-header .hint { margin: 0; color: #888; font-size: 14px; }

.card { background: #2b2b2b; border-radius: 8px; padding: 20px; }
.card h3 { margin: 0 0 16px; font-size: 16px; color: #ddd; }

.send-form .form-row { display: grid; grid-template-columns: 2fr 1.5fr auto; gap: 12px; margin-bottom: 16px; }
.send-form .field { display: flex; flex-direction: column; gap: 4px; font-size: 13px; color: #aaa; position: relative; }
.send-form .field input[type=text], .send-form .field select { background: #1e1e1e; border: 1px solid #444; padding: 10px; border-radius: 4px; color: #ddd; }
.send-form .field input[type=text]:focus, .send-form .field select:focus { border-color: #4a6b8a; outline: none; }
.send-form .field.rated { flex-direction: row; align-items: end; gap: 6px; padding-bottom: 10px; }

.autocomplete { position: absolute; top: 100%; left: 0; right: 0; margin: 2px 0 0; padding: 0; list-style: none; background: #1a1a1a; border: 1px solid #444; border-radius: 4px; z-index: 10; max-height: 220px; overflow-y: auto; box-shadow: 0 6px 20px rgba(0,0,0,0.4); }
.autocomplete li { padding: 8px 12px; cursor: pointer; display: flex; gap: 10px; align-items: baseline; font-size: 13px; }
.autocomplete li:hover { background: #2b2b2b; }
.autocomplete .muted { color: #888; font-size: 12px; }

.btn-primary { background: #4a6b8a; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: 600; }
.btn-primary:hover { background: #5a7b9a; }
.btn-primary:disabled { opacity: 0.4; cursor: not-allowed; }

.invite-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 8px; }
.invite-row { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; background: #1e1e1e; border-radius: 6px; border-left: 3px solid #4a6b8a; }
.invite-meta { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap; }
.invite-meta .muted { color: #888; font-size: 13px; }
.invite-meta .ttl { color: #d48d44; font-size: 12px; font-family: ui-monospace, monospace; }

.invite-actions { display: flex; gap: 8px; }
.btn-accept { background: #2d5a2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: 600; }
.btn-accept:hover { background: #3a6b3a; }
.btn-decline { background: #5a2d2d; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; }
.btn-decline:hover { background: #6b3a3a; }
.btn-accept:disabled, .btn-decline:disabled { opacity: 0.4; cursor: not-allowed; }

.muted { color: #888; font-size: 14px; }
</style>
