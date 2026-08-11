/*!
 * client.ts — the rtail client, minus the command line.
 *
 * Kept apart from rtail-client.ts so the behaviour can be exercised in-process
 * by the test suite. The bin script is only argv parsing on top of this.
 */

import dgram from 'node:dgram'
import { createInterface } from 'node:readline'
import type { Readable, Writable } from 'node:stream'
import * as chrono from 'chrono-node'
import JSON5 from 'json5'
import stripAnsi from 'strip-ansi'

/** The JSON envelope broadcast over UDP, one per input line. */
export interface WirePayload {
  id: string
  timestamp: number
  content: unknown
}

export interface ParseOptions {
  id: string
  /** Look for a leading date to use as the timestamp, and strip it. */
  parseDate: boolean
  /** Injectable clock, so tests are not at the mercy of the wall time. */
  now?: () => number
}

// Characters that would otherwise be meaningful inside the RegExp built from
// the matched date text.
const RE_SPECIAL = /[-[\]{}()*+?.,\\^$|#\s]/g

/**
 * Turns one raw line into the payload the server expects.
 *
 * A line is either structured (JSON/JSON5, kept as an object) or free text. In
 * the free-text case a leading date is promoted to the message timestamp and
 * removed from the body, so the gutter shows it instead of the line repeating
 * it.
 */
export function parseLine(raw: string, opts: ParseOptions): WirePayload {
  const now = opts.now ?? Date.now
  let content: unknown = raw
  let timestamp: chrono.ParsedResult | null = null

  try {
    content = JSON5.parse(raw)
  } catch {
    // Not structured, so it is worth looking for a date.
    timestamp = opts.parseDate ? chrono.parse(raw)[0] ?? null : null
  }

  if (timestamp) {
    const text = timestamp.text.replace(RE_SPECIAL, '\\$&')
    content = raw.replace(new RegExp(' *[^ ]?' + text + '[^ ]? *'), '')
  }

  return {
    id: opts.id,
    timestamp: timestamp ? timestamp.start.date().getTime() : now(),
    content
  }
}

export interface ClientOptions extends ParseOptions {
  host: string
  port: number
  /** Don't echo stdin to stdout. */
  mute: boolean
  /** Keep ANSI colours. Ignored when the output is not a terminal. */
  tty: boolean
  input?: Readable
  output?: Writable
  /** Overrides the `output.isTTY` sniff; tests pipe, so nothing is ever a TTY. */
  isTTY?: boolean
  socket?: dgram.Socket
}

export interface Client {
  /** Resolves once stdin has ended and every datagram has been flushed. */
  finished: Promise<void>
}

/**
 * Reads `input` line by line, echoing to `output` and broadcasting each line.
 *
 * The original implementation piped stdin twice — once to stdout and once
 * through a line splitter — using `split` and `through2-map`. One pass over
 * readline does both and drops two dependencies.
 */
export function createClient(opts: ClientOptions): Client {
  const input = opts.input ?? process.stdin
  const output = opts.output ?? process.stdout
  const socket = opts.socket ?? dgram.createSocket('udp4')

  // Colours are kept only when the sink is a real terminal and --tty is on.
  const isTTY = opts.isTTY ?? Boolean((output as NodeJS.WriteStream).isTTY)
  const stripColors = !isTTY || !opts.tty

  let isClosed = false
  let isSending = 0

  socket.bind(() => socket.setBroadcast(true))

  const lines = createInterface({ input, crlfDelay: Infinity })

  const finished = new Promise<void>((resolve) => {
    const closeIfDrained = () => {
      if (!isClosed || isSending) return
      socket.close()
      resolve()
    }

    lines.on('line', (raw: string) => {
      if (!opts.mute) {
        output.write((stripColors ? stripAnsi(raw) : raw) + '\n')
      }

      const buffer = Buffer.from(JSON.stringify(parseLine(raw, opts)))

      isSending++

      socket.send(buffer, 0, buffer.length, opts.port, opts.host, () => {
        isSending--
        closeIfDrained()
      })
    })

    // Drain the pipe, then exit.
    lines.on('close', () => {
      isClosed = true
      closeIfDrained()
    })
  })

  return { finished }
}
