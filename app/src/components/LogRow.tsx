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
  onToggle: () => void
  onFilter: (action: FilterAction) => void
  onContext: () => void
}

/*!
 * Severity glyphs, copied off a real Logs Explorer.
 *
 * A round badge with a symbol rather than a lettered square: `i` for the
 * routine levels, `!` once something is wrong and `!!` when it is worse. The
 * shape carries as much as the colour, which is what keeps the column
 * readable at 22px and for anyone who cannot separate the reds from the greys.
 */
const GLYPHS: Record<string, string> = {
  EMERGENCY: '!!', ALERT: '!!', CRITICAL: '!!', FATAL: '!!',
  ERROR: '!', WARN: '!',
  NOTICE: 'i', INFO: 'i', DEBUG: 'i', TRACE: 'i'
}

export function LogRow({ line, expanded, selected, active, needles, columns, onToggle, onFilter, onContext }: Props) {
  const level = (line.level ?? '').toUpperCase()
  const clock = formatClock(line.timestamp)

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

        <span class="row-level" title={level || undefined}>
          <i class="row-chip" aria-hidden="true">{GLYPHS[level] ?? 'i'}</i>
          <span class="sr-only">{level || 'no severity'}</span>
        </span>

        <time class="row-time" dateTime={new Date(line.timestamp).toISOString()}>
          {clock.date && <span class="row-date">{clock.date}</span>}
          {clock.time}
          <span class="row-ms">.{clock.ms}</span>
        </time>

        {/*
          Promoted columns sit between the timestamp and the summary, in the
          same grid tracks the header declares. An empty cell is left empty
          rather than filled with a dash: a column of dashes reads as data, and
          "this record does not have that field" is worth seeing at a glance
          in a store with no fixed schema.
        */}
        {columns.map((field) => (
          <span key={field} class="row-cell" title={columnValue(line, field)}>
            {columnValue(line, field)}
          </span>
        ))}

        <span class="row-message" dangerouslySetInnerHTML={{ __html: highlight(line.html, needles) }} />
      </div>

      {expanded && (
        <div class="row-detail" onClick={(event) => event.stopPropagation()}>
          <dl class="row-fields">
            <Leaf
              label="timestamp"
              value={new Date(line.timestamp).toISOString()}
              field="ts"
              active={active}
              onFilter={onFilter}
              filterable={false}
            />

            <Leaf label="stream" value={line.streamid} field="stream" active={active} onFilter={onFilter} />

            {level && <Leaf label="level" value={level} field="level" active={active} onFilter={onFilter} />}

            {line.host && <Leaf label="host" value={line.host} field="host" active={active} onFilter={onFilter} />}

            {/*
              Promoted keys the envelope already carries are skipped. A payload
              with its own `level` would otherwise list it twice — once
              normalized to ERROR and once as whatever it literally said — which
              reads as a bug in the data rather than as two views of one value.
              The envelope's is the one a query resolves to, so it is the one
              shown.
            */}
            {Object.entries(line.fields ?? {})
              .filter(([key]) => !LIFTED.has(key))
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([key, value]) => (
                <Leaf
                  key={key}
                  label={key}
                  field={key}
                  value={value}
                  active={active}
                  onFilter={onFilter}
                />
              ))}
          </dl>

          <div class="row-actions">
            <button
              class="row-action"
              onClick={() => navigator.clipboard?.writeText(line.text)}
            >
              Copy
            </button>
            <button class="row-action" onClick={onContext}>
              Show context
            </button>
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
            <button
              class="leaf-filter"
              title={`Filter to ${field}=${String(value)}`}
              aria-label={`Filter to ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: String(value), negated: false })}
            >
              ⊕
            </button>
            <button
              class="leaf-filter"
              title={`Exclude ${field}=${String(value)}`}
              aria-label={`Exclude ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: String(value), negated: true })}
            >
              ⊖
            </button>
          </span>
        )}
      </dd>
    </div>
  )
}
