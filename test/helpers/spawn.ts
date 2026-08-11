/*!
 * spawn.ts — running the real bins, as a user's shell would.
 *
 * These cover the one thing an in-process test cannot: that the published
 * .ts entry points are actually executable by a bare `node`, with the types
 * stripped on the fly and no build step anywhere.
 */

import { spawn } from 'node:child_process'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const root = new URL('../../', import.meta.url)

export const CLIENT = fileURLToPath(new URL('cli/rtail-client.ts', root))
export const SERVER = fileURLToPath(new URL('cli/rtail-server.ts', root))

export interface Run {
  code: number | null
  stdout: string
  stderr: string
}

/** Runs the client with `input` on stdin and resolves once it exits. */
export function runClient(args: string[], input: string): Promise<Run> {
  const child = spawn(process.execPath, [CLIENT, ...args], {
    // Without this the notifier can write to stdout and pollute the assertion.
    env: { ...process.env, NO_UPDATE_NOTIFIER: '1' }
  })

  let stdout = ''
  let stderr = ''

  child.stdout.on('data', (chunk) => (stdout += chunk))
  child.stderr.on('data', (chunk) => (stderr += chunk))
  child.stdin.end(input)

  return new Promise((resolve, reject) => {
    child.once('error', reject)
    child.once('close', (code) => resolve({ code, stdout, stderr }))
  })
}

export interface ServerProcess {
  stop(): Promise<void>
}

/** Starts the real server and resolves once its HTTP port answers. */
export async function startServer(args: string[], webPort: number): Promise<ServerProcess> {
  const child = spawn(process.execPath, [SERVER, ...args], {
    stdio: 'ignore',
    env: { ...process.env, NO_UPDATE_NOTIFIER: '1' }
  })

  await waitForHttp(`http://127.0.0.1:${webPort}/`)

  return {
    stop: () =>
      new Promise<void>((resolve) => {
        child.once('close', () => resolve())
        child.kill('SIGTERM')
      })
  }
}

/** Polls a URL until it answers, so tests never race a fixed sleep. */
export async function waitForHttp(url: string, timeoutMs = 20_000): Promise<void> {
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
