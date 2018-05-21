import Vue from 'vue';
import VueRouter from 'vue-router';

import Streams from '../views/streams.vue';

Vue.use(VueRouter);

const routes = [
  { path: '/', redirect: '/streams' },
  { path: '/streams', component: Streams },
  { path: '/streams/:streamId', component: Streams },
];

const router = new VueRouter({ routes });

export default router;
