import { useRef, useState } from 'preact/hooks'
import type { FieldPath } from '../lib/json.ts'
import { FONT_FAMILY_COUNT, FONT_SIZE_MAX, FONT_SIZE_MIN } from '../lib/prefs.ts'
import type { JsonView, Prefs, Theme } from '../lib/types.ts'
import { Popover } from './Popover.tsx'
import { FieldsPicker, SearchBar, SearchHelp, type SearchPanel } from './SearchBar.tsx'

declare const __VERSION__: string

interface Props {
  prefs: Prefs
  activeStream: string | null
  isFavorite: boolean
  paused: boolean
  filter: string
  /** Set when the query did not fully parse. */
  filterError: string | null
  matched: number
  total: number
  /** Paths extracted from this stream's object lines. */
  fields: string[]
  /** Paths seen in the buffer, most common first. */
  availableFields: FieldPath[]
  onChangePrefs: (patch: Partial<Prefs>) => void
  onToggleFavorite: () => void
  onFilter: (pattern: string) => void
  onFields: (fields: string[]) => void
}

type OpenPanel = 'info' | 'settings' | SearchPanel | null

const JSON_VIEWS: Array<[JsonView, string, string]> = [
  ['auto', 'Auto', 'Expand small payloads, collapse the rest'],
  ['collapsed', 'One line', 'Every payload on a single line'],
  ['expanded', 'Pretty', 'Every payload expanded']
]

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
  filterError,
  matched,
  total,
  fields,
  availableFields,
  onChangePrefs,
  onToggleFavorite,
  onFilter,
  onFields
}: Props) {
  const [open, setOpen] = useState<OpenPanel>(null)
  const infoRef = useRef<HTMLButtonElement>(null)
  const settingsRef = useRef<HTMLButtonElement>(null)
  const helpRef = useRef<HTMLButtonElement>(null)
  const fieldsRef = useRef<HTMLButtonElement>(null)

  const toggle = (panel: Exclude<OpenPanel, null>) =>
    setOpen((current) => (current === panel ? null : panel))

  const close = () => setOpen(null)

  return (
    // The popovers are siblings of the bar, not children: bar-scoped styles
    // cannot reach into them, and — since .topbar-main is a query container,
    // which makes it the containing block for fixed descendants — a panel
    // mounted inside would anchor to the bar's box rather than the viewport.
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

            <SearchBar
              value={filter}
              error={filterError}
              matched={matched}
              total={total}
              fieldCount={fields.length}
              open={'help' === open || 'fields' === open ? open : null}
              helpRef={helpRef}
              fieldsRef={fieldsRef}
              onChange={onFilter}
              onToggle={toggle}
            />
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

    {'help' === open && <SearchHelp anchor={helpRef.current} onClose={close} />}

    {'fields' === open && (
        <FieldsPicker
          anchor={fieldsRef.current}
          fields={fields}
          available={availableFields}
          onFields={onFields}
          onClose={close}
        />
      )}

      {'info' === open && (
        <Popover anchor={infoRef.current} class="popover-info" onClose={close}>
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
        </Popover>
      )}

      {'settings' === open && (
        <Popover anchor={settingsRef.current} class="popover-settings" onClose={close}>
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

          <h4>JSON payloads</h4>
          <div class="btn-group">
            {JSON_VIEWS.map(([view, label, title]) => (
              <button
                key={view}
                class={`btn btn-text ${view === prefs.jsonView ? 'selected' : ''}`}
                title={title}
                onClick={() => onChangePrefs({ jsonView: view })}
              >
                {label}
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
