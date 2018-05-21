<template>
  <div>
    <streamContent v-for="stream in activeStreams" :key="stream.id" :streamId="stream.id"></streamContent>
  </div>
</template>

<script>
import { mapState } from 'vuex';

import streamContent from '../components/streamContent.vue';

export default {
  data: () => ({}),
  components: {
    streamContent,
    'nsr-loading': require('../components/loading.vue'),
  },
  computed: mapState({
    activeStreams: state => state.activeStreams,
    isloadingComplete: state => state.isStreamsLoaded,
  })
}
</script>

<style>
</style>
