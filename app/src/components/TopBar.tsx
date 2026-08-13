/*!
 * The top bar.
 *
 * Brand, stream, the live toggle, and what the last query cost. Everything
 * else that used to live here moved: the filter is its own row now because it
 * is the primary control, and streams collapsed into a picker.
 *
 * The cost readout is deliberate. A search interface that hides how much work
 * it just did teaches nobody why one query is instant and another is not.
 */

import type { ComponentChildren } from 'preact'
import { useRef, useState } from 'preact/hooks'
import { FONT_FAMILY_COUNT, FONT_SIZE_MAX, FONT_SIZE_MIN } from '../lib/prefs.js'
import type { Prefs, Theme } from '../lib/types.js'
import { Popover } from './Popover.js'

declare const __VERSION__: string

interface Props {
  prefs: Prefs
  live: boolean
  scanned: string
  children?: ComponentChildren
  onChangePrefs: (patch: Partial<Prefs>) => void
  onToggleLive: () => void
}

type OpenPanel = 'info' | 'settings' | null

export function TopBar({ prefs, live, scanned, children, onChangePrefs, onToggleLive }: Props) {
  const [open, setOpen] = useState<OpenPanel>(null)
  const infoRef = useRef<HTMLButtonElement>(null)
  const settingsRef = useRef<HTMLButtonElement>(null)

  const toggle = (panel: Exclude<OpenPanel, null>) =>
    setOpen((current) => (current === panel ? null : panel))

  return (
    <>
      <div class="topbar">
        <div class="topbar-brand">
          <div class="rtail-logo" />
        </div>

        <div class="topbar-main">
          {children}

          <button
            class={`live-toggle ${live ? 'on' : ''}`}
            title={live ? 'Streaming — click to browse history' : 'Click to stream live'}
            aria-pressed={live}
            onClick={onToggleLive}
          >
            <i />
            {live ? 'Live' : 'History'}
          </button>

          {scanned && !live && <span class="topbar-scanned">{scanned}</span>}

          <div class="topbar-actions">
            <button
              ref={infoRef}
              class="btn btn-info"
              aria-label="About rTail"
              aria-expanded={'info' === open}
              onClick={() => toggle('info')}
            />

            <a
              class="btn btn-github"
              aria-label="rTail on GitHub"
              href="https://github.com/kilianc/rtail"
              target="_blank"
              rel="noreferrer"
            />

            <button
              ref={settingsRef}
              class="btn btn-settings"
              aria-label="Settings"
              aria-expanded={'settings' === open}
              onClick={() => toggle('settings')}
            />
          </div>
        </div>
      </div>

      {'info' === open && (
        <Popover anchor={infoRef.current} class="popover-info" onClose={() => setOpen(null)}>
          <div class="rtail-logo" />
          <div class="version">Version {__VERSION__}</div>

          <div class="shortcuts">
            <h4>Shortcuts</h4>
            <dl>
              <dt>/</dt><dd>focus the filter</dd>
              <dt>⌘K</dt><dd>focus the filter</dd>
              <dt>⏎</dt><dd>run the query</dd>
              <dt>j / k</dt><dd>move between events</dd>
              <dt>⎋</dt><dd>collapse everything</dd>
            </dl>
          </div>

          <a
            class="btn btn-issue"
            href="https://github.com/kilianc/rtail/issues"
            target="_blank"
            rel="noreferrer"
          >
            Report issue
          </a>
        </Popover>
      )}

      {'settings' === open && (
        <Popover anchor={settingsRef.current} class="popover-settings" onClose={() => setOpen(null)}>
          <h4>Font size</h4>
          <div class="btn-group">
            <button
              class="btn btn-font-smaller"
              aria-label="Decrease font size"
              disabled={prefs.fontSize <= FONT_SIZE_MIN}
              onClick={() => onChangePrefs({ fontSize: Math.max(FONT_SIZE_MIN, prefs.fontSize - 1) })}
            />
            <button
              class={`btn btn-font-reset ${4 === prefs.fontSize ? 'selected' : ''}`}
              aria-label="Reset font size"
              onClick={() => onChangePrefs({ fontSize: 4 })}
            />
            <button
              class="btn btn-font-bigger"
              aria-label="Increase font size"
              disabled={prefs.fontSize >= FONT_SIZE_MAX}
              onClick={() => onChangePrefs({ fontSize: Math.min(FONT_SIZE_MAX, prefs.fontSize + 1) })}
            />
          </div>

          <h4>Font family</h4>
          <div class="btn-group six-grid">
            {Array.from({ length: FONT_FAMILY_COUNT }, (_, index) => index + 1).map((n) => (
              <button
                key={n}
                class={`btn btn-font-${n} font-family-${n} ${n === prefs.fontFamily ? 'selected' : ''}`}
                aria-label={`Font family ${n}`}
                onClick={() => onChangePrefs({ fontFamily: n })}
              >
                Ag
              </button>
            ))}
          </div>

          <h4>Theme</h4>
          <div class="btn-group">
            {(['dark', 'light'] as Theme[]).map((theme) => (
              <button
                key={theme}
                class={`btn btn-theme-${theme} ${theme === prefs.theme ? 'selected' : ''}`}
                aria-label={`${theme} theme`}
                onClick={() => onChangePrefs({ theme })}
              />
            ))}
          </div>
        </Popover>
      )}
    </>
  )
}
