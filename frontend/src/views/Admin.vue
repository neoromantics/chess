<template>
  <div class="admin-wrap">
    <header class="admin-header">
      <h1>Admin</h1>
      <p class="lede">Operator view. Read-only at this stage — write tools land in a follow-up.</p>
    </header>

    <section v-if="loadError" class="card error-card">
      <strong>Could not load overview.</strong>
      <p>{{ loadError }}</p>
      <button class="btn" @click="loadAll">Retry</button>
    </section>

    <section v-else class="card grid">
      <div class="stat">
        <div class="stat-label">Total users</div>
        <div class="stat-value">{{ overview?.users ?? '—' }}</div>
        <div class="stat-sub">humans only</div>
      </div>
      <div class="stat">
        <div class="stat-label">Seeded bots</div>
        <div class="stat-value">{{ overview?.bots ?? '—' }}</div>
        <div class="stat-sub">matchmaker fallback</div>
      </div>
      <div class="stat">
        <div class="stat-label">Signups (24h)</div>
        <div class="stat-value">{{ overview?.signups_24h ?? '—' }}</div>
      </div>
      <div class="stat">
        <div class="stat-label">Signups (7d)</div>
        <div class="stat-value">{{ overview?.signups_7d ?? '—' }}</div>
      </div>
      <div class="stat">
        <div class="stat-label">Active games</div>
        <div class="stat-value">{{ overview?.active_games ?? '—' }}</div>
      </div>
      <div v-for="(depth, tc) in queueDepth" :key="tc" class="stat">
        <div class="stat-label">Queue ({{ tc }})</div>
        <div class="stat-value">{{ depth }}</div>
      </div>
    </section>

    <section class="card">
      <header class="card-header">
        <h2>Live games</h2>
        <span class="muted">{{ liveGames.length }} ongoing</span>
      </header>
      <table v-if="liveGames.length" class="signup-table">
        <thead>
          <tr>
            <th>White</th>
            <th>Black</th>
            <th>TC</th>
            <th>Started</th>
            <th class="num">Viewers</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="g in liveGames" :key="g.id">
            <td><router-link :to="`/watch/${g.id}`" class="username game-link">{{ g.white_username || '—' }}</router-link></td>
            <td><router-link :to="`/watch/${g.id}`" class="username game-link">{{ g.black_username || '—' }}</router-link></td>
            <td>
              <span class="tc-tag">{{ g.time_control }}</span>
              <span v-if="g.rated" class="rated-tag">rated</span>
            </td>
            <td :title="g.created_at">{{ formatRelative(g.created_at) }}</td>
            <td class="num">{{ g.viewer_count }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No ongoing games right now.</div>
    </section>

    <details class="card collapse">
      <summary class="card-header collapse-summary">
        <h2>Seeded bots</h2>
        <span class="muted">{{ bots.length }} in pool</span>
      </summary>
      <table v-if="bots.length" class="signup-table">
        <thead>
          <tr>
            <th>Username</th>
            <th class="num">Rating</th>
            <th class="num">Games played</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in bots" :key="b.id">
            <td class="username">{{ b.username }}</td>
            <td class="num">{{ b.rating }}</td>
            <td class="num">{{ b.games_played }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No bots seeded.</div>
    </details>

    <section class="card">
      <header class="card-header">
        <h2>Recent signups</h2>
        <span class="muted">{{ signups.length }} most recent</span>
      </header>
      <table v-if="signups.length" class="signup-table">
        <thead>
          <tr>
            <th>User</th>
            <th>Country</th>
            <th>Rating</th>
            <th>Joined</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in signups" :key="u.id">
            <td>
              <span class="username">{{ u.username }}</span>
              <span v-if="u.display_name && u.display_name !== u.username" class="display-name">{{ u.display_name }}</span>
            </td>
            <td>{{ u.country || '—' }}</td>
            <td>{{ u.rating }}</td>
            <td :title="u.created_at">{{ formatRelative(u.created_at) }}</td>
            <td>
              <button
                v-if="authStore.user && u.id !== authStore.user.id"
                class="btn-delete"
                title="Delete this user"
                @click="openDelete(u)"
              >Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No signups yet.</div>
    </section>

    <section class="card">
      <header class="card-header">
        <h2>Recent admin actions</h2>
        <span class="muted">{{ actions.length }} entries</span>
      </header>
      <table v-if="actions.length" class="signup-table">
        <thead>
          <tr>
            <th>When</th>
            <th>Actor</th>
            <th>Action</th>
            <th>Target</th>
            <th>Detail</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in actions" :key="a.id">
            <td :title="a.created_at">{{ formatRelative(a.created_at) }}</td>
            <td class="username">{{ a.actor_username || '(deleted)' }}</td>
            <td><span class="action-pill">{{ a.action }}</span></td>
            <td>{{ a.target_username || '—' }}</td>
            <td class="muted">{{ a.detail || '' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No admin actions recorded yet.</div>
      <div v-if="actions.length >= 50" class="more-row">
        <button
          class="btn ghost"
          :disabled="loadingMoreActions"
          @click="loadMoreActions"
        >{{ loadingMoreActions ? 'Loading…' : 'Load older' }}</button>
      </div>
    </section>

    <!-- Confirm-delete modal. Type the username to enable the
         button — the gateway also re-validates the match so a typo
         can't slip through, but mirroring it here is cheaper than a
         400 round-trip. -->
    <div v-if="deleteTarget" class="modal-backdrop" @click.self="closeDelete">
      <div class="modal">
        <h3>Delete user</h3>
        <p>
          This will hard-delete <strong>{{ deleteTarget.username }}</strong>.
          Game history stays readable with their slot anonymized;
          pending invites are removed.
        </p>
        <p class="warn">This cannot be undone.</p>
        <label>Type <code>{{ deleteTarget.username }}</code> to confirm:</label>
        <input v-model="deleteConfirm" autofocus />
        <div class="modal-actions">
          <button class="btn ghost" @click="closeDelete">Cancel</button>
          <button
            class="btn danger"
            :disabled="deleteConfirm !== deleteTarget.username || deleting"
            @click="confirmDelete"
          >{{ deleting ? 'Deleting…' : 'Delete user' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';
import { useToastStore } from '../stores/toast';

const authStore = useAuthStore();
const toastStore = useToastStore();

type Signup = {
  id: number;
  username: string;
  display_name: string;
  country: string;
  rating: number;
  created_at: string;
};

// Local state for the read-only dashboard. We poll on mount only; the
// operator can refresh the page (or the Retry button on error) when
// they want a fresher snapshot. Auto-poll would burn DB cycles for a
// surface a single admin opens occasionally.
const overview = ref<{
  users: number;
  signups_24h: number;
  signups_7d: number;
  active_games: number;
  queue_depth: Record<string, number>;
} | null>(null);
const signups = ref<Signup[]>([]);
type AdminAction = {
  id: number;
  actor_user_id?: number;
  actor_username: string;
  action: string;
  target_user_id?: number;
  target_username: string;
  detail: string;
  created_at: string;
};
const actions = ref<AdminAction[]>([]);
const loadingMoreActions = ref(false);
const liveGames = ref<Array<{
  id: string;
  white_user_id?: number;
  black_user_id?: number;
  white_username: string;
  black_username: string;
  time_control: string;
  rated: boolean;
  created_at: string;
  updated_at: string;
  viewer_count: number;
}>>([]);
const bots = ref<Array<{
  id: number;
  username: string;
  rating: number;
  games_played: number;
}>>([]);
const loadError = ref<string | null>(null);

// Delete-confirm modal state. Single-target at a time keeps the UI
// dead simple; bulk-delete would need a multi-confirm flow we don't
// need yet.
const deleteTarget = ref<Signup | null>(null);
const deleteConfirm = ref('');
const deleting = ref(false);

const queueDepth = computed(() => overview.value?.queue_depth ?? {});

const loadAll = async () => {
  loadError.value = null;
  try {
    const [ov, sg, ac, lg, bt] = await Promise.all([
      api.adminOverview(),
      api.adminSignups(),
      api.adminActions(),
      api.adminLiveGames(),
      api.adminBots(),
    ]);
    overview.value = ov;
    signups.value = sg || [];
    actions.value = ac || [];
    liveGames.value = lg || [];
    bots.value = bt || [];
  } catch (e: any) {
    loadError.value = e?.message || 'unknown error';
  }
};

const loadMoreActions = async () => {
  if (loadingMoreActions.value || actions.value.length === 0) return;
  loadingMoreActions.value = true;
  try {
    const cursor = actions.value[actions.value.length - 1].created_at;
    const more = await api.adminActions(cursor);
    // Append; the backend returns rows strictly older than cursor so
    // there's no overlap to dedupe.
    actions.value = actions.value.concat(more || []);
  } catch (e: any) {
    toastStore.error('Failed to load older actions: ' + (e?.message || e));
  } finally {
    loadingMoreActions.value = false;
  }
};

const openDelete = (u: Signup) => {
  deleteTarget.value = u;
  deleteConfirm.value = '';
};
const closeDelete = () => {
  deleteTarget.value = null;
  deleteConfirm.value = '';
};
const confirmDelete = async () => {
  if (!deleteTarget.value) return;
  deleting.value = true;
  try {
    await api.adminDeleteUser(deleteTarget.value.id, deleteConfirm.value);
    toastStore.success(`Deleted ${deleteTarget.value.username}`);
    closeDelete();
    // Reload everything (signups dropped a row; actions gained one;
    // overview's user-count is now stale by one).
    await loadAll();
  } catch (e: any) {
    toastStore.error('Delete failed: ' + (e?.message || e));
  } finally {
    deleting.value = false;
  }
};

// Relative time strings without a date library. "5m ago" / "3h ago" /
// "2d ago"; falls back to the absolute timestamp past a week.
const formatRelative = (iso: string): string => {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return iso;
  const secs = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const m = Math.floor(secs / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d ago`;
  return new Date(iso).toLocaleDateString();
};

onMounted(loadAll);
</script>

<style scoped>
.admin-wrap {
  max-width: 880px;
  margin: 0 auto;
  padding: 48px 20px 40px;
  display: flex;
  flex-direction: column;
  gap: 24px;
}
.admin-header h1 { margin: 0 0 6px; font-size: 30px; font-weight: 700; }
.lede { color: #aaa; margin: 0; font-size: 14px; }

.card {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 12px;
  padding: 22px;
}
.error-card { border-color: #6f3a3a; color: #f3c2c2; }

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 16px;
}
.stat {
  background: #232323;
  border: 1px solid #2f2f2f;
  border-radius: 8px;
  padding: 12px 14px;
}
.stat-label { color: #8a8a8a; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; }
.stat-value { color: #fff; font-size: 26px; font-weight: 700; margin-top: 4px; }
.stat-sub { color: #666; font-size: 10px; margin-top: 2px; text-transform: lowercase; letter-spacing: 0.3px; }

.card-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 12px; }
.card-header h2 { margin: 0; font-size: 16px; color: #ddd; font-weight: 600; }
.muted { color: #777; font-size: 12px; }

/* Collapsible card variant — used for low-frequency tables like the
   seeded bot pool that the operator rarely needs to glance at. */
.card.collapse { padding-bottom: 16px; }
.collapse-summary {
  list-style: none;
  cursor: pointer;
  user-select: none;
  margin-bottom: 0;
}
.collapse-summary::-webkit-details-marker { display: none; }
.collapse-summary::after {
  content: '›';
  color: #666;
  margin-left: 10px;
  font-size: 16px;
  transition: transform 120ms ease;
}
.card.collapse[open] > .collapse-summary { margin-bottom: 12px; }
.card.collapse[open] > .collapse-summary::after { transform: rotate(90deg); }

.signup-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.signup-table th { text-align: left; color: #888; font-weight: 500; padding: 8px 6px; border-bottom: 1px solid #333; }
.signup-table td { padding: 10px 6px; border-bottom: 1px solid #2a2a2a; color: #ddd; }
.signup-table tr:last-child td { border-bottom: none; }
.username { font-weight: 600; color: #fff; }
.display-name { color: #888; font-size: 12px; margin-left: 6px; }

.empty { color: #888; font-style: italic; padding: 8px 0; }

.btn {
  background: #1f1f1f;
  border: 1px solid #3a3a3a;
  color: #ddd;
  padding: 8px 14px;
  border-radius: 6px;
  cursor: pointer;
}
.btn:hover { border-color: #4a6b8a; }
.btn.ghost { background: transparent; }
.btn.danger { background: #5a2a2a; border-color: #7a3a3a; color: #f3c2c2; }
.btn.danger:hover:not(:disabled) { background: #6a3a3a; }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }

.btn-delete {
  background: transparent;
  border: 1px solid #5a2a2a;
  color: #c98c8c;
  padding: 4px 10px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 12px;
}
.btn-delete:hover { background: #5a2a2a; color: #fff; }

.action-pill {
  display: inline-block;
  background: #1f2933;
  border: 1px solid #2d3e50;
  color: #9bb3cc;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-family: ui-monospace, Menlo, monospace;
}

.tc-tag {
  background: #1f1f1f;
  border: 1px solid #3a3a3a;
  color: #ccc;
  padding: 2px 7px;
  border-radius: 4px;
  font-size: 11px;
  font-family: ui-monospace, Menlo, monospace;
}
.rated-tag {
  margin-left: 6px;
  color: #c9a86a;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.signup-table th.num, .signup-table td.num { text-align: right; }
.game-link { color: #9bb3cc; text-decoration: none; }
.game-link:hover { color: #c0d3e6; text-decoration: underline; }

.more-row { display: flex; justify-content: center; margin-top: 12px; }

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 10px;
  padding: 22px;
  width: 100%;
  max-width: 440px;
  color: #ddd;
}
.modal h3 { margin: 0 0 10px; font-size: 18px; }
.modal p { margin: 0 0 10px; font-size: 13px; line-height: 1.5; color: #bbb; }
.modal .warn { color: #d4544c; font-weight: 600; }
.modal label { display: block; font-size: 12px; color: #aaa; margin: 10px 0 4px; }
.modal code { font-family: ui-monospace, Menlo, monospace; background: #1f1f1f; padding: 1px 6px; border-radius: 3px; }
.modal input {
  width: 100%;
  background: #1f1f1f;
  border: 1px solid #3a3a3a;
  color: #fff;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 14px;
  box-sizing: border-box;
}
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
</style>
