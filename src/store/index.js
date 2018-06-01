import Vue from 'vue';
import Vuex from 'vuex';
import { orderBy } from 'lodash';

import { formatLine } from '../tools/common';

Vue.use(Vuex);

const MAX_ACTIVE_STREAMS = 5;

const defaultState = {
  isConnected: false,
  isStreamsLoaded: false,
  backlogByStream: {},
  streams: {},
  activeStreams: ['tired-nest'],
  routeParams: {},
};

const filterFavoriteStreams = function filterFavoriteStreams(streams, isFavorite = false) {
  return Object.keys(streams).reduce((result, streamId) => {
    const stream = streams[streamId];
    if (stream.childs) {
      result[streamId] = Object.assign({}, stream, { childs: {} });
      result[streamId].childs = filterFavoriteStreams(stream.childs, isFavorite);
    }

    if (stream.isFavorite === isFavorite) {
      result[streamId] = stream;
    }

    return result;
  }, {});
};

const getters = {
  backlogDESC: state => (streamId) => {
    if (!state.backlogByStream[streamId]) {
      Vue.set(state.backlogByStream, streamId, { backlog: [] });
    }

    return orderBy(state.backlogByStream[streamId].backlog, 'timestamp', ['desc']);
  },
  streamsFavorites: state => filterFavoriteStreams(state.streams, true),
  streamsNotFavorites: state => filterFavoriteStreams(state.streams, false),
  activeStreamIs: state => streamId => state.activeStreams.includes(streamId),
  activeStreamIsLast: state => streamId => state.activeStreams.indexOf(streamId) === (state.activeStreams.length - 1),
  activeStreamIsFirst: state => streamId => state.activeStreams.indexOf(streamId) === 0,
};

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
          isFavorite: false,
        };
        return result;
      }

      if (!result[stream.group]) {
        result[stream.group] = {
          title: stream.group,
          childs: {},
        };
      }

      result[stream.group].childs[stream.id] = {
        name: stream.id,
        title: stream.name,
        isFavorite: false,
      };

      return result;
    }, {});
  },

  SOCKET_BACKLOG(state, message) {
    const data = message[0];
    if (!state.backlogByStream[data.id]) {
      Vue.set(state.backlogByStream, data.id, { backlog: [] });
    }

    const stream = state.backlogByStream[data.id];
    Vue.set(stream, 'name', data.name);
    Vue.set(stream, 'group', data.group);
    Vue.set(stream, 'backlogLimit', data.backlogLimit);
    stream.backlog.splice(0);
    data.backlog.forEach(line => state.backlogByStream[data.id].backlog.splice(Infinity, 0, formatLine(line)));
  },

  SOCKET_LINE(state, message) {
    const data = message[0];
    if (!state.backlogByStream[data.id]) return;

    const { backlog } = state.backlogByStream[data.id];
    backlog.splice(backlog.length, 0, formatLine(data));
    if (backlog.length > state.backlogByStream[data.id].backlogLimit) {
      backlog.shift();
    }
  },

  toggleFavorite(state, { streamId, group }) {
    let { streams } = state;
    if (group) {
      streams = state.streams[group].childs;
    }

    streams[streamId].isFavorite = !streams[streamId].isFavorite;
  },

  activeStreamAdd(state, { streamId, rewriteStreamId }) {
    if (state.activeStreams.includes(streamId) || state.activeStreams.length >= MAX_ACTIVE_STREAMS) {
      return;
    }

    if (rewriteStreamId && state.activeStreams.includes(rewriteStreamId)) {
      const idx = state.activeStreams.indexOf(rewriteStreamId);
      state.activeStreams.splice(idx, 1, streamId);
    } else {
      state.activeStreams.splice(Infinity, 0, streamId);
    }
  },

  activeStreamClose(state, { streamId }) {
    const idx = state.activeStreams.indexOf(streamId);
    if (idx < 0) {
      return;
    }

    state.activeStreams.splice(idx, 1);
  },

  activeStreamMove(state, { streamId, direction }) {
    const idx = state.activeStreams.indexOf(streamId);
    if (idx < 0) {
      return;
    }

    state.activeStreams.splice(idx, 1);
    state.activeStreams.splice(Math.max(idx - direction, 0), 0, streamId);
  },
};

const actions = {};

const store = new Vuex.Store({
  state: defaultState,
  getters,
  mutations,
  actions,
});

export default store;
