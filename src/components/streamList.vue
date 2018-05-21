<template>
  <div class="streamList">
    <md-toolbar class="md-transparent" md-elevation="0">
      Navigation
    </md-toolbar>
    <md-field md-clearable>
      <label>Search streams</label>
      <md-input v-model="streamsFilter"></md-input>
    </md-field>
    <h4>Favorites</h4>
    <h4>Streams</h4>
    <md-list>
      <md-list-item v-for="stream in searchFilter(streams)" :key="stream.name" v-if="!stream.childs" :to="`/streams/${stream.name}`">
        <span class="md-list-item-text">{{stream.title}}</span>
        <md-button class="md-icon-button md-list-action">
          <md-icon class="md-primary">star</md-icon>
        </md-button>
      </md-list-item>
      <md-list-item v-for="stream in streams" :key="stream.title" v-if="searchFilter(stream.childs || []).length" md-expand>
        <span class="md-list-item-text">{{stream.title}}</span>
        <md-list slot="md-expand">
          <md-list-item v-for="childStream in searchFilter(stream.childs)" :key="childStream.name" to="/streams/stream.name" class="md-inset">
            <span class="md-list-item-text">{{childStream.title}}</span>
            <md-button class="md-icon-button md-list-action">
              <md-icon class="md-primary">star</md-icon>
            </md-button>
          </md-list-item>
        </md-list>
      </md-list-item>
    </md-list>
    <div class="nsr-card-loading">
      <nsr-loading :hide-loading="isloadingComplete" :is-end-text="endText">
      </nsr-loading>
    </div>
  </div>
</template>

<script>
import { mapState } from 'vuex';

export default {
  data: () => ({
    streamsFilter: '',
  }),
  components: {
    'nsr-loading': require('../components/loading.vue'),
  },
  mounted: function() {
  },
  methods: {
    searchFilter: function (streams) {
      if (Array.isArray(streams)) {
        return streams.filter(stream => stream.name.toLowerCase().includes(this.streamsFilter.toLowerCase()));
      }

      return Object.keys(streams).reduce((result, streamId) => {
        const stream = streams[streamId];
        console.log(streamId, stream);
        if (stream.name && stream.name.toLowerCase().includes(this.streamsFilter.toLowerCase())) {
          result[streamId] = stream;
        }

        return result;
      }, {});
    },
  },
  computed: mapState({
    streams: state => state.streams,
    isloadingComplete: state => state.isStreamsLoaded,
  })
}
</script>

<style>
.streamList {
  padding: 0 5px;
}
</style>
