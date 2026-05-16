<template>
  <div id="app-root">
    <Navbar />
    <main class="app-content">
      <router-view></router-view>
    </main>
    <Toast />
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue';
import { useAuthStore } from './stores/auth';
import Navbar from './components/Navbar.vue';
import Toast from './components/Toast.vue';

const authStore = useAuthStore();

onMounted(() => {
  authStore.init();
});
</script>

<style>
/* Global styles */
/* Board cell sizing.
 *
 * The board is 8 × var(--sq) plus a thin coords strip on each axis.
 * The viewport budget has to subtract the sticky 64px navbar, the
 * 32px top+bottom container padding (64px total), and ~30px for the
 * file-coordinates row + breathing room. That gives ~160px of
 * vertical chrome — anything more and the page scrolls on a 768p
 * laptop screen, which was the original complaint.
 *
 * The min() picks the smallest constraint so the board never overflows:
 *   - 112px      → hard cap on huge monitors (matches the original)
 *   - height term → keeps the column inside the viewport (no scroll)
 *   - width term  → leaves room for the 340px SidePanel + gaps + padding
 */
:root {
  --sq: min(
    112px,
    calc((100vh - 160px) / 8.2),
    calc((100vw - 420px) / 8.2)
  );
}
* { box-sizing: border-box; }
body { font-family: -apple-system, system-ui, sans-serif; background: #1e1e1e; color: #ddd; margin: 0; min-height: 100vh; }

/* Below 1000px the SidePanel collapses (GameView.vue media query); the
 * board can use almost the full width. Keep the vertical budget the
 * same so the board still fits without scrolling on short screens. */
@media (max-width: 1000px) {
  :root {
    --sq: min(
      calc((100vh - 140px) / 8.2),
      calc((100vw - 20px) / 8.2)
    );
  }
}

select, button, input[type=text], input[type=password] { background: #333; color: #ddd; border: 1px solid #555; padding: 6px 10px; border-radius: 3px; font: inherit; cursor: pointer; }
button:hover, select:hover { background: #404040; }

.btn-row { display: flex; gap: 6px; margin: 6px 0; }
.btn-row button { flex: 1; }

.btn-analysis { background: #2d4a5a !important; border-color: #3a6075 !important; }
.btn-analysis:hover { background: #355a6f !important; }
.btn-view { background: #3a3a5a !important; border-color: #454570 !important; }
.btn-view:hover { background: #45456f !important; }
.btn-file { background: #3a4a3a !important; border-color: #455045 !important; }
.btn-file:hover { background: #455a45 !important; }
.btn-edit { background: #5a4a2d !important; border-color: #75603a !important; }
.btn-edit:hover { background: #6f5a35 !important; }
.btn-action { background: #2d5a2d !important; border-color: #3a703a !important; }
.btn-action:hover { background: #3a6b3a !important; }
.btn-danger { background: #5a2d2d !important; border-color: #703a3a !important; }
.btn-danger:hover { background: #6b3a3a !important; }
.btn-pause-on { background: #7a5a2d !important; border-color: #9a753a !important; }

.info-line { margin-top: 6px; min-height: 18px; font-size: 13px; font-family: ui-monospace, Menlo, monospace; }

[v-cloak] { display: none; }

#app-root { display: flex; flex-direction: column; min-height: 100vh; }
.app-content { flex: 1; }
</style>
