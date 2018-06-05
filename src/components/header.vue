<template>
  <div class="app-header">
    <md-toolbar
      class="md-accent app-header__toolbar"
      md-elevation="1">
      <h3
        class="md-title"
        style="flex: 1">
        <md-icon class="md-primary md-size-2x">accessible_forward</md-icon>
        r-Tail
      </h3>
      <md-button
        class="md-icon-button md-primary"
        @click="copyUrl()">
        <md-icon class="md-primary">share</md-icon>
        <md-tooltip md-direction="bottom">Share link</md-tooltip>
        <input
          id="shareLink"
          :value="shareUrl"
          type="hidden">
      </md-button>
      <md-button
        class="md-icon-button md-primary"
        @click="showSettings = true">
        <md-icon class="md-primary">settings</md-icon>
      </md-button>
    </md-toolbar>

    <md-dialog :md-active.sync="showSettings">
      <md-dialog-title>Settings</md-dialog-title>
      <md-dialog-content>
        <md-subheader>Font size:</md-subheader>
        <div>
          <md-button
            :disabled="fontSize <= minFontSize"
            class="md-icon-button md-dense md-primary"
            @click="changeFontsize(-1)">
            <md-icon>text_rotate_vertical</md-icon>
          </md-button>

          <md-button
            :disabled="settingIsActive('fontSize', defaultFontSize)"
            class="md-icon-button md-dense md-primary"
            @click="changeFontsize(0)">
            <md-icon>format_clear</md-icon>
          </md-button>

          <md-button
            :disabled="fontSize >= maxFontSize"
            class="md-icon-button md-dense md-primary"
            @click="changeFontsize(1)">
            <md-icon>text_rotate_up</md-icon>
          </md-button>
        </div>

        <md-subheader>Sorting:</md-subheader>
        <div>
          <md-button
            :disabled="settingIsActive('sorting', 'acs')"
            class="md-icon-button md-dense md-primary"
            @click="changeSorting('acs')">
            <md-icon>arrow_downward</md-icon>
          </md-button>

          <md-button
            :disabled="settingIsActive('sorting', 'desc')"
            class="md-icon-button md-dense md-primary"
            @click="changeSorting('desc')">
            <md-icon>arrow_upward</md-icon>
          </md-button>
        </div>

        <md-subheader>Theme:</md-subheader>
        <div>
          <md-button
            :disabled="settingIsActive('theme', 'dark')"
            class="md-icon-button md-dense md-primary"
            @click="changeTheme('dark')">
            <md-icon>brightness_3</md-icon>
          </md-button>

          <md-button
            :disabled="settingIsActive('theme', 'white')"
            class="md-icon-button md-dense md-primary"
            @click="changeTheme('white')">
            <md-icon>wb_sunny</md-icon>
          </md-button>
        </div>
      </md-dialog-content>
      <md-dialog-actions>
        <md-button
          class="md-primary"
          @click="showSettings = false">
          Close
        </md-button>
      </md-dialog-actions>
    </md-dialog>

    <md-snackbar
      :md-active.sync="snackbarShow"
      md-position="left"
      md-duration="5000"
      md-persistent>
      <span>{{ snackbarText }}</span>
    </md-snackbar>
  </div>
</template>

<script>
import { mapState, mapGetters } from 'vuex';

export default {
  data: () => ({
    showSettings: false,
    defaultFontSize: 14,
    minFontSize: 6,
    maxFontSize: 32,
    snackbarShow: false,
    snackbarText: 'Oops. Sorry',
  }),

  computed: {
    ...mapGetters(['settingIsActive']),
    ...mapState({
      fontSize: state => state.settings.fontSize,
      activeStreams: state => state.activeStreams,
      shareUrl: state => `${window.location.origin}/#/streams/${JSON.stringify(state.activeStreams)}`,
    }),
  },

  methods: {
    changeFontsize(type) {
      let { fontSize } = this.$store.state.settings;
      if (type === 0) {
        fontSize = this.defaultFontSize;
      } else {
        fontSize += type;
      }

      if (fontSize < this.minFontSize || fontSize > this.maxFontSize) return;

      this.$store.commit('updateSettings', { fontSize });
    },

    changeSorting(type) {
      this.$store.commit('updateSettings', { sorting: type });
    },

    changeTheme(type) {
      this.$store.commit('updateSettings', { theme: type });
    },

    copyUrl() {
      const testingCodeToCopy = document.querySelector('#shareLink');
      testingCodeToCopy.setAttribute('type', 'text');
      testingCodeToCopy.select();

      try {
        const successful = document.execCommand('copy');
        const msg = successful ? 'successful' : 'unsuccessful';
        this.snackbarText = `Testing code was copied ${msg}`;
      } catch (err) {
        this.snackbarText = 'Oops, unable to copy';
      }

      this.snackbarShow = true;
      testingCodeToCopy.setAttribute('type', 'hidden');
      window.getSelection().removeAllRanges();
    },
  },
};
</script>

<style>
.app-header {
  height: 48px;
  max-height: 48px;
  overflow: hidden;
}

.app-header__toolbar {
  min-height: 48px;
}
</style>
