import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import router from './router';

const app = createApp(App);
const pinia = createPinia();

app.config.errorHandler = (err, instance, info) => {
  console.error('Global Vue Error:', err);
  console.error('Info:', info);
};

app.use(pinia);
app.use(router);
app.mount('#app');
