import 'vue-material/dist/vue-material.min.css';
import 'vue-material/dist/theme/default.css';
import 'vue-hljs/dist/vue-hljs.min.css';

import Vue from 'vue';
import VueSocketio from 'vue-socket.io';
import VueProgressBar from 'vue-progressbar';
import VueMaterial from 'vue-material';
import VueHljs from 'vue-hljs';

import App from './app.vue';
import router from './router';
import store from './store';

const options = {
  color: '#fff',
  failedColor: '#874b4b',
  thickness: '3px',
  transition: {
    speed: '0.2s',
    opacity: '0.6s',
  },
  autoRevert: true,
  location: 'top',
  inverse: false,
};
Vue.use(VueProgressBar, options);
Vue.use(VueMaterial);
Vue.use(VueHljs);
// Vue.use(VueSocketio, `${document.location.origin}${document.location.pathname}`, store);
Vue.use(VueSocketio, 'http://localhost:8888', store);

window.app = new Vue({
  router,
  store,
  render: h => h(App),
}).$mount('#app');

router.afterEach((to, from) => {
  store.commit('ROUTE_CHANGED', { to, from });

  if (to.params.streamId) {
    const opts = { streamId: to.params.streamId };
    // if (from.params.streamId) opts.rewriteStreamId = from.params.streamId;

    store.commit('activeStreamAdd', opts);
  }
});

