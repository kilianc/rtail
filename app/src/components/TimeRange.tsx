/*!
 * The time range control.
 *
 * Not decoration: this is the partition pruner. Every search is bounded by it,
 * and the server injects a default when a query arrives without one — so the
 * control has to be visible and always show what is actually being asked for,
 * including after a drag on the histogram has pinned it to something absolute.
 */

import { useRef, useState } from 'preact/hooks'
import { PRESETS, isRelative, label, resolve, type Range } from '../lib/timerange.js'
import { Popover } from './Popover.js'

interface Props {
  range: Range
  onChange: (range: Range) => void
}

export function TimeRange({ range, onChange }: Props) {
  const anchor = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)

  const pick = (next: Range) => {
    onChange(next)
    setOpen(false)
  }

  return (
    <>
      <button
        ref={anchor}
        class={`timerange ${isRelative(range) ? 'relative' : 'absolute'}`}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <i class="timerange-icon" aria-hidden="true" />
        {label(range)}
      </button>

      {open && (
        <Popover anchor={anchor.current} class="popover-timerange" onClose={() => setOpen(false)}>
          <h4>Relative</h4>
          <div class="timerange-presets">
            {PRESETS.map((preset) => (
              <button
                key={preset.short}
                class={preset.range.from === range.from && '' === range.to ? 'selected' : ''}
                onClick={() => pick(preset.range)}
              >
                {preset.label}
              </button>
            ))}
          </div>

          <h4>Absolute</h4>
          <Absolute range={range} onApply={pick} />

          {!isRelative(range) && (
            <button class="timerange-reset" onClick={() => pick(PRESETS[2].range)}>
              Back to live
            </button>
          )}
        </Popover>
      )}
    </>
  )
}

function Absolute({ range, onApply }: { range: Range; onApply: (range: Range) => void }) {
  const start = useRef<HTMLInputElement>(null)
  const end = useRef<HTMLInputElement>(null)

  /*!
   * The inputs are seeded from the *resolved* range, not from its text.
   *
   * A relative range's `from` is an offset like `-1h`, and `new Date('-1h')`
   * is an Invalid Date whose toISOString throws — which took the whole popover
   * down with it, silently, because the throw happened during render.
   */
  const resolved = resolve(range)

  // datetime-local wants a local-time string with no zone; toISOString gives
  // UTC. Converting through the offset is the only way to get the browser to
  // display the instant the user is actually looking at.
  const local = (date: Date) => {
    const offset = date.getTimezoneOffset() * 60_000
    return new Date(date.getTime() - offset).toISOString().slice(0, 16)
  }

  return (
    <div class="timerange-absolute">
      <label>
        From
        <input ref={start} type="datetime-local" defaultValue={local(resolved.from)} />
      </label>

      <label>
        To
        <input ref={end} type="datetime-local" defaultValue={local(resolved.to)} />
      </label>

      <button
        class="timerange-apply"
        onClick={() => {
          const from = start.current?.value
          const to = end.current?.value
          if (!from || !to) return

          onApply({ from: new Date(from).toISOString(), to: new Date(to).toISOString() })
        }}
      >
        Apply
      </button>
    </div>
  )
}
