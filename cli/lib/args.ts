/*!
 * args.ts — the command-line surface of both bins.
 *
 * Separated from the bin scripts so a test can parse an argv array without
 * spawning a process. Each parser maps yargs' loosely-typed output onto the
 * options interface its module actually takes; the dashed keys are read
 * rather than the camelCase aliases because those are the ones yargs is
 * documented to set.
 */

import yargs from 'yargs'
import { choose as moniker } from './moniker.ts'
import { pkg } from './pkg.ts'
import type { ClientOptions } from './client.ts'
import type { ServerOptions } from './server.ts'

export type ClientArgs = Pick<
  ClientOptions,
  'host' | 'port' | 'id' | 'mute' | 'tty' | 'parseDate'
>

export function parseClientArgv(argv: string[], defaultId: string = moniker()): ClientArgs {
  const parsed = yargs(argv)
    .usage('Usage: cmd | rtail [OPTIONS]')
    .example('server | rtail > server.log', 'localhost + file')
    .example('server | rtail --id api.domain.com', 'Name the log stream')
    .example('server | rtail --host example.com', 'Sends to example.com')
    .example('server | rtail --port 43567', 'Uses custom port')
    .example('server | rtail --mute', 'No stdout')
    .example('server | rtail --no-tty', 'Strips ansi colors')
    .example('server | rtail --no-parse-date', 'Disable date parsing/stripping')
    .option('host', {
      alias: 'h',
      type: 'string',
      default: '127.0.0.1',
      describe: 'The server host'
    })
    .option('port', {
      alias: 'p',
      type: 'number',
      default: 9999,
      describe: 'The server port'
    })
    .option('id', {
      alias: 'name',
      type: 'string',
      default: defaultId,
      defaultDescription: 'a random name',
      describe: 'The log stream id'
    })
    .option('mute', {
      alias: 'm',
      type: 'boolean',
      default: false,
      describe: 'Don\'t pipe stdin with stdout'
    })
    .option('tty', {
      type: 'boolean',
      default: true,
      describe: 'Keeps ansi colors'
    })
    .option('parse-date', {
      type: 'boolean',
      default: true,
      describe: 'Looks for dates to use as timestamp'
    })
    .help('help')
    .version(pkg.version)
    .alias('version', 'v')
    .strict()
    .parseSync()

  return {
    host: parsed.host,
    port: parsed.port,
    id: parsed.id,
    mute: parsed.mute,
    tty: parsed.tty,
    parseDate: parsed['parse-date'] as boolean
  }
}

/** `--backlog` has to be a positive integer; 0 would keep no history at all. */
export function isValidBacklog(value: unknown): boolean {
  return Number.isInteger(value) && (value as number) >= 1
}

export function parseServerArgv(argv: string[]): ServerOptions {
  const parsed = yargs(argv)
    .usage('Usage: rtail-server [OPTIONS]')
    .example('rtail-server --web-port 8080', 'Use custom HTTP port')
    .example('rtail-server --udp-port 8080', 'Use custom UDP port')
    .example('rtail-server --web-version stable', 'Always uses latest stable webapp')
    .example('rtail-server --web-version unstable', 'Always uses latest develop webapp')
    .example('rtail-server --web-version 0.1.3', 'Use webapp v0.1.3')
    .option('udp-host', {
      alias: 'uh',
      type: 'string',
      default: '127.0.0.1',
      describe: 'The listening UDP hostname'
    })
    .option('udp-port', {
      alias: 'up',
      type: 'number',
      default: 9999,
      describe: 'The listening UDP port'
    })
    .option('web-host', {
      alias: 'wh',
      type: 'string',
      default: '127.0.0.1',
      describe: 'The listening HTTP hostname'
    })
    .option('web-port', {
      alias: 'wp',
      type: 'number',
      default: 8888,
      describe: 'The listening HTTP port'
    })
    .option('web-version', {
      type: 'string',
      describe: 'Define web app version to serve'
    })
    .option('backlog', {
      alias: 'b',
      type: 'number',
      default: 100,
      describe: 'Lines of history kept in memory, per stream'
    })
    .check((args) => {
      if (!isValidBacklog(args.backlog)) {
        throw new Error('--backlog must be a positive integer')
      }
      return true
    })
    // Every option is also settable as RTAIL_*, e.g. RTAIL_WEB_HOST. The
    // container image sets the listen hosts this way so that `docker run rtail`
    // with extra flags appends to the command instead of replacing it.
    .env('RTAIL')
    .help('help')
    .alias('help', 'h')
    .version(pkg.version)
    .alias('version', 'v')
    .strict()
    .parseSync()

  return {
    udpHost: parsed['udp-host'] as string,
    udpPort: parsed['udp-port'] as number,
    webHost: parsed['web-host'] as string,
    webPort: parsed['web-port'] as number,
    webVersion: parsed['web-version'] as string | undefined,
    backlog: parsed.backlog
  }
}
