import Vue from 'vue';
import VueRouter from 'vue-router';

import store from '../store';
import Streams from '../views/streams.vue';

Vue.use(VueRouter);

const routes = [
  { path: '/', redirect: '/streams' },
  {
    name: 'streams',
    path: '/streams/(.*)?',
    component: Streams,
    beforeEnter: (to, from, next) => {
      let streamList = null;
      try {
        streamList = JSON.parse(to.params[0]);
      } catch (e) {}
      if (streamList && streamList.length) {
        store.commit('activeStreamsReplace', { streamList });
        next({ path: '/streams', replace: true });
        return;
      }

      next();
    },
  },
];

const router = new VueRouter({ routes });

export default router;
