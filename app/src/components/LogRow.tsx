/*!
 * One log row, collapsed and expanded.
 *
 * The expanded state is the best thing Stackdriver has and the reason most
 * queries never get typed: every leaf carries "filter to this" and "exclude
 * this", so you refine by pointing at what you can already see. The command
 * bar updates to match, which keeps the two representations honest — whatever
 * you built by clicking is something you could have typed.
 */

import { formatClock } from '../lib/format.js'
import { Button } from './Button.js'
import { columnValue, highlight } from '../lib/query.js'
import type { Line } from '../lib/types.js'

/**
 * Keys the normalizer lifts into the envelope, and which are therefore already
 * rendered above the promoted fields.
 */
const LIFTED = new Set([
  'level', 'severity', 'severity_text', 'severitytext', 'lvl', 'loglevel', 'log_level', '@l',
  'message', 'msg', 'text', 'short_message', '@m',
  'timestamp', 'ts', 'time', '@timestamp', 'eventtime', 'datetime'
])

export interface FilterAction {
  field: string
  op: string
  value: string
  negated: boolean
}

interface Props {
  line: Line
  expanded: boolean
  selected: boolean
  /** Fields the current query already mentions, shown as active. */
  active: string[]
  /** Substrings the query is looking for, marked in the message. */
  needles: string[]
  /** Fields promoted into columns of their own. */
  columns: string[]
  /** Of those, the ones holding numbers, which align to the right. */
  numeric: string[]
  onToggle: () => void
  onFilter: (action: FilterAction) => void
  onContext: () => void
}

/*!
 * Severity, abbreviated to three characters.
 *
 * This was a round badge carrying `i` / `!` / `!!` before, on the reasoning
 * that a shape survives being read by someone who cannot separate the reds
 * from the greys. The reasoning was right and the glyph was the wrong answer
 * to it: `!` meant both ERROR and WARN, so the two levels people most need to
 * tell apart were the two the shape could not distinguish.
 *
 * Three letters carry the distinction in the text itself, so the colour is
 * confirmation rather than the only signal — and in a monospace column they
 * are exactly as scannable as a badge, for less ink.
 */
const ABBREV: Record<string, string> = {
  EMERGENCY: 'EMG', ALERT: 'ALR', CRITICAL: 'CRT', FATAL: 'FTL',
  ERROR: 'ERR', WARN: 'WRN',
  NOTICE: 'NTC', INFO: 'INF', DEBUG: 'DBG', TRACE: 'TRC'
}

/*!
 * Digits grouped in threes with underscores.
 *
 * `918_441_203` rather than `918,441,203`, because a sequence number is an
 * identifier you compare, not a quantity you add up, and the comma form
 * invites the second reading.
 */
function groupDigits(value: number): string {
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, '_')
}

export function LogRow({ line, expanded, selected, active, needles, columns, numeric, onToggle, onFilter, onContext }: Props) {
  const level = (line.level ?? '').toUpperCase()
  const clock = formatClock(line.timestamp)

  const promoted = Object.entries(line.fields ?? {})
    .filter(([key]) => !LIFTED.has(key))
    .sort(([a], [b]) => a.localeCompare(b))

  return (
    <div
      class={`row ${expanded ? 'expanded' : ''} ${selected ? 'selected' : ''} level-${level.toLowerCase() || 'none'}`}
      onClick={onToggle}
    >
      <div class="row-head">
        {/*
          The disclosure caret is drawn rather than iconised — two rules and a
          rotation, so it inherits colour and stays crisp at any zoom.
        */}
        <i class="row-caret" aria-hidden="true" />

        <time class="row-time" dateTime={new Date(line.timestamp).toISOString()}>
          {clock.date && <span class="row-date">{clock.date}</span>}
          {clock.time}
          <span class="row-ms">.{clock.ms}</span>
        </time>

        <span class="row-level" title={level || undefined}>
          <i class="row-chip" aria-hidden="true">{ABBREV[level] ?? (level ? level.slice(0, 3) : '—')}</i>
          <span class="sr-only">{level || 'no severity'}</span>
        </span>

        {/*
          Promoted columns sit between the timestamp and the summary, in the
          same grid tracks the header declares. A record without the field
          gets an em dash rather than an empty cell: in a store with no fixed
          schema, "this line does not have that key" is a fact worth seeing,
          and a blank reads as a value that failed to load. The dash is drawn
          a shade below the values so a column of them stays quiet.
        */}
        {columns.map((field) => {
          const value = columnValue(line, field)

          return (
            <span
              key={field}
              class={`row-cell ${numeric.includes(field) ? 'numeric' : ''} ${value ? '' : 'empty'}`}
              title={value || `no ${field}`}
            >
              {value || '—'}
            </span>
          )
        })}

        <span class="row-message" dangerouslySetInnerHTML={{ __html: highlight(line.html, needles) }} />
      </div>

      {expanded && (
        <div class="row-detail" onClick={(event) => event.stopPropagation()}>
          {/*
            Two columns, envelope on the left and promoted keys on the right,
            because they are two different kinds of fact and were reading as
            one undifferentiated list. The envelope is fixed and present on
            every record; the promoted keys are whatever this line happened to
            carry, which is the thing worth scanning. Side by side, the shape
            of the record is legible at a glance — and on a narrow pane the
            two stack rather than squeezing.
          */}
          <div class="row-detail-grid">
            <dl class="row-fields">
              <h5 class="row-fields-head">Envelope</h5>

              <Leaf
                label="ts"
                value={new Date(line.timestamp).toISOString()}
                field="ts"
                active={active}
                onFilter={onFilter}
                filterable={false}
              />

              <Leaf label="stream" value={line.streamid} field="stream" active={active} onFilter={onFilter} />

              {line.host && <Leaf label="host" value={line.host} field="host" active={active} onFilter={onFilter} />}

              {/*
                The pagination key, grouped with underscores rather than
                commas: it is an identifier, not a quantity, and a comma in a
                monospace column reads as something you could sum.
              */}
              <Leaf
                label="seq"
                value={groupDigits(line.seq)}
                field="seq"
                active={active}
                onFilter={onFilter}
                filterable={false}
              />

              <Leaf
                label="is_json"
                value={'object' === line.type}
                field="is_json"
                active={active}
                onFilter={onFilter}
                filterable={false}
              />
            </dl>

            {/*
              Promoted keys the envelope already carries are skipped. A payload
              with its own `level` would otherwise list it twice — once
              normalized to ERROR and once as whatever it literally said — which
              reads as a bug in the data rather than as two views of one value.
              The envelope's is the one a query resolves to, so it is the one
              shown.
            */}
            <dl class="row-fields">
              <h5 class="row-fields-head">Promoted columns</h5>

              {promoted.length > 0 ? (
                promoted.map(([key, value]) => (
                  <Leaf
                    key={key}
                    label={key}
                    field={key}
                    value={value}
                    active={active}
                    onFilter={onFilter}
                  />
                ))
              ) : (
                // A plain-text line has none, and saying so beats an empty
                // column that reads as something failing to load.
                <p class="row-fields-empty">none — this line is not JSON</p>
              )}
            </dl>
          </div>

          <div class="row-actions">
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                navigator.clipboard?.writeText(
                  'object' === line.type ? JSON.stringify(line.content, null, 2) : line.text
                )
              }
            >
              copy as JSON
            </Button>
            <Button variant="outline" size="sm" onClick={onContext}>
              surrounding 50 lines
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

interface LeafProps {
  label: string
  field: string
  value: unknown
  active: string[]
  filterable?: boolean
  onFilter: (action: FilterAction) => void
}

function Leaf({ label, field, value, active, filterable = true, onFilter }: LeafProps) {
  const nested = null !== value && 'object' === typeof value

  return (
    <div class={`leaf ${active.includes(field) ? 'active' : ''}`}>
      <dt class="leaf-key">{label}</dt>

      <dd class="leaf-value">
        {nested ? (
          <pre class="leaf-json">{JSON.stringify(value, null, 2)}</pre>
        ) : (
          <span class={`leaf-scalar type-${typeof value}`}>{String(value)}</span>
        )}

        {/*
          Nested values get no filter buttons: the path a person means by
          clicking `req` is ambiguous, and offering a filter that quietly
          matches on serialized JSON text would be worse than offering none.
        */}
        {filterable && !nested && (
          <span class="leaf-actions">
            <Button
              size="sm"
              class="leaf-filter"
              title={`Filter to ${field}=${String(value)}`}
              aria-label={`Filter to ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: String(value), negated: false })}
            >
              ⊕
            </Button>
            <Button
              size="sm"
              class="leaf-filter"
              title={`Exclude ${field}=${String(value)}`}
              aria-label={`Exclude ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: String(value), negated: true })}
            >
              ⊖
            </Button>
          </span>
        )}
      </dd>
    </div>
  )
}
