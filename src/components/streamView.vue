<template>
  <div class="stream-view">
    <streamContent
      v-for="streamId in activeStreams"
      :key="streamId"
      :stream-id="streamId"/>
  </div>
</template>

<script>
import { mapState } from 'vuex';

import streamContent from '../components/streamContent.vue';

export default {
  components: {
    streamContent,
  },
  computed: mapState({
    activeStreams: state => state.activeStreams,
    isloadingComplete: state => state.isStreamsLoaded,
  }),
};
</script>

<style>
.stream-view {
  display: grid;
  height: 100%;
  grid-auto-rows: 1fr;
  background: #282828;
}
.stream-view > div {
  overflow: hidden;
}
</style>
