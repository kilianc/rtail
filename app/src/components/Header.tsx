import { useRef, useState } from 'preact/hooks'
import { FONT_FAMILY_COUNT, FONT_SIZE_MAX, FONT_SIZE_MIN } from '../lib/prefs.js'
import type { Prefs, Theme } from '../lib/types.js'
import { Popover } from './Popover.js'

declare const __VERSION__: string

interface Props {
  prefs: Prefs
  onChange: (patch: Partial<Prefs>) => void
}

type OpenPanel = 'info' | 'settings' | null

export function Header({ prefs, onChange }: Props) {
  const [open, setOpen] = useState<OpenPanel>(null)
  const infoRef = useRef<HTMLButtonElement>(null)
  const settingsRef = useRef<HTMLButtonElement>(null)

  const toggle = (panel: Exclude<OpenPanel, null>) =>
    setOpen((current) => (current === panel ? null : panel))

  return (
    <header>
      <div class="rtail-logo" />

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
              onClick={() => onChange({ fontSize: Math.max(FONT_SIZE_MIN, prefs.fontSize - 1) })}
            />
            <button
              class={`btn btn-font-reset ${4 === prefs.fontSize ? 'selected' : ''}`}
              aria-label="Reset font size"
              onClick={() => onChange({ fontSize: 4 })}
            />
            <button
              class="btn btn-font-bigger"
              aria-label="Increase font size"
              disabled={prefs.fontSize >= FONT_SIZE_MAX}
              onClick={() => onChange({ fontSize: Math.min(FONT_SIZE_MAX, prefs.fontSize + 1) })}
            />
          </div>

          <h4>Font family</h4>
          <div class="btn-group six-grid">
            {Array.from({ length: FONT_FAMILY_COUNT }, (_, index) => index + 1).map((n) => (
              <button
                key={n}
                class={`btn btn-font-${n} font-family-${n} ${n === prefs.fontFamily ? 'selected' : ''}`}
                aria-label={`Font family ${n}`}
                onClick={() => onChange({ fontFamily: n })}
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
              onClick={() => onChange({ ascending: true })}
            />
            <button
              class={`btn btn-sorting-desc ${prefs.ascending ? '' : 'selected'}`}
              aria-label="Newest first"
              onClick={() => onChange({ ascending: false })}
            />
          </div>

          <h4>Theme</h4>
          <div class="btn-group">
            {(['dark', 'light'] as Theme[]).map((theme) => (
              <button
                key={theme}
                class={`btn btn-theme-${theme} ${theme === prefs.theme ? 'selected' : ''}`}
                aria-label={`${theme} theme`}
                onClick={() => onChange({ theme })}
              />
            ))}
          </div>
        </Popover>
      )}
    </header>
  )
}
