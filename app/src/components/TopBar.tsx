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
import type { Store } from '../lib/api.js'
import { formatBytes, formatCount } from '../lib/format.js'
import type { Prefs, Theme } from '../lib/types.js'
import { Button } from './Button.js'
import { Popover } from './Popover.js'

declare const __VERSION__: string

interface Props {
  prefs: Prefs
  live: boolean
  /** Whether the timeline is showing. */
  timeline: boolean
  /** What is in the store, for the readout. */
  store: Store | null
  children?: ComponentChildren
  onChangePrefs: (patch: Partial<Prefs>) => void
  onToggleLive: () => void
  onToggleTimeline: () => void
}

type OpenPanel = 'info' | 'settings' | null

export function TopBar({ prefs, live, timeline, store, children, onChangePrefs, onToggleLive, onToggleTimeline }: Props) {
  const [open, setOpen] = useState<OpenPanel>(null)
  const infoRef = useRef<HTMLButtonElement>(null)
  const settingsRef = useRef<HTMLButtonElement>(null)

  const toggle = (panel: Exclude<OpenPanel, null>) =>
    setOpen((current) => (current === panel ? null : panel))

  return (
    <>
      <div class="topbar">
        <div class="topbar-brand">
          <span class="topbar-mark">rtail</span>
        </div>

        <div class="topbar-main">
          {children}

          <span class="topbar-rule" aria-hidden="true" />

          <button
            class={`live-toggle ${live ? 'on' : ''}`}
            title={live ? 'Streaming — click to browse history' : 'Click to stream live'}
            aria-pressed={live}
            onClick={onToggleLive}
          >
            <i />
            {live ? 'live' : 'history'}
          </button>

          {/*
            The timeline's switch, beside the live switch, because they are
            the same kind of control: what this window is showing. It used to
            be a disclosure row above the chart, which spent a line of the
            window saying the word "Timeline".
          */}
          <Button variant="outline" size="sm" on={timeline} onClick={onToggleTimeline}>
            timeline
          </Button>

          <span class="topbar-gap" />

          {/*
            How much log there is. It is the only thing on this bar that is
            about the install rather than the question being asked, which is
            why it sits apart from the controls, next to the links out.
          */}
          {store && (
            <span class="topbar-store">
              {formatCount(store.rows)} lines
              <span class="topbar-dot" aria-hidden="true">·</span>
              {formatBytes(store.bytes)}
              <span class="topbar-dot" aria-hidden="true">·</span>
              {store.files.toLocaleString()} {1 === store.files ? 'file' : 'files'}
            </span>
          )}

          <span class="topbar-rule" aria-hidden="true" />

          <a
            class="topbar-docs"
            href="https://github.com/kilianc/rtail#readme"
            target="_blank"
            rel="noreferrer"
          >
            docs
          </a>

          <span class="topbar-rule" aria-hidden="true" />

          <div class="topbar-actions">
            <Button
              elementRef={infoRef}
              variant="icon"
              icon="info"
              aria-label="About rTail"
              aria-expanded={'info' === open}
              onClick={() => toggle('info')}
            />

            {/*
              An anchor, not a button, because it navigates — so it spells the
              classes out. They are the same contract Button emits, and this is
              the only place that has to know it.
            */}
            <a
              class="btn btn-icon btn-md btn-icon-github"
              aria-label="rTail on GitHub"
              href="https://github.com/kilianc/rtail"
              target="_blank"
              rel="noreferrer"
            />

            <Button
              elementRef={settingsRef}
              variant="icon"
              icon="settings"
              aria-label="Settings"
              aria-expanded={'settings' === open}
              onClick={() => toggle('settings')}
            />
          </div>
        </div>
      </div>

      {'info' === open && (
        <Popover anchor={infoRef.current} class="popover-info" onClose={() => setOpen(null)}>
          <div class="popover-brand">
            <span class="topbar-mark">rtail</span>
            <span class="version">{__VERSION__}</span>
          </div>

          <div class="shortcuts">
            <h4>Shortcuts</h4>
            <dl>
              <dt>/</dt><dd>focus the query</dd>
              <dt>⌘K</dt><dd>focus the query</dd>
              <dt>return</dt><dd>run it</dd>
              <dt>j / k</dt><dd>move between events</dd>
              <dt>esc</dt><dd>collapse everything</dd>
            </dl>
          </div>

          <a
            class="btn btn-outline btn-md btn-block"
            href="https://github.com/kilianc/rtail/issues"
            target="_blank"
            rel="noreferrer"
          >
            report an issue
          </a>
        </Popover>
      )}

      {'settings' === open && (
        <Popover anchor={settingsRef.current} class="popover-settings" onClose={() => setOpen(null)}>
          <h4>Size</h4>
          <div class="btn-group">
            <Button
              variant="icon"
              icon="font-smaller"
              aria-label="Decrease font size"
              disabled={prefs.fontSize <= FONT_SIZE_MIN}
              onClick={() => onChangePrefs({ fontSize: Math.max(FONT_SIZE_MIN, prefs.fontSize - 1) })}
            />
            <Button
              variant="icon"
              icon="font-reset"
              on={4 === prefs.fontSize}
              aria-label="Reset font size"
              onClick={() => onChangePrefs({ fontSize: 4 })}
            />
            <Button
              variant="icon"
              icon="font-bigger"
              aria-label="Increase font size"
              disabled={prefs.fontSize >= FONT_SIZE_MAX}
              onClick={() => onChangePrefs({ fontSize: Math.min(FONT_SIZE_MAX, prefs.fontSize + 1) })}
            />
          </div>

          <h4>Face</h4>
          <div class="btn-group six-grid">
            {Array.from({ length: FONT_FAMILY_COUNT }, (_, index) => index + 1).map((n) => (
              <Button
                key={n}
                variant="icon"
                on={n === prefs.fontFamily}
                class={`btn-swatch font-family-${n}`}
                aria-label={`Font family ${n}`}
                onClick={() => onChangePrefs({ fontFamily: n })}
              >
                Ag
              </Button>
            ))}
          </div>

          <h4>Theme</h4>
          <div class="btn-group">
            {(['dark', 'light'] as Theme[]).map((theme) => (
              <Button
                key={theme}
                variant="icon"
                icon={`theme-${theme}`}
                on={theme === prefs.theme}
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
