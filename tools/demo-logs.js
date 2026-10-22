/*!
 * demo-logs.js — representative log lines on stdout, for piping into rtail.
 *
 *   node tools/demo-logs.js api-gateway | bin/rtail --id api-gateway
 *
 * tools/dev.js runs one of these per demo stream, which is also how you check
 * that switching streams no longer interleaves their output.
 *
 * The point of this file is *variety*. It used to emit eleven fixed strings
 * and one JSON shape, none of which carried a level — the INFO and DEBUG in
 * them were ANSI colour inside the message text, not a field — so a local app
 * had an empty severity panel, no error row anywhere, a flat histogram and
 * one payload shape to look at. Everything the normalizer knows how to do was
 * invisible on the only data anybody develops against.
 *
 * So the generator below deliberately covers:
 *
 *   · every canonical level, through every spelling the normalizer accepts —
 *     `level`, `severity`, `lvl`, `@l`, and pino's numeric 10–60
 *   · an unrecognised level, which should be kept, upper-cased, not dropped
 *   · each message and timestamp alias — msg, short_message, @m, ts, time
 *   · payloads that are nested, arrayed, null, empty, unicode, very long,
 *     very numeric, and one whose own `level` key collides with the envelope
 *   · plain text with ANSI, a multi-line stack trace, and text that merely
 *     looks like JSON
 *   · a burst of errors every so often, so the histogram has a real spike
 *     with a red band in it rather than a flat blue field
 *
 * Each stream gets its own vocabulary, so the field explorer is a different
 * shape depending on which one you are looking at.
 */

const stream = process.argv[2] || 'demo'

const pick = (list) => list[Math.floor(Math.random() * list.length)]
const int = (max) => Math.floor(Math.random() * max)
const chance = (p) => Math.random() < p

const say = (value) =>
  process.stdout.write(('string' === typeof value ? value : JSON.stringify(value)) + '\n')

/*!
 * Levels, weighted the way a healthy service actually logs.
 *
 * Mostly INFO with a tail of everything else — a uniform draw across ten
 * severities makes a system that is permanently on fire, which is no better a
 * test of the interface than one that never is.
 */
const LEVELS = [
  ...Array(26).fill('info'),
  ...Array(8).fill('debug'),
  ...Array(6).fill('warn'),
  ...Array(4).fill('error'),
  ...Array(2).fill('trace'),
  'notice',
  'critical',
  'alert',
  'emergency',
  'fatal',
  // Spellings the normalizer maps onto the same canonical names, and one it
  // does not know — "audit" should survive as AUDIT rather than disappearing.
  'err',
  'warning',
  'dbg',
  'verbose',
  'panic',
  'audit'
]

const ROUTES = ['/1/config', '/1/signin', '/1/checkout', '/1/me/profile', '/1/geocode', '/healthz']
const REGIONS = ['eu-west-1', 'us-east-1', 'ap-southeast-2']
const HOSTS = ['ip-10-0-4-21', 'ip-10-0-7-88', 'ip-10-0-2-15']

/*!
 * One request, as the gateway logs it.
 *
 * The level is spelled a different way each time so the normalizer's whole
 * vocabulary gets walked: a record that says `severity: "err"` and one that
 * says `level: 50` should both end up ERROR and sit in the same bucket.
 */
function request() {
  const level = pick(LEVELS)
  const status = 'error' === level || 'err' === level ? pick([500, 502, 503]) : pick([200, 201, 301, 400, 404])
  const latency = 'error' === level ? 2000 + int(6000) : int(900)

  const record = {
    event: 'http.request',
    route: pick(ROUTES),
    method: pick(['GET', 'POST', 'PUT', 'DELETE']),
    status,
    latency_ms: latency,
    region: pick(REGIONS),
    host: pick(HOSTS),
    trace_id: Math.random().toString(16).slice(2, 12),
    user_id: chance(0.7) ? '556605ede9fa35333befa9e6' : null,
    retry: chance(0.15),
    cache: pick(['hit', 'miss', 'bypass'])
  }

  // The same level, said four different ways.
  const dialect = int(4)
  if (0 === dialect) record.level = level
  else if (1 === dialect) record.severity = level
  else if (2 === dialect) record['@l'] = level
  else record.lvl = level

  // And the message under each of its aliases.
  const text = `${record.method} ${record.route} ${status} ${latency}ms`
  const alias = int(4)
  if (0 === alias) record.message = text
  else if (1 === alias) record.msg = text
  else if (2 === alias) record.short_message = text
  else record['@m'] = text

  return record
}

/** A payload shaped like something a billing worker would emit. */
function billing() {
  return {
    level: pick([30, 40, 50, 20, 10, 60]), // pino's numeric scale, 10 through 60
    msg: pick(['charge captured', 'charge declined', 'refund issued', 'invoice settled']),
    ts: new Date().toISOString(),
    invoice: {
      id: `in_${Math.random().toString(36).slice(2, 10)}`,
      amount_cents: int(400000),
      currency: pick(['EUR', 'USD', 'GBP']),
      lines: [
        { sku: 'seat', qty: 1 + int(40), unit_cents: 1200 },
        { sku: 'support', qty: chance(0.4) ? 1 : 0, unit_cents: 9900 }
      ]
    },
    customer: { id: `cus_${int(99999)}`, tier: pick(['free', 'team', 'enterprise']), trial: chance(0.2) },
    provider: pick(['stripe', 'adyen']),
    // A value that is deliberately awkward: unicode, an empty string, a float,
    // a negative, and a number past 2^53 as a string.
    note: pick(['', 'retry après échec', '請求済み', 'ok ✓', '🙂 manual review']),
    rate: Number((Math.random() * 2 - 1).toFixed(6)),
    ledger_seq: String(9007199254740993n + BigInt(int(1000)))
  }
}

/** nginx-ish access logs, flat and wide. */
function access() {
  const status = pick([200, 200, 200, 206, 301, 304, 404, 499, 500, 502])
  return {
    severity_text: status >= 500 ? 'ERROR' : status >= 400 ? 'WARN' : 'INFO',
    text: `${pick(HOSTS)} - - "${pick(['GET', 'HEAD', 'POST'])} ${pick(ROUTES)}" ${status}`,
    '@timestamp': new Date().toISOString(),
    status,
    bytes: int(2_000_000),
    upstream_ms: Number((Math.random() * 400).toFixed(3)),
    scheme: pick(['https', 'http']),
    tls: pick(['TLSv1.3', 'TLSv1.2']),
    agent: pick([
      'curl/8.4.0',
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36',
      ''
    ]),
    referer: chance(0.5) ? 'https://example.com/a/very/long/path?with=query&and=more#fragment' : null
  }
}

/*!
 * The awkward cases, drawn rarely.
 *
 * Each one is here because it broke something, or could: a payload with its
 * own `level` key that must not be listed twice, a `time` field holding a
 * duration rather than an instant, a value deep enough to test the detail
 * panel's nesting, and plain text that merely looks like JSON.
 */
const ODDITIES = [
  () => ({ level: 'warn', msg: 'payload carries its own level', level_reported: 'debug', source: 'legacy-shim' }),
  () => ({ level: 'info', msg: 'duration, not an instant', time: 500, unit: 'ms' }),
  () => ({
    level: 'debug',
    msg: 'deeply nested',
    ctx: { request: { headers: { accept: 'application/json', 'x-request-id': 'abc-123' }, body: { items: [1, 2, 3] } } }
  }),
  () => ({ level: 'info', msg: 'empty and null everywhere', empty: '', nothing: null, zero: 0, no: false, list: [] }),
  () => ({ level: 'trace', msg: 'x'.repeat(600), kind: 'a very long single value' }),
  () => '{ "looks": "like json", but: it is not parseable }',
  () => '\u001b[90mapi:logs\u001b[0m \u001b[32m200\u001b[0m GET /1/config',
  () => '\u001b[90mapi:logs\u001b[0m \u001b[31m500\u001b[0m POST /1/signin \u001b[91mconnection reset by peer\u001b[0m',
  () => 'deploy \u001b[92m✔\u001b[0m build succeeded in \u001b[96m1m 04s\u001b[0m',
  () => 'A                    B                        C',
  () => '<script>alert(1)</script>',
  () =>
    'Traceback (most recent call last):\n  File "worker.py", line 42, in handle\n    settle(invoice)\n  File "billing.py", line 7, in settle\n    raise PaymentError("card_declined")\nPaymentError: card_declined'
]

const SHAPES = {
  'api-gateway': request,
  'worker-billing': billing,
  'nginx-access': access
}

const shape = SHAPES[stream] ?? request

/*!
 * A burst of errors every minute or so.
 *
 * A flat arrival rate makes a flat histogram, and a flat histogram cannot
 * show that the chart stacks by severity, that dragging it selects a window,
 * or that the peak readout tracks anything. The spike is what the interface
 * is for.
 */
let burst = 0

setInterval(() => {
  if (0 === burst && chance(0.012)) burst = 25 + int(25)

  if (burst > 0) {
    burst -= 1
    say({ ...shape(), level: pick(['error', 'error', 'critical', 'warn']), incident: 'upstream-timeout' })
    return
  }

  if (chance(0.08)) {
    say(ODDITIES[int(ODDITIES.length)]())
    return
  }

  say(shape())
}, 240)
