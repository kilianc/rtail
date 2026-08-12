/*!
 * The landing page's stand-in for the socket.io connection.
 *
 * The preview on the home page is the real webapp — the same components, the
 * same ANSI and JSON formatting, the same stylesheet. Only the transport is
 * fake, and it has to be: rtail ingests over UDP and keeps a socket.io
 * connection open, and a static Vercel deployment does neither.
 *
 * tools/build-site.js resolves the app's `lib/connection.js` to this module,
 * so app/src is not modified and cannot tell the difference — it subscribes to
 * a stream, receives a backlog, then receives lines. This file therefore
 * mirrors rtail-server's socket contract exactly (see cli/rtail-server.js):
 * one `streams` event on connect, a `backlog` reply to every non-null
 * `select stream`, and `line` events only for the selected stream, while every
 * stream's ring buffer keeps filling in the background.
 */

import type { WireLine } from '../../app/src/lib/types.ts'

/** Matches BACKLOG_SIZE's default in rtail-server. */
const BACKLOG_SIZE = 100

/** How often a new line lands, in ms. Slow enough to read, fast enough to live. */
const TICK = 550

/**
 * How often that line belongs to the stream the visitor is looking at.
 *
 * A uniform pick across three streams would put barely one line a second in
 * front of them, which reads as stalled rather than live. The remainder still
 * goes to the other streams, so their backlogs are warm when one is selected.
 */
const SELECTED_SHARE = 0.65

const ESC = ''
const reset = `${ESC}[0m`
const dim = (s: string) => `${ESC}[90m${s}${reset}`
const red = (s: string) => `${ESC}[31m${s}${reset}`
const green = (s: string) => `${ESC}[32m${s}${reset}`
const yellow = (s: string) => `${ESC}[33m${s}${reset}`
const blue = (s: string) => `${ESC}[34m${s}${reset}`
const magenta = (s: string) => `${ESC}[35m${s}${reset}`
const cyan = (s: string) => `${ESC}[96m${s}${reset}`
const brightRed = (s: string) => `${ESC}[91m${s}${reset}`
const brightGreen = (s: string) => `${ESC}[92m${s}${reset}`

const pick = <T>(list: readonly T[]): T => list[Math.floor(Math.random() * list.length)]!
const int = (min: number, max: number) => min + Math.floor(Math.random() * (max - min + 1))
const hex = (length: number) =>
  Array.from({ length }, () => '0123456789abcdef'[int(0, 15)]).join('')

/*!
 * The three streams, and what each one sounds like.
 *
 * Every generator returns `content` exactly as the client would have piped it:
 * a string for ordinary output, a plain object for a JSON5 line. The `type`
 * field the server derives from it is computed in `emitLine` below.
 */

const PATHS = [
  '/1/config',
  '/1/geocode?address=ny',
  '/1/me/review_status/seen',
  '/1/users/me/profile',
  '/1/checkout/session',
  '/1/search?q=espresso',
  '/health'
] as const

const METHODS = ['GET', 'GET', 'GET', 'POST', 'PUT', 'DELETE'] as const

function apiLine(): string | object {
  if (Math.random() < 0.12) {
    return {
      event: 'checkout.completed',
      order: `ord_${hex(12)}`,
      amount: int(9, 480) + 0.99,
      currency: 'EUR',
      region: pick(['eu-west-1', 'us-east-1', 'ap-south-1']),
      items: int(1, 4),
      ok: true
    }
  }

  const method = pick(METHODS)
  const path = pick(PATHS)
  const ms = int(2, 340)
  const roll = Math.random()

  const status =
    roll < 0.78 ? green('200') : roll < 0.88 ? yellow('301') : roll < 0.95 ? yellow('404') : red('500')

  const tail = roll >= 0.95 ? ' ' + brightRed('connection reset by peer') : ''

  return `${dim('api')} ${status} ${method} ${path} ${dim(`${ms}ms`)}${tail}`
}

function workerLine(): string | object {
  const roll = Math.random()

  if (roll < 0.22) {
    return {
      job: pick(['thumbnail.generate', 'email.digest', 'index.rebuild']),
      id: `job_${hex(10)}`,
      attempt: int(1, 2),
      duration_ms: int(40, 2600),
      queue: pick(['default', 'low', 'critical']),
      ok: Math.random() > 0.15
    }
  }

  if (roll < 0.4) {
    return `${magenta('DEBUG')} pool=${yellow(String(int(1, 32)))}/32 idle=${brightGreen(
      String(int(0, 24))
    )} waiting=${int(0, 6)}`
  }

  if (roll < 0.52) {
    return `${red('ERROR')} job ${dim(`job_${hex(10)}`)} failed ${brightRed(
      pick(['ETIMEDOUT', 'ECONNREFUSED', 'OOMKilled'])
    )} — retrying in ${int(2, 30)}s`
  }

  return `${blue('INFO')}  ${pick([
    `processed ${int(1, 40)} jobs in ${cyan(`${int(80, 900)}ms`)}`,
    `cache warmed in ${cyan(`${int(20, 300)}ms`)}`,
    `lease renewed for ${dim(`worker-01`)}`,
    `flushed ${int(10, 900)} metrics`
  ])}`
}

const BUILD_STEPS = [
  'resolving dependencies',
  'type-checking app/src',
  'bundling app/src/main.tsx',
  'compiling app/scss/main.scss',
  'writing dist/',
  'pushing ghcr.io/kilianc/rtail'
] as const

function deployLine(): string | object {
  const roll = Math.random()

  if (roll < 0.1) {
    return {
      deploy: `dpl_${hex(8)}`,
      commit: hex(7),
      branch: 'main',
      status: 'ready',
      duration_s: int(38, 190)
    }
  }

  if (roll < 0.22) {
    return `${dim('deploy')} ${brightGreen('✔')} ${pick([
      'build succeeded',
      'image pushed',
      'health check passed'
    ])} in ${cyan(`${int(1, 3)}m ${int(1, 59)}s`)}`
  }

  if (roll < 0.3) {
    return `${yellow('WARN')}  ${pick([
      'bundle grew by 1.4 KB',
      'no cache hit for layer 3',
      'deprecated flag --web-version'
    ])}`
  }

  return `${dim('step')} ${pick(BUILD_STEPS)} ${dim('…')}`
}

const GENERATORS: Record<string, () => string | object> = {
  'api.myproject.com': apiLine,
  'worker-01': workerLine,
  deploy: deployLine
}

/** Stream ids, in sidebar order. */
export const STREAM_IDS = Object.keys(GENERATORS)

/** The stream the preview opens on. */
export const DEFAULT_STREAM = 'api.myproject.com'

const HOSTS: Record<string, string> = {
  'api.myproject.com': '10.0.1.24',
  'worker-01': '10.0.1.31',
  deploy: '10.0.0.7'
}

/*!
 * A minimal socket surface — exactly the four events app/src/app.tsx listens
 * for, and the one it emits. Typed as an interface rather than `any` so the
 * app keeps type-checking against this module when the build swaps it in.
 */

interface DemoSocket {
  on(event: 'connect', handler: () => void): void
  on(event: 'streams', handler: (streams: string[]) => void): void
  on(event: 'backlog', handler: (lines: WireLine[] | null) => void): void
  on(event: 'line', handler: (line: WireLine) => void): void
  emit(event: 'select stream', stream: string | null): void
  close(): void
}

export type Connection = DemoSocket

function makeLine(streamid: string, timestamp: number): WireLine {
  const content = GENERATORS[streamid]!()

  return {
    timestamp,
    streamid,
    host: HOSTS[streamid] ?? '127.0.0.1',
    port: int(40000, 60000),
    content,
    type: typeof content
  }
}

/**
 * Opens the fake connection.
 *
 * Backlogs are seeded before the first paint so that selecting a stream shows
 * history immediately rather than an empty pane that fills one line at a time.
 */
export function connect(): Connection {
  const backlogs = new Map<string, WireLine[]>()
  const handlers = new Map<string, (payload: never) => void>()

  const now = Date.now()

  for (const id of STREAM_IDS) {
    const seed: WireLine[] = []
    const count = int(24, 60)

    for (let i = count; i > 0; i--) {
      seed.push(makeLine(id, now - i * int(400, 1600)))
    }

    backlogs.set(id, seed)
  }

  let selected: string | null = null
  let closed = false

  const emitTo = <T>(event: string, payload: T) => {
    const handler = handlers.get(event) as ((payload: T) => void) | undefined
    handler?.(payload)
  }

  const timer = setInterval(() => {
    // A background tab should not accumulate work it will render all at once.
    if (closed || document.hidden) return

    const streamid =
      null !== selected && Math.random() < SELECTED_SHARE ? selected : pick(STREAM_IDS)

    const line = makeLine(streamid, Date.now())
    const backlog = backlogs.get(streamid)!

    if (backlog.length >= BACKLOG_SIZE) backlog.shift()
    backlog.push(line)

    if (streamid === selected) emitTo('line', line)
  }, TICK)

  // The app registers its handlers synchronously after connect() returns, so
  // the opening events wait for the current task to finish — the same ordering
  // a real socket gives you for free.
  setTimeout(() => {
    if (closed) return
    emitTo('connect', undefined)
    emitTo('streams', STREAM_IDS)
  }, 0)

  return {
    on(event: string, handler: (payload: never) => void) {
      handlers.set(event, handler)
    },

    emit(_event: 'select stream', stream: string | null) {
      selected = stream

      if (null === stream) return

      // Copy: the app keeps the array it is handed, and this one keeps filling.
      emitTo('backlog', [...(backlogs.get(stream) ?? [])])
    },

    close() {
      closed = true
      clearInterval(timer)
      handlers.clear()
    }
  } as DemoSocket
}
