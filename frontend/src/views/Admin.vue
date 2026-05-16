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
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No signups yet.</div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { api } from '../api';

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
const signups = ref<Array<{
  id: number;
  username: string;
  display_name: string;
  country: string;
  rating: number;
  created_at: string;
}>>([]);
const loadError = ref<string | null>(null);

const queueDepth = computed(() => overview.value?.queue_depth ?? {});

const loadAll = async () => {
  loadError.value = null;
  try {
    const [ov, sg] = await Promise.all([api.adminOverview(), api.adminSignups()]);
    overview.value = ov;
    signups.value = sg || [];
  } catch (e: any) {
    loadError.value = e?.message || 'unknown error';
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

.card-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 12px; }
.card-header h2 { margin: 0; font-size: 16px; color: #ddd; font-weight: 600; }
.muted { color: #777; font-size: 12px; }

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
  margin-top: 8px;
}
.btn:hover { border-color: #4a6b8a; }
</style>
