import { createRouter, createWebHistory, RouteRecordRaw } from 'vue-router';
import Dashboard from './views/Dashboard.vue';
import Login from './views/Login.vue';
import Signup from './views/Signup.vue';
import GameView from './views/GameView.vue';
import { useAuthStore } from './stores/auth';

const routes: RouteRecordRaw[] = [
  { path: '/', component: Dashboard, meta: { requiresAuth: true } },
  { path: '/login', component: Login, meta: { guestOnly: true } },
  { path: '/signup', component: Signup, meta: { guestOnly: true } },
  { path: '/game/:id', component: GameView, props: true, meta: { requiresAuth: true } },
  { path: '/:pathMatch(.*)*', component: { template: '<div style="padding: 50px; text-align: center;"><h2>404 Page Not Found</h2><router-link to="/">Go Home</router-link></div>' } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// Guard: gate protected routes behind auth; bounce signed-in users
// away from /login and /signup.
router.beforeEach(async (to) => {
  const auth = useAuthStore();
  await auth.init();
  if (to.meta.requiresAuth && !auth.user) {
    return { path: '/login', query: { next: to.fullPath } };
  }
  if (to.meta.guestOnly && auth.user) {
    return { path: '/' };
  }
});

export default router;
