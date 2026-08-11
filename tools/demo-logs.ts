/**
 * Emit representative log lines on stdout, for piping into `rtail`.
 *
 *   node tools/demo-logs.ts api-gateway | node cli/rtail-client.ts --id api-gateway
 *
 * Several of these are run side by side by tools/dev.ts so the sidebar has
 * more than one stream in it — which is also how you check that switching
 * streams no longer interleaves their output.
 */

const stream = process.argv[2] || 'demo'

const LINES = [
  '[90mapi:logs[0m [32m200[0m GET /1/config',
  '[90mapi:logs[0m [32m200[0m GET /1/geocode?address=ny',
  '[90mapi:logs[0m [33m301[0m GET /1/users/556605ede9fa35333befa9e6/profile',
  '[90mapi:logs[0m [31m500[0m POST /1/signin [91mconnection reset by peer[0m',
  '[34mINFO[0m  cache warmed in [96m142ms[0m',
  '[35mDEBUG[0m pool=[93m8[0m/[93m32[0m idle=[92m24[0m',
  '[90mapi:logs[0m [32m200[0m PUT /1/me/review_status/seen',
  '[90mapi:logs[0m [31m400[0m PUT /1/me/gcm_tokens/3G7ggYFcGXIHkIgaGLW16s4',
  'deploy [92m✔[0m build succeeded in [96m1m 04s[0m',
  'A                    B                        C',
  '<script>alert(1)</script>'
]

setInterval(() => {
  if (Math.random() < 0.2) {
    // Object lines exercise the JSON highlighting path.
    process.stdout.write(JSON.stringify({
      stream,
      event: 'checkout.completed',
      count: Math.floor(Math.random() * 1000),
      ok: Math.random() > 0.3,
      region: 'eu-west-1',
      list: ['foo', 'bar'],
      doc: { foo: 'bar' }
    }) + '\n')
    return
  }

  process.stdout.write(LINES[Math.floor(Math.random() * LINES.length)] + '\n')
}, 900)
