import Vue from 'vue'; // get vue
import Vuex from 'vuex'; // get vuex

Vue.use(Vuex);

const defaultState = {
  isConnected: false,
  isStreamsLoaded: false,
  streams: {},
  activeStreams: [{ id: 2 }, { id: 3 }],
};

const getters = {};

const mutations = {
  SOCKET_CONNECT(state, status) {
    state.isConnected = true;
  },
  SOCKET_DISCONNECT(state) {
    state.isConnected = false;
  },
  SOCKET_STREAMS(state, message) {
    state.isStreamsLoaded = true;
    state.streams = message[0].reduce((result, stream) => {
      if (!stream.group) {
        result[stream.id] = {
          name: stream.id,
          title: stream.name,
        };
        return result;
      }

      if (!result[stream.group]) {
        result[stream.group] = {
          title: stream.group,
          childs: [],
        };
      }

      result[stream.group].childs.push({
        name: stream.id,
        title: stream.name,
      });

      return result;
    }, {});
  },
};

const actions = {
  socket_streams(context, streamList) {
    return true;
  },
  getStream(context, { streamId }) {
    context.$socket.emit('streamSubscribe', streamId);
  },
};

const store = new Vuex.Store({
  state: defaultState,
  getters,
  mutations,
  actions,
});

export default store;
