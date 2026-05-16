import { createRouter, createWebHistory, RouteRecordRaw } from 'vue-router';
import Profile from './views/Profile.vue';
import Login from './views/Login.vue';
import Signup from './views/Signup.vue';
import GameView from './views/GameView.vue';
import Invites from './views/Invites.vue';
import Landing from './views/Landing.vue';
import Match from './views/Match.vue';
import { useAuthStore } from './stores/auth';

const routes: RouteRecordRaw[] = [
  // / is the mode chooser (Play vs Engine / Match a Player). /match
  // is the dedicated PvP matchmaking page that the chooser links to.
  // The old /dashboard route is gone; any bookmarks redirect here.
  { path: '/', component: Landing },
  { path: '/match', component: Match, meta: { requiresAuth: true } },
  { path: '/dashboard', redirect: '/match' },
  { path: '/profile', component: Profile, meta: { requiresAuth: true } },
  { path: '/invites', component: Invites, meta: { requiresAuth: true } },
  { path: '/login', component: Login, meta: { guestOnly: true } },
  { path: '/signup', component: Signup, meta: { guestOnly: true } },
  // Game routes share the same component. /play/:id is the anonymous
  // entry point (cookie-authenticated, Redis-only); /game/:id is the
  // durable signed-in surface (PG-backed). GameView routes API calls
  // by ID prefix so it doesn't need to know which it is.
  { path: '/play/:id', component: GameView, props: true },
  { path: '/game/:id', component: GameView, props: true, meta: { requiresAuth: true } },
  // /watch/:id is the spectator entry point — no auth required.
  // GameView detects spectator mode when the snapshot's user IDs
  // don't include the caller and is_public=true. Backend enforces:
  // /api/state on a private game still 404s, so a guessed UUID here
  // is harmless.
  { path: '/watch/:id', component: GameView, props: (route) => ({ id: route.params.id, spectator: true }) },
  { path: '/:pathMatch(.*)*', component: { template: '<div style="padding: 50px; text-align: center;"><h2>404 Page Not Found</h2><router-link to="/">Go Home</router-link></div>' } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// Guard: gate protected routes behind auth; bounce signed-in users
// away from /login and /signup. /play/:id is intentionally unguarded —
// anonymous users are the whole point.
router.beforeEach(async (to) => {
  const auth = useAuthStore();
  await auth.init();
  if (to.meta.requiresAuth && !auth.user) {
    return { path: '/login', query: { next: to.fullPath } };
  }
  if (to.meta.guestOnly && auth.user) {
    return { path: '/match' };
  }
});

export default router;
