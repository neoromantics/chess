import { createRouter, createWebHistory, RouteRecordRaw } from 'vue-router';
import Dashboard from './views/Dashboard.vue';
import Login from './views/Login.vue';
import Signup from './views/Signup.vue';
import GameView from './views/GameView.vue';

const routes: RouteRecordRaw[] = [
  { path: '/', component: Dashboard },
  { path: '/login', component: Login },
  { path: '/signup', component: Signup },
  { path: '/game/:id', component: GameView, props: true },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

export default router;
