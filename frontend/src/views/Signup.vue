<template>
  <div class="auth-page">
    <div class="auth-card">
      <h2>Sign Up</h2>
      <form @submit.prevent="handleSubmit">
        <div class="form-group">
          <label>Username</label>
          <input v-model="username" type="text" required placeholder="Choose a username">
          <div class="hint-list" v-if="username">
            <div v-if="usernameStatus === 'checking'" class="checking">• Checking availability…</div>
            <div v-else-if="usernameStatus === 'available'" class="ok">• Available</div>
            <div v-else-if="usernameStatus === 'taken'" class="err">• Already taken — pick another</div>
            <div v-else-if="usernameStatus === 'too_short'" class="err">• At least 5 characters</div>
            <div v-else-if="usernameStatus === 'too_long'" class="err">• At most 32 characters</div>
          </div>
        </div>
        <div class="form-group">
          <label>Password</label>
          <input v-model="password" type="password" required placeholder="At least 8 characters" minlength="8">
          <!-- Mirror of the backend ValidatePassword rules in pkg/auth/password.go.
               Authoritative check happens server-side; this is just to surface
               the constraints before the user hits Submit. -->
          <div class="hint-list">
            <div :class="{ ok: password.length >= 8 }">• At least 8 characters</div>
            <div :class="{ ok: password.length > 0 && !looksCommon }">• Not a commonly-used password</div>
            <div :class="{ ok: password.length > 0 && !containsUsername }">• Doesn't contain your username</div>
          </div>
        </div>
        <div class="form-group">
          <label>Confirm Password</label>
          <input v-model="confirmPassword" type="password" required placeholder="Confirm your password">
        </div>
        <div class="error-msg" v-if="error">{{ error }}</div>
        <button type="submit" class="btn-primary" :disabled="loading">
          {{ loading ? 'Creating account...' : 'Sign Up' }}
        </button>
      </form>
      <div class="auth-footer">
        Already have an account? <router-link to="/login">Login</router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api } from '../api';
import { useAuthStore } from '../stores/auth';

const router = useRouter();
const route = useRoute();
const authStore = useAuthStore();
const username = ref('');
const password = ref('');
const confirmPassword = ref('');
const error = ref('');
const loading = ref(false);

// Frontend mirror of pkg/auth/password.go's blacklist. Kept short on
// purpose — the server has the authoritative list; this is just for
// instant visual feedback while typing. Don't fork the full list to
// avoid drift; the most common offenders cover 95% of the help-text
// value.
const commonPasswordsHint = new Set([
  'password', 'password1', 'password123', '12345678', '123456789',
  'qwerty', 'qwerty123', 'abc123', 'letmein', 'welcome', 'iloveyou',
  'admin', 'changeme', 'chess', 'chess123', 'chessmaster',
]);
const looksCommon = computed(() => commonPasswordsHint.has(password.value.toLowerCase()));
const containsUsername = computed(() => {
  const u = username.value.trim().toLowerCase();
  if (u.length < 3) return false;
  return password.value.toLowerCase().includes(u);
});

// Live username availability. Backed by GET /api/auth/check-username.
// Debounced so we don't fire a request per keystroke, and gated by a
// monotonic request ID so an in-flight check for "ali" can't overwrite
// the answer for "alice" if it races back later.
type UsernameStatus = '' | 'checking' | 'available' | 'taken' | 'too_short' | 'too_long';
const usernameStatus = ref<UsernameStatus>('');
let usernameDebounce: ReturnType<typeof setTimeout> | null = null;
let usernameCheckSeq = 0;

watch(username, (raw) => {
  const u = raw.trim();
  if (usernameDebounce) clearTimeout(usernameDebounce);
  if (u.length === 0) { usernameStatus.value = ''; return; }
  if (u.length < 5)   { usernameStatus.value = 'too_short'; return; }
  if (u.length > 32)  { usernameStatus.value = 'too_long'; return; }

  usernameStatus.value = 'checking';
  const seq = ++usernameCheckSeq;
  usernameDebounce = setTimeout(async () => {
    try {
      const res = await api.checkUsername(u);
      if (seq !== usernameCheckSeq) return;       // a newer keystroke landed
      if (raw !== username.value)   return;       // input changed during await
      if (res.available) usernameStatus.value = 'available';
      else usernameStatus.value = (res.reason as UsernameStatus) || 'taken';
    } catch {
      // Network hiccup — clear the indicator rather than blocking; the
      // submit-side check is the real gate.
      if (seq === usernameCheckSeq) usernameStatus.value = '';
    }
  }, 350);
});

const handleSubmit = async () => {
  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match';
    return;
  }
  if (usernameStatus.value === 'taken') {
    error.value = 'Username already taken';
    return;
  }

  loading.value = true;
  error.value = '';
  try {
    const res = await api.signup(username.value, password.value);
    authStore.setUser(res.user);
    // Carry-over: if the user signed up while playing an anonymous
    // temp game, the gateway upgrades it into a durable game and
    // returns its new id. Land the user back in their game so the
    // signup feels seamless.
    if (res.upgraded_game_id) {
      router.push(`/game/${res.upgraded_game_id}`);
      return;
    }
    const next = (route.query.next as string) || '/';
    router.push(next);
  } catch (e: any) {
    error.value = e.message || 'Signup failed';
  } finally {
    loading.value = false;
  }
};
</script>

<style scoped>
.auth-page { display: flex; align-items: center; justify-content: center; min-height: 100vh; padding: 20px; }
.auth-card { background: #2b2b2b; padding: 40px; border-radius: 12px; width: 100%; max-width: 400px; box-shadow: 0 10px 30px rgba(0,0,0,0.5); }
h2 { margin: 0 0 24px; text-align: center; color: #ddd; }

.form-group { margin-bottom: 20px; }
.form-group label { display: block; font-size: 12px; text-transform: uppercase; color: #666; font-weight: 700; margin-bottom: 8px; }
.form-group input { width: 100%; background: #1e1e1e; border: 1px solid #444; border-radius: 4px; padding: 12px; color: #ddd; font-size: 14px; }
.form-group input:focus { border-color: #4a6b8a; outline: none; }

.error-msg { color: #ff7575; font-size: 14px; margin-bottom: 20px; text-align: center; }

.btn-primary { width: 100%; background: #4a6b8a; color: white; border: none; padding: 14px; border-radius: 4px; cursor: pointer; font-weight: 600; font-size: 16px; }
.btn-primary:hover { background: #5a7b9a; }
.btn-primary:disabled { opacity: 0.5; }

.hint-list { margin-top: 8px; font-size: 12px; color: #777; line-height: 1.6; }
.hint-list .ok { color: #5cb85c; }
.hint-list .err { color: #d35454; }
.hint-list .checking { color: #aaa; font-style: italic; }

.auth-footer { margin-top: 24px; text-align: center; font-size: 14px; color: #888; }
.auth-footer a { color: #4a6b8a; text-decoration: none; }
.auth-footer a:hover { text-decoration: underline; }
</style>
