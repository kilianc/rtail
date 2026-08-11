/*!
 * Test helpers.
 */

import { spawn } from 'node:child_process'
import dgram from 'node:dgram'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const root = new URL('../', import.meta.url)

export const CLIENT = fileURLToPath(new URL('cli/rtail-client.js', root))
export const SERVER = fileURLToPath(new URL('cli/rtail-server.js', root))

/** Binds a UDP socket and collects every decoded rtail message it receives. */
export async function listen(port, host = '127.0.0.1') {
  const socket = dgram.createSocket('udp4')
  const messages = []

  socket.on('message', (data) => {
    try {
      messages.push(JSON.parse(data))
    } catch {
      messages.push({ malformed: String(data) })
    }
  })

  await new Promise((resolve, reject) => {
    socket.once('error', reject)
    socket.bind(port, host, resolve)
  })

  return {
    messages,
    close: () => new Promise((resolve) => socket.close(resolve))
  }
}

/**
 * Runs the client with `input` on stdin and resolves once it exits.
 *
 * @returns {Promise<{ code: number, stdout: string }>}
 */
export function runClient(args, input) {
  const child = spawn(process.execPath, [CLIENT, ...args])
  let stdout = ''

  child.stdout.on('data', (chunk) => {
    stdout += chunk
  })

  child.stdin.end(input)

  return new Promise((resolve, reject) => {
    child.once('error', reject)
    child.once('close', (code) => resolve({ code, stdout }))
  })
}

/** Starts the server and resolves once its HTTP port answers. */
export async function startServer(args, { webPort } = {}) {
  const child = spawn(process.execPath, [SERVER, ...args], { stdio: 'ignore' })

  if (webPort) await waitForHttp(`http://127.0.0.1:${webPort}/`)
  else await delay(500)

  return {
    child,
    stop: () =>
      new Promise((resolve) => {
        child.once('close', resolve)
        child.kill('SIGTERM')
      })
  }
}

/** Polls a URL until it answers, so tests never race a fixed sleep. */
export async function waitForHttp(url, timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs

  while (Date.now() < deadline) {
    try {
      await fetch(url)
      return
    } catch {
      await delay(100)
    }
  }

  throw new Error(`timed out waiting for ${url}`)
}

/** Polls `predicate` until it returns true. */
export async function waitFor(predicate, message = 'condition', timeoutMs = 10_000) {
  const deadline = Date.now() + timeoutMs

  while (Date.now() < deadline) {
    if (await predicate()) return
    await delay(50)
  }

  throw new Error(`timed out waiting for ${message}`)
}

export { delay }
