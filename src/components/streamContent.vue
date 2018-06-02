<template>
  <div
    :style="`font-size: ${fontSize}px;`"
    class="stream-content">
    <md-toolbar
      class="md-dense">
      <span class="md-caption">{{ streamId }}</span>
      <div class="md-toolbar-section-end stream-content__header-buttons">
        <md-button
          v-if="!activeStreamIsLast(streamId)"
          class="md-icon-button"
          @click="streamMoveDown(streamId)">
          <md-icon>keyboard_arrow_down</md-icon>
        </md-button>
        <md-button
          v-if="!activeStreamIsFirst(streamId)"
          class="md-icon-button"
          @click="streamMoveUp(streamId)">
          <md-icon>keyboard_arrow_up</md-icon>
        </md-button>
        <md-button
          v-if="!activeStreamIsFirst(streamId) || !activeStreamIsLast(streamId)"
          class="md-icon-button"
          @click="streamClose(streamId)">
          <md-icon>clear</md-icon>
        </md-button>
      </div>
    </md-toolbar>
    <md-content class="backlog md-scrollbar">
      <div>
        <div
          v-for="line in orderedBacklog"
          :key="line.uid"
          class="backlog__line-container hljs">
          <div
            class="backlog__line-timestamp">
            {{ line.timestamp | dateFormat }}
          </div>
          <div
            v-highlight
            v-if="line.isJSON"
            class="backlog__line-content backlog__line-content-highlight">
            <pre><code class="json">{{ line.html }}</code></pre>
          </div>
          <div
            v-if="!line.isJSON"
            class="backlog__line-content backlog__line-content-text"
            v-html="line.html"/>
        </div>
      </div>
    </md-content>
  </div>
</template>

<script>
import fecha from 'fecha';
import { mapState, mapGetters } from 'vuex';

export default {
  components: {},
  filters: {
    dateFormat(ts) {
      return fecha.format(new Date(ts), 'MM/DD/YY hh:mm:ss');
    },
  },
  props: {
    streamId: {
      type: String,
      required: true,
    },
  },
  data: () => ({}),
  computed: {
    ...mapGetters([
      'activeStreamIsLast',
      'activeStreamIsFirst',
    ]),
    ...mapState({
      orderedBacklog(state) {
        return this.$store.getters.backlogDESC(this.$props.streamId);
      },
      isloadingComplete: state => state.isStreamsLoaded,
      fontSize: state => state.settings.fontSize,
    }),
  },
  mounted() {
    this.$nextTick(function () {
      this.$socket.emit('streamSubscribe', this.$props.streamId);
    });
  },
  beforeDestroy() {
    this.$socket.emit('streamUnsubscribe', this.$props.streamId);
  },
  methods: {
    streamMoveUp(streamId) {
      this.$store.commit('activeStreamMove', { streamId, direction: 1 });
    },
    streamMoveDown(streamId) {
      this.$store.commit('activeStreamMove', { streamId, direction: -1 });
    },
    streamClose(streamId) {
      this.$store.commit('activeStreamClose', { streamId });
    },
  },
};
</script>

<style>
.backlog {
  max-height: calc(100% - 48px);
  overflow: auto;
}
.backlog__line-container.hljs {
  padding: 0;
}
.backlog__line-container {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  border-bottom: 1px solid rgba(255, 255, 255, 0.2);
}
.backlog__line-timestamp {
  width: 105px;
  font-size: 0.8em;
  padding-left: 5px;
}
.backlog__line-container {
  line-height: 1.1em;

  pre {
    margin: 0;
  }
}
.backlog__line-content {
  padding-left: 5px;
}
</style>
