<template>
  <transition name="confirm-fade">
    <div
      v-if="store.pending"
      class="confirm-backdrop"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="titleId"
      :aria-describedby="msgId"
      @mousedown.self="cancel"
    >
      <div class="confirm-card">
        <h3 :id="titleId" class="confirm-title">{{ store.pending.title }}</h3>
        <p :id="msgId" class="confirm-msg">{{ store.pending.message }}</p>
        <div class="confirm-actions">
          <button class="confirm-btn-cancel" @click="cancel">
            {{ store.pending.cancelLabel }}
          </button>
          <button
            ref="confirmBtn"
            class="confirm-btn-ok"
            :class="{ danger: store.pending.danger }"
            @click="ok"
          >
            {{ store.pending.confirmLabel }}
          </button>
        </div>
      </div>
    </div>
  </transition>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted, computed } from 'vue';
import { useConfirmStore } from '../stores/confirm';

const store = useConfirmStore();
const confirmBtn = ref<HTMLButtonElement | null>(null);

// IDs for aria-labelledby/aria-describedby — stable per mount so
// screen readers correctly associate title + message with the dialog.
const uid = Math.random().toString(36).slice(2, 8);
const titleId = computed(() => `confirm-title-${uid}`);
const msgId = computed(() => `confirm-msg-${uid}`);

const ok = () => store.resolve(true);
const cancel = () => store.resolve(false);

const onKey = (e: KeyboardEvent) => {
  if (!store.pending) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    cancel();
  } else if (e.key === 'Enter') {
    // Enter confirms — matches the native confirm() behavior where
    // Enter on the default-focused OK button submits the dialog.
    e.preventDefault();
    ok();
  }
};

// Auto-focus the confirm button when the dialog opens so keyboard
// users can hit Enter immediately. For destructive actions this still
// requires a click or Enter, never auto-fires.
watch(
  () => store.pending,
  async (val) => {
    if (val) {
      await nextTick();
      confirmBtn.value?.focus();
    }
  }
);

onMounted(() => document.addEventListener('keydown', onKey));
onUnmounted(() => document.removeEventListener('keydown', onKey));
</script>

<style scoped>
.confirm-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.65);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1100;
  padding: 16px;
}

.confirm-card {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 8px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.55);
  width: 100%;
  max-width: 380px;
  padding: 20px 22px 18px;
}

.confirm-title {
  margin: 0 0 8px;
  font-size: 16px;
  font-weight: 600;
  color: #fff;
}

.confirm-msg {
  margin: 0 0 18px;
  font-size: 14px;
  color: #ccc;
  line-height: 1.45;
  white-space: pre-line;
}

.confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.confirm-btn-cancel,
.confirm-btn-ok {
  font-size: 13px;
  font-weight: 600;
  padding: 7px 16px;
  border-radius: 5px;
  cursor: pointer;
  border: 1px solid;
  transition: background-color 120ms ease, border-color 120ms ease;
}

.confirm-btn-cancel {
  background: transparent;
  border-color: #555;
  color: #bbb;
}
.confirm-btn-cancel:hover { background: #333; color: #fff; }

.confirm-btn-ok {
  background: #3a5a7a;
  border-color: #4a6b8a;
  color: #fff;
}
.confirm-btn-ok:hover { background: #4a6b8a; }
.confirm-btn-ok:focus-visible { outline: 2px solid #88aacc; outline-offset: 1px; }

.confirm-btn-ok.danger {
  background: #6b3a3a;
  border-color: #8a4a4a;
}
.confirm-btn-ok.danger:hover { background: #8a4a4a; }
.confirm-btn-ok.danger:focus-visible { outline-color: #d49090; }

.confirm-fade-enter-active,
.confirm-fade-leave-active { transition: opacity 130ms ease; }
.confirm-fade-enter-from,
.confirm-fade-leave-to { opacity: 0; }
.confirm-fade-enter-active .confirm-card,
.confirm-fade-leave-active .confirm-card { transition: transform 130ms ease; }
.confirm-fade-enter-from .confirm-card,
.confirm-fade-leave-to .confirm-card { transform: translateY(-6px); }
</style>
