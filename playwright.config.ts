/*!
 * Playwright — the end-to-end smoke suite.
 *
 * Runs against the real thing: the published dist/ build, served by the real
 * rtail-server bin, fed by the real rtail client bin over a real UDP socket.
 * Nothing here is stubbed, which is the point — the unit suite already covers
 * the branches, and this covers the wiring between them.
 *
 * Chromium only, in the pinned tools/Dockerfile.e2e image:
 *
 *   make test-e2e
 */

import { defineConfig, devices } from '@playwright/test'

// Container-local, so they cannot clash with a dev server on the host.
export const WEB_PORT = 8123
export const UDP_PORT = 9123

const BASE_URL = `http://127.0.0.1:${WEB_PORT}`

export default defineConfig({
  testDir: './test/e2e',
  // The app is a live log tail; a hung assertion should fail fast, not idle.
  timeout: 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],

  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  },

  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],

  // Serves dist/, so `npm run dist` has to have run first — the make target
  // and the CI job both do that before getting here.
  webServer: {
    command: [
      'node cli/rtail-server.ts',
      `--web-port ${WEB_PORT}`,
      `--udp-port ${UDP_PORT}`,
      '--web-host 127.0.0.1',
      '--udp-host 127.0.0.1'
    ].join(' '),
    url: `${BASE_URL}/`,
    reuseExistingServer: false,
    timeout: 30_000,
    env: { NO_UPDATE_NOTIFIER: '1' }
  }
})
