#!/usr/bin/env node
'use strict'

/*!
 * The npm entry point: find this machine's binary and become it.
 *
 * rtail is a Go program. npm is one of the ways to get it, because the people
 * who pipe logs into it are often already standing in a project with a
 * package.json — but nothing here implements the client, and this file is the
 * only JavaScript left in the install.
 *
 * ** Why a launcher at all. **
 *
 * The binaries ship as one package per platform (`@rtail/darwin-arm64` and
 * friends), each declaring `os` and `cpu`, listed here as optional
 * dependencies. npm installs the one that matches and silently skips the
 * rest, so a Linux box downloads a Linux binary and nothing else. That is the
 * arrangement esbuild uses, and it is the only one that needs no install-time
 * download: no postinstall script, no curl, nothing that breaks behind a
 * proxy or under `--ignore-scripts`.
 *
 * The cost is this file. A platform package can declare its own `bin`, and
 * for a local install npm would link it — but `npm i -g rtail` links the bins
 * of the named package only, never a transitive dependency's, so a global
 * install would leave no `rtail` on PATH. Hence a launcher in the package the
 * user actually named.
 *
 * Anyone who would rather not have Node in the path at all can take the
 * binary straight from a release tarball, `go install`, or the container.
 */

const { spawnSync } = require('node:child_process')

// Go and Node spell the same architectures differently.
const PLATFORMS = {
  'darwin-arm64': '@rtail/darwin-arm64',
  'darwin-x64': '@rtail/darwin-x64',
  'linux-arm64': '@rtail/linux-arm64',
  'linux-x64': '@rtail/linux-x64',
  'win32-x64': '@rtail/win32-x64'
}

const key = `${process.platform}-${process.arch}`
const pkg = PLATFORMS[key]

if (!pkg) {
  console.error(
    `rtail: no prebuilt binary for ${key}.\n` +
      'Build one with `go install github.com/kilianc/rtail/v2/cmd/rtail@latest`,\n' +
      'or open an issue at https://github.com/kilianc/rtail/issues if this\n' +
      'platform should be published.'
  )
  process.exit(1)
}

const exe = 'win32' === process.platform ? 'rtail.exe' : 'rtail'

let binary

try {
  binary = require.resolve(`${pkg}/bin/${exe}`)
} catch {
  // The optional dependency was skipped — almost always --no-optional, or a
  // lockfile copied from a machine of a different shape.
  console.error(
    `rtail: ${pkg} is not installed.\n` +
      'It is an optional dependency carrying the binary for this platform.\n' +
      `Reinstall without --no-optional, or \`npm install ${pkg}\` directly.`
  )
  process.exit(1)
}

/*!
 * stdio is inherited, which is the whole point.
 *
 * rtail is a pipe. Its stdin is the producer's stdout and its own stdout is
 * whatever came next in the shell, so the file descriptors are handed over
 * untouched rather than copied through this process — which also means no
 * buffering, no mangling of a partial line, and a SIGINT at the terminal
 * reaching the binary directly.
 */
const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' })

if (result.error) {
  console.error(`rtail: ${result.error.message}`)
  process.exit(1)
}

// A child killed by a signal has a null status; 128 + signal is the shell's
// convention for reporting that, and exiting 0 would claim it succeeded.
process.exit(null === result.status ? 1 : result.status)
