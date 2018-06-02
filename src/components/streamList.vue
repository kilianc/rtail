<template>
  <div class="stream-list">
    <md-list>
      <md-list-item
        v-for="(stream, streamId) in searchFilter(streams)"
        v-if="!stream.childs"
        :key="stream.name"
        @click="changeLastStream(streamId)">
        <span class="md-list-item-text">{{ stream.title }}</span>
        <md-button
          v-if="!activeStreamIs(streamId)"
          class="md-icon-button md-list-action"
          @click="activeStreamAdd($event, streamId)">
          <md-icon class="md-primary">add</md-icon>
        </md-button>
        <md-button
          class="md-icon-button md-list-action"
          @click="toggleFavorite($event, streamId)">
          <md-icon class="md-primary">{{ stream.isFavorite ? 'star' : 'star_border' }}</md-icon>
        </md-button>
      </md-list-item>
      <md-list-item
        v-for="stream in streams"
        v-if="Object.keys(searchFilter(stream.childs || {})).length"
        :key="stream.title"
        md-expand>
        <span class="md-list-item-text">{{ stream.title }}</span>
        <md-list
          slot="md-expand"
          class="stream-list__childs-list">
          <md-list-item
            v-for="(childStream, childStreamId) in searchFilter(stream.childs)"
            :key="childStream.name"
            class="md-inset"
            @click="changeLastStream(childStreamId)">
            <span class="md-list-item-text">{{ childStream.title }}</span>
            <md-button
              v-if="!activeStreamIs(childStreamId)"
              class="md-icon-button md-list-action"
              @click="activeStreamAdd($event, childStreamId)">
              <md-icon class="md-primary">add</md-icon>
            </md-button>
            <md-button
              class="md-icon-button md-list-action"
              @click="toggleFavorite($event, childStreamId, stream.title)">
              <md-icon class="md-primary">{{ childStream.isFavorite ? 'star' : 'star_border' }}</md-icon>
            </md-button>
          </md-list-item>
        </md-list>
      </md-list-item>
    </md-list>
  </div>
</template>

<script>
import { mapState, mapGetters } from 'vuex';

export default {
  props: {
    streams: {
      type: Object,
      required: true,
    },
    streamsFilter: {
      type: String,
      required: true,
    },
  },
  computed: {
    ...mapGetters(['activeStreamIs']),
    ...mapState({
      isloadingComplete: state => state.isStreamsLoaded,
    }),
  },
  methods: {
    searchFilter(streams = {}) {
      const streamsFilter = (this.streamsFilter || '').toLowerCase();
      if (Array.isArray(streams)) {
        return streams.filter(stream => stream.name.toLowerCase().includes(streamsFilter));
      }

      return Object.keys(streams).reduce((result, streamId) => {
        const stream = streams[streamId];
        if (stream.name && stream.name.toLowerCase().includes(streamsFilter)) {
          result[streamId] = stream;
        }

        return result;
      }, {});
    },

    toggleFavorite(event, streamId, group = null) {
      if (event) event.preventDefault();
      this.$store.commit('toggleFavorite', { streamId, group });
    },

    activeStreamAdd(event, streamId) {
      if (event) event.preventDefault();
      this.$store.commit('activeStreamAdd', { streamId });
    },

    changeLastStream(streamId) {
      this.$store.commit('activeStreamAdd', { streamId, replaceLastStream: true });
    },
  },
};
</script>

<style>
.stream-list .stream-list__childs-list .md-list-item-content {
  padding-left: 25px;
}
</style>
