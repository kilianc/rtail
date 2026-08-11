import { useRef, useState } from 'preact/hooks'
import { FONT_FAMILY_COUNT, FONT_SIZE_MAX, FONT_SIZE_MIN } from '../lib/prefs.js'
import type { Prefs, Theme } from '../lib/types.js'
import { Popover } from './Popover.js'

declare const __VERSION__: string

interface Props {
  prefs: Prefs
  activeStream: string | null
  isFavorite: boolean
  paused: boolean
  filter: string
  onChangePrefs: (patch: Partial<Prefs>) => void
  onToggleFavorite: () => void
  onFilter: (pattern: string) => void
}

type OpenPanel = 'info' | 'settings' | null

/**
 * The single top bar.
 *
 * The brand cell is exactly as wide as the sidebar, so the vertical hairline
 * runs unbroken from the top of the window to the bottom. Both widths come
 * from --sidebar-w, which App keeps in sync while the sidebar is dragged.
 */
export function TopBar({
  prefs,
  activeStream,
  isFavorite,
  paused,
  filter,
  onChangePrefs,
  onToggleFavorite,
  onFilter
}: Props) {
  const [open, setOpen] = useState<OpenPanel>(null)
  const infoRef = useRef<HTMLButtonElement>(null)
  const settingsRef = useRef<HTMLButtonElement>(null)

  const toggle = (panel: Exclude<OpenPanel, null>) =>
    setOpen((current) => (current === panel ? null : panel))

  return (
    // The popovers are siblings of the bar, not children, so bar-scoped styles
    // cannot reach into them.
    <>
    <div class="topbar">
      <div class="topbar-brand">
        <div class="rtail-logo" />
      </div>

      <div class="topbar-main">
        {activeStream && (
          <>
            <button
              class="stream-title"
              title={isFavorite ? 'Remove from favorites' : 'Add to favorites'}
              onClick={onToggleFavorite}
            >
              <i class={`stream-title-favorite ${isFavorite ? 'on' : ''}`} />
              {activeStream}
            </button>

            <span class={`stream-status ${paused ? 'paused' : ''}`}>
              <i />
              {paused ? 'Paused' : 'Live'}
            </span>

            <div class="filter-box">
              <input
                type="text"
                placeholder="filter stream (regexp allowed)"
                aria-label="Filter stream"
                value={filter}
                onInput={(event) => onFilter(event.currentTarget.value)}
              />
            </div>
          </>
        )}

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
          <a
            class="btn btn-issue"
            href="https://github.com/kilianc/rtail/issues"
            target="_blank"
            rel="noreferrer"
          >
            Report issue
          </a>
          <a
            class="btn btn-fork"
            href="https://github.com/kilianc/rtail/fork"
            target="_blank"
            rel="noreferrer"
          >
            Fork it
          </a>
          <div class="lukibear-logo" />
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

          <h4>Sorting</h4>
          <div class="btn-group">
            <button
              class={`btn btn-sorting-asc ${prefs.ascending ? 'selected' : ''}`}
              aria-label="Oldest first"
              onClick={() => onChangePrefs({ ascending: true })}
            />
            <button
              class={`btn btn-sorting-desc ${prefs.ascending ? '' : 'selected'}`}
              aria-label="Newest first"
              onClick={() => onChangePrefs({ ascending: false })}
            />
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
