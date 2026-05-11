<template>
  <div class="toast-container">
    <transition-group name="toast">
      <div v-for="toast in toastStore.toasts" :key="toast.id" :class="['toast', toast.type]">
        <div class="toast-message">{{ toast.message }}</div>
        <button class="toast-close" @click="toastStore.remove(toast.id)">×</button>
      </div>
    </transition-group>
  </div>
</template>

<script setup lang="ts">
import { useToastStore } from '../stores/toast';
const toastStore = useToastStore();
</script>

<style scoped>
.toast-container { position: fixed; top: 20px; right: 20px; z-index: 9999; display: flex; flex-direction: column; gap: 10px; pointer-events: none; }
.toast { padding: 12px 20px; border-radius: 6px; color: white; display: flex; align-items: center; justify-content: space-between; min-width: 250px; max-width: 400px; box-shadow: 0 4px 12px rgba(0,0,0,0.3); pointer-events: auto; }

.info { background: #4a6b8a; }
.success { background: #2d5a2d; }
.error { background: #ff7575; }
.warning { background: #e6a075; }

.toast-message { flex: 1; font-size: 14px; font-weight: 500; }
.toast-close { background: transparent; border: none; color: rgba(255,255,255,0.7); font-size: 20px; cursor: pointer; padding: 0 0 0 10px; line-height: 1; }
.toast-close:hover { color: white; }

/* Transitions */
.toast-enter-active, .toast-leave-active { transition: all 0.3s ease; }
.toast-enter-from { opacity: 0; transform: translateX(30px); }
.toast-leave-to { opacity: 0; transform: translateX(30px); }
</style>
