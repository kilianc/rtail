<template>
  <div class="route-streams">
    <md-toolbar
      class="md-accent"
      md-elevation="1">
      <h3
        class="md-title"
        style="flex: 1">Title</h3>
      <md-button class="md-primary">Settings</md-button>
    </md-toolbar>

    <md-app md-mode="fixed">
      <md-app-drawer
        class="route-streams__sidebar"
        md-permanent="full">
        <stream-sidebar/>
      </md-app-drawer>

      <md-app-content class="route-streams__content">
        <streamView/>
      </md-app-content>
    </md-app>
  </div>
</template>

<script>
import streamSidebar from '../components/streamSidebar.vue';
import streamView from '../components/streamView.vue';

export default {
  components: {
    streamSidebar,
    streamView,
  },
};
</script>

<style>
.route-streams {
  height: 100vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.route-streams__content {
  padding: 0;
}
.route-streams__sidebar {
  width: 250px;
}
</style>
