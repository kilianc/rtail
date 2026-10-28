/*!
 * seed.js — an hour of clean traffic, for the README screenshot.
 *
 *   node tools/screenshot/seed.js http://localhost:8888
 *
 * tools/demo-logs.js is the wrong data for a picture. It exists to walk every
 * spelling the normalizer accepts — `lvl`, `@l`, `short_message`, a payload
 * with its own `level` — and the field explorer lists every one of them, so
 * the first image anyone saw of rtail was a sidebar full of `_l` and `@m`.
 * This is what one well-behaved service logs instead, written over the hour
 * before now through /v1/ingest, with the story the caption tells: payments
 * timing out against its upstream for the last few minutes — recent enough
 * that the newest rows are the timeouts, and the histogram shows where they
 * start.
 *
 * Seeded, so a retake draws the same traffic against a later clock, and the
 * picture changes only where the app did.
 */

const BASE = process.argv[2] || 'http://localhost:8888'

const MINUTE = 60_000
const now = Date.now()
const start = now - 60 * MINUTE

const SPIKE_FROM = now - 6 * MINUTE
const SPIKE_TO = now - MINUTE / 3

// mulberry32: small, fast, and the same sequence for the same seed.
let state = 0x5eed
function random() {
  state = (state + 0x6d2b79f5) | 0
  let t = Math.imul(state ^ (state >>> 15), 1 | state)
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296
}

const pick = (list) => list[Math.floor(random() * list.length)]
const int = (min, max) => min + Math.floor(random() * (max - min))
const chance = (p) => random() < p
const hex = (length) => Array.from({ length }, () => int(0, 16).toString(16)).join('')

const ROUTES = {
  orders: [['GET', '/v1/orders'], ['GET', '/v1/orders/:id'], ['POST', '/v1/orders']],
  payments: [['POST', '/v1/checkout'], ['POST', '/v1/refunds']],
  search: [['GET', '/v1/search']],
  auth: [['POST', '/v1/login'], ['POST', '/v1/token']]
}

const SERVICES = Object.keys(ROUTES)
const REGIONS = ['eu-west-1', 'us-east-1', 'ap-southeast-2']

/*!
 * One request, as an API gateway logs it.
 *
 * Mostly INFO, with the tail a healthy service has: a few slow requests and
 * client errors at WARN, the odd 5xx. Inside the incident window, most of
 * payments times out against its upstream at thirty seconds.
 */
function request(ts) {
  const service = ts >= SPIKE_FROM && ts < SPIKE_TO && chance(0.7) ? 'payments' : pick(SERVICES)
  const [method, route] = pick(ROUTES[service])

  let level = 'info'
  let status = pick([200, 200, 200, 201, 204, 304])
  let latency = int(6, 140)
  let msg = null

  if ('payments' === service && ts >= SPIKE_FROM && ts < SPIKE_TO && chance(0.8)) {
    level = 'error'
    status = 504
    latency = int(30000, 30400)
    msg = 'upstream timeout'
  } else if (chance(0.06)) {
    level = 'warn'
    status = pick([404, 409, 429, 200])
    latency = 200 === status ? int(900, 2400) : int(20, 300)
  } else if (chance(0.012)) {
    level = 'error'
    status = pick([500, 502, 503])
    latency = int(40, 900)
  } else if (chance(0.04)) {
    level = 'debug'
  }

  return {
    ts: new Date(ts).toISOString(),
    level,
    msg: msg ?? `${method} ${route} ${status} ${latency}ms`,
    service,
    method,
    route,
    status,
    latency_ms: latency,
    region: pick(REGIONS),
    pod: `${service}-${hex(5)}`,
    trace_id: hex(32),
    user_id: chance(0.8) ? `usr_${hex(10)}` : null
  }
}

/** A background worker, so the stream picker has more than one entry. */
function job(ts) {
  const failed = chance(0.03)
  return {
    ts: new Date(ts).toISOString(),
    level: failed ? 'error' : chance(0.05) ? 'warn' : 'info',
    msg: failed ? 'job failed, will retry' : pick(['job finished', 'job started', 'batch settled']),
    queue: pick(['invoices', 'emails', 'exports']),
    attempt: failed ? int(1, 4) : 1,
    duration_ms: int(30, 4000)
  }
}

/**
 * Arrival times over the hour: a few a second, on a rate that drifts from
 * minute to minute so the histogram has a shape, and heavier in the incident.
 */
function arrivals(perSecond) {
  const out = []
  let rate = perSecond

  for (let minute = start; minute < now; minute += MINUTE) {
    rate = Math.max(perSecond * 0.5, Math.min(perSecond * 1.6, rate * (0.85 + random() * 0.3)))
    const spike = minute >= SPIKE_FROM && minute < SPIKE_TO ? 2.5 : 1
    const count = Math.round(rate * spike * 60)

    for (let i = 0; i < count; i++) out.push(minute + Math.floor(random() * MINUTE))
  }

  return out.filter((ts) => ts < now).sort((a, b) => a - b)
}

async function send(stream, records) {
  for (let i = 0; i < records.length; i += 2000) {
    const batch = records.slice(i, i + 2000)
    const response = await fetch(`${BASE}/v1/ingest?stream=${stream}`, {
      method: 'POST',
      body: batch.map((record) => JSON.stringify(record)).join('\n') + '\n'
    })

    const body = await response.json()
    if (!response.ok || body.accepted !== batch.length) {
      throw new Error(`ingesting ${stream}: ${response.status} ${JSON.stringify(body)}`)
    }
  }

  console.log(`  ${stream}: ${records.length} records`)
}

await send('api', arrivals(3).map(request))
await send('worker', arrivals(0.2).map(job))
