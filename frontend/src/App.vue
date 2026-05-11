<template>
  <router-view></router-view>
</template>

<script setup lang="ts">
import { onMounted } from 'vue';
import { authStore } from './auth';

onMounted(() => {
  authStore.init();
});
</script>

<style>
/* Global styles can stay here or be moved to a separate CSS file */
:root { --sq: 112px; }
* { box-sizing: border-box; }
body { font-family: -apple-system, system-ui, sans-serif; background: #1e1e1e; color: #ddd; margin: 0; min-height: 100vh; }

@media (max-width: 1400px) {
  :root { --sq: min(11vh, calc((100vw - 420px) / 8.2)); }
}
@media (max-width: 1000px) {
  :root { --sq: min(11vh, 11vw); }
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
.btn-pause-on { background: #7a5a2d !important; border-color: #9a753a !important; }

.info-line { margin-top: 6px; min-height: 18px; font-size: 13px; font-family: ui-monospace, Menlo, monospace; }

[v-cloak] { display: none; }
</style>
