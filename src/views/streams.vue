<template>
  <div class="route-streams">
    <Header />
    <md-app
      md-mode="fixed"
      class="route-streams__app">
      <md-app-drawer
        class="route-streams__sidebar"
        md-permanent="full">
        <Stream-sidebar/>
      </md-app-drawer>

      <md-app-content class="route-streams__content">
        <StreamView/>
      </md-app-content>
    </md-app>
  </div>
</template>

<script>
import Header from '../components/header.vue';
import StreamView from '../components/streamView.vue';
import StreamSidebar from '../components/streamSidebar.vue';

export default {
  components: {
    Header,
    StreamView,
    StreamSidebar,
  },
};
</script>

<style>
.route-streams {
  height: 100vh;
  overflow: hidden;
  display: grid;
  grid-template-rows: 48px 1fr;
}
.route-streams__content {
  padding: 0;
  border: none;
}
.route-streams__sidebar {
  width: 250px;
}
</style>
