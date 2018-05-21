<template>
  <div>
    <md-toolbar class="md-accent" md-elevation="1">
      <h3 class="md-title" style="flex: 1">Title</h3>
      <md-button class="md-primary">Settings</md-button>
    </md-toolbar>

    <md-app>
      <md-app-drawer md-permanent="full">
        <stream-list></stream-list>
      </md-app-drawer>

      <md-app-content>
        <streamView></streamView>
      </md-app-content>
    </md-app>
  </div>
</template>

<script>
import streamList from '../components/streamList.vue';
import streamView from '../components/streamView.vue';

export default {
  components: {
    streamList,
    streamView,
  },
}
</script>

<style>

</style>
