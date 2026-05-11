<template>
  <div class="auth-page">
    <div class="auth-card">
      <h2>Sign Up</h2>
      <form @submit.prevent="handleSubmit">
        <div class="form-group">
          <label>Username</label>
          <input v-model="username" type="text" required placeholder="Choose a username">
        </div>
        <div class="form-group">
          <label>Password</label>
          <input v-model="password" type="password" required placeholder="Choose a password">
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
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api';
import { authStore } from '../auth';

const router = useRouter();
const username = ref('');
const password = ref('');
const confirmPassword = ref('');
const error = ref('');
const loading = ref(false);

const handleSubmit = async () => {
  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match';
    return;
  }

  loading.value = true;
  error.value = '';
  try {
    const res = await api.signup(username.value, password.value);
    authStore.setUser(res.user);
    router.push('/');
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

.auth-footer { margin-top: 24px; text-align: center; font-size: 14px; color: #888; }
.auth-footer a { color: #4a6b8a; text-decoration: none; }
.auth-footer a:hover { text-decoration: underline; }
</style>
