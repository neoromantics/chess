<template>
  <transition name="prompt-fade">
    <div
      v-if="store.pending"
      class="prompt-backdrop"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="titleId"
      :aria-describedby="msgId"
      @mousedown.self="cancel"
    >
      <div class="prompt-card">
        <h3 :id="titleId" class="prompt-title">{{ store.pending.title }}</h3>
        <p v-if="store.pending.message" :id="msgId" class="prompt-msg">
          {{ store.pending.message }}
        </p>
        <input
          ref="inputEl"
          v-model="value"
          type="text"
          class="prompt-input"
          :placeholder="store.pending.placeholder"
          @keydown.enter.prevent="ok"
        />
        <div class="prompt-actions">
          <button class="prompt-btn-cancel" @click="cancel">
            {{ store.pending.cancelLabel }}
          </button>
          <button class="prompt-btn-ok" :disabled="!value.trim()" @click="ok">
            {{ store.pending.confirmLabel }}
          </button>
        </div>
      </div>
    </div>
  </transition>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted, computed } from 'vue';
import { usePromptStore } from '../stores/prompt';

const store = usePromptStore();
const inputEl = ref<HTMLInputElement | null>(null);
const value = ref('');

const uid = Math.random().toString(36).slice(2, 8);
const titleId = computed(() => `prompt-title-${uid}`);
const msgId = computed(() => `prompt-msg-${uid}`);

const ok = () => {
  const v = value.value.trim();
  // Empty input behaves like cancel — matches window.prompt() returning
  // the empty string for "user typed nothing then hit OK," which most
  // callers (including ours) treat the same as cancel.
  store.resolve(v === '' ? null : v);
};
const cancel = () => store.resolve(null);

const onKey = (e: KeyboardEvent) => {
  if (!store.pending) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    cancel();
  }
  // Enter is handled by the input's @keydown.enter so we don't
  // double-fire when the field has focus.
};

// Reset the input and focus on every fresh open. selectAll lets the
// user immediately overwrite the default (matches Chrome's prompt UX
// where the default text is pre-selected).
watch(
  () => store.pending,
  async (val) => {
    if (val) {
      value.value = val.defaultValue;
      await nextTick();
      inputEl.value?.focus();
      inputEl.value?.select();
    }
  }
);

onMounted(() => document.addEventListener('keydown', onKey));
onUnmounted(() => document.removeEventListener('keydown', onKey));
</script>

<style scoped>
.prompt-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.65);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1100;
  padding: 16px;
}

.prompt-card {
  background: #2b2b2b;
  border: 1px solid #3d3d3d;
  border-radius: 8px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.55);
  width: 100%;
  max-width: 420px;
  padding: 20px 22px 18px;
}

.prompt-title {
  margin: 0 0 8px;
  font-size: 16px;
  font-weight: 600;
  color: #fff;
}

.prompt-msg {
  margin: 0 0 12px;
  font-size: 13px;
  color: #aaa;
  line-height: 1.45;
}

.prompt-input {
  width: 100%;
  box-sizing: border-box;
  background: #1c1c1c;
  border: 1px solid #444;
  border-radius: 5px;
  color: #eee;
  font-size: 14px;
  padding: 8px 10px;
  margin-bottom: 16px;
  font: inherit;
  outline: none;
  transition: border-color 120ms ease;
}
.prompt-input:focus { border-color: #4a6b8a; }

.prompt-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.prompt-btn-cancel,
.prompt-btn-ok {
  font-size: 13px;
  font-weight: 600;
  padding: 7px 16px;
  border-radius: 5px;
  cursor: pointer;
  border: 1px solid;
  transition: background-color 120ms ease, border-color 120ms ease;
}

.prompt-btn-cancel {
  background: transparent;
  border-color: #555;
  color: #bbb;
}
.prompt-btn-cancel:hover { background: #333; color: #fff; }

.prompt-btn-ok {
  background: #3a5a7a;
  border-color: #4a6b8a;
  color: #fff;
}
.prompt-btn-ok:hover { background: #4a6b8a; }
.prompt-btn-ok:disabled {
  background: #2f2f2f;
  border-color: #3a3a3a;
  color: #777;
  cursor: not-allowed;
}

.prompt-fade-enter-active,
.prompt-fade-leave-active { transition: opacity 130ms ease; }
.prompt-fade-enter-from,
.prompt-fade-leave-to { opacity: 0; }
.prompt-fade-enter-active .prompt-card,
.prompt-fade-leave-active .prompt-card { transition: transform 130ms ease; }
.prompt-fade-enter-from .prompt-card,
.prompt-fade-leave-to .prompt-card { transform: translateY(-6px); }
</style>
