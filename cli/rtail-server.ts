#!/usr/bin/env node

/*!
 * rtail-server.ts
 * Created by Kilian Ciuffolo on Oct 26, 2014
 *
 * Ships as TypeScript: Node strips the types on load (>= 22.18), so there is
 * no build step between this file and the one npm installs. Everything below
 * the argv parsing lives in lib/server.ts, where it can be tested in-process.
 */

import updateNotifier from 'update-notifier'
import { hideBin } from 'yargs/helpers'
import { parseServerArgv } from './lib/args.ts'
import { createRtailServer } from './lib/server.ts'
import { pkg } from './lib/pkg.ts'

/*!
 * inform the user of updates
 */
updateNotifier({ pkg }).notify()

createRtailServer(parseServerArgv(hideBin(process.argv)))
