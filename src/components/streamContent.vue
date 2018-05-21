<template>
  <div>
    <md-toolbar class="md-dense">
      <span class="md-title">My Title - {{streamId}}</span>
    </md-toolbar>
    <p>Lorem ipsum dolor sit amet, consectetur adipisicing elit. Error quibusdam, non molestias et! Earum magnam, similique, quo recusandae placeat dicta asperiores modi sint ea repudiandae maxime? Quae non explicabo, neque.</p>
  </div>
</template>

<script>
import {mapState} from 'vuex';

export default {
  props: ['streamId'],
  data: () => ({}),
  components: {
  },
  mounted: function() {
    this.$nextTick(function () {
      this.fetchData();
    });
  },
  methods: {
    fetchData: function() {
      this.$socket.emit('streamSubscribe', this.$props.streamId);
    },
  },
  computed: mapState({
    activeStreams: state => state.activeStreams,
    isloadingComplete: state => state.isStreamsLoaded,
  })
}
</script>

<style>
</style>
