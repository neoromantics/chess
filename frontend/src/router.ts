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
  { path: '/:pathMatch(.*)*', component: { template: '<div style="padding: 50px; text-align: center;"><h2>404 Page Not Found</h2><router-link to="/">Go Home</router-link></div>' } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

export default router;
