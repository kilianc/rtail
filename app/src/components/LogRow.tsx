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
import { columnValue, highlight, type Scalar } from '../lib/query.js'
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

/*!
 * Envelope names a payload key can carry without having been lifted.
 *
 * A query resolves these to the envelope whatever the payload says — the
 * server's resolver and its live predicate both check the envelope first — so
 * a promoted `host` of `ip-10-0-4-21` filtered as `host=ip-10-0-4-21` was
 * compared with the sender's address and matched nothing, and pinned it showed
 * the sender's address in the column. The key is still shown; it just offers
 * no action that would quietly act on a different value.
 */
const SHADOWED = new Set(['stream', 'host', 'seq', 'ingest_ts', 'raw', 'is_json'])

export interface FilterAction {
  field: string
  op: string
  /** Typed, so a clicked number or boolean is written as one. */
  value: Scalar
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
  /** Pin this field out of the summary and into a column of its own. */
  onPin: (field: string) => void
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

export function LogRow({ line, expanded, selected, active, needles, columns, numeric, onToggle, onFilter, onPin, onContext }: Props) {
  const level = (line.level ?? '').toUpperCase()
  const clock = formatClock(line.timestamp)

  const promoted = Object.entries(line.fields ?? {})
    .filter(([key]) => !LIFTED.has(key))
    .sort(([a], [b]) => a.localeCompare(b))

  return (
    <div
      class={classes('row', expanded && 'expanded', selected && 'selected', `level-${level.toLowerCase() || 'none'}`)}
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
              class={classes('row-cell', numeric.includes(field) && 'numeric', !value && 'empty')}
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
                columns={columns}
                onFilter={onFilter}
                onPin={onPin}
                filterable={false}
                pinnable={false}
              />

              <Leaf label="stream" value={line.streamid} field="stream" active={active} columns={columns} onFilter={onFilter} onPin={onPin} />

              {line.host && <Leaf label="host" value={line.host} field="host" active={active} columns={columns} onFilter={onFilter} onPin={onPin} />}

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
                columns={columns}
                onFilter={onFilter}
                onPin={onPin}
                filterable={false}
              />

              <Leaf
                label="is_json"
                value={'object' === line.type}
                field="is_json"
                active={active}
                columns={columns}
                onFilter={onFilter}
                onPin={onPin}
                filterable={false}
                pinnable={false}
              />

              {/*
                The line as it arrived, when the summary shows something
                else: a syslog frame's priority, version and timestamp are
                cut away to leave its message on the row, and this is where
                the frame is still there to read.
              */}
              {'object' !== line.type && line.msg && line.msg !== line.text && (
                <Leaf
                  label="raw"
                  value={line.text}
                  field="raw"
                  active={active}
                  columns={columns}
                  onFilter={onFilter}
                  onPin={onPin}
                  filterable={false}
                  pinnable={false}
                />
              )}
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
              {/*
                "Promoted", the word the explorer uses for the same thing.
                It said "Promoted columns", which named something else: these
                are the keys this record carries, not the handful pinned as
                columns — and those are already on the row above.
              */}
              <h5 class="row-fields-head">Promoted</h5>

              {promoted.length > 0 ? (
                promoted.map(([key, value]) => (
                  <Leaf
                    key={key}
                    label={key}
                    field={key}
                    value={value}
                    active={active}
                    columns={columns}
                    onFilter={onFilter}
                    onPin={onPin}
                    filterable={!SHADOWED.has(key)}
                    pinnable={!SHADOWED.has(key)}
                    note={
                      SHADOWED.has(key)
                        ? `A query for ${key} reads the envelope's ${key}, not this one, so it cannot be filtered or pinned from here`
                        : undefined
                    }
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
            {/*
              What it does, not a count it never kept to: the window is the
              thirty seconds either side of this line, unfiltered, however
              many lines that turns out to be.
            */}
            <Button variant="outline" size="sm" onClick={onContext}>
              surrounding lines (±30s)
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

/*!
 * A class list without the holes.
 *
 * Template strings left an empty slot for every condition that was false, so
 * a plain row's class attribute read `row   level-none` — three spaces for
 * two states it does not have. Harmless to the browser, and noise to anyone
 * who opens the inspector.
 */
function classes(...parts: (string | false | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

interface LeafProps {
  label: string
  field: string
  value: unknown
  active: string[]
  /** Fields already pinned as columns, so the pin can read as a toggle. */
  columns: string[]
  filterable?: boolean
  /*!
   * Whether this key can become a column.
   *
   * `ts` and `is_json` cannot: the first is already the row's leftmost column
   * and the second is a property of the record rather than a value in it, so
   * neither resolves through `columnValue`. Everything else does, `seq`
   * included, which is why pinnable is not simply filterable under a second
   * name.
   */
  pinnable?: boolean
  /** Why this key offers no actions, when that is not self-evident. */
  note?: string
  onFilter: (action: FilterAction) => void
  onPin: (field: string) => void
}

function Leaf({ label, field, value, active, columns, filterable = true, pinnable = true, note, onFilter, onPin }: LeafProps) {
  const nested = null !== value && 'object' === typeof value
  const pinned = columns.includes(field)

  // The value as the record typed it. A number stays a number and a null
  // stays null, so the filter written is `n=-5` or `x=null` rather than the
  // string either would otherwise become.
  const scalar: Scalar =
    'number' === typeof value || 'boolean' === typeof value || null === value ? value : String(value)

  return (
    <div class={classes('leaf', active.includes(field) && 'active', note && 'shadowed')} title={note}>
      <dt class="leaf-key">{label}</dt>

      <dd class="leaf-value">
        {nested ? (
          <pre class="leaf-json">{JSON.stringify(value)}</pre>
        ) : (
          <span class={`leaf-scalar type-${typeof value}`}>{String(value)}</span>
        )}

        {/*
          Nested values get no filter buttons: the path a person means by
          clicking `req` is ambiguous, and offering a filter that quietly
          matches on serialized JSON text would be worse than offering none.
        */}
        {(pinnable || (filterable && !nested)) && (
          <span class={classes('leaf-actions', pinned && 'kept')}>
            {pinnable && (
              /*
                The same ⊞ the explorer pins with, offered where the value
                actually is. The design drew this as a `pin field as column`
                button under the record, which had no field to act on — the
                record carries a dozen, and the one you mean is the one your
                eye is already on.
              */
              <Button
                size="sm"
                class={classes('leaf-filter', pinned && 'on')}
                title={pinned ? `Unpin the ${field} column` : `Pin ${field} as a column`}
                aria-label={pinned ? `Unpin the ${field} column` : `Pin ${field} as a column`}
                aria-pressed={pinned}
                onClick={() => onPin(field)}
              >
                ⊞
              </Button>
            )}
            {filterable && !nested && (<>
            <Button
              size="sm"
              class="leaf-filter"
              title={`Filter to ${field}=${String(value)}`}
              aria-label={`Filter to ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: scalar, negated: false })}
            >
              ⊕
            </Button>
            <Button
              size="sm"
              class="leaf-filter"
              title={`Exclude ${field}=${String(value)}`}
              aria-label={`Exclude ${field} equals ${String(value)}`}
              onClick={() => onFilter({ field, op: '=', value: scalar, negated: true })}
            >
              ⊖
            </Button>
            </>)}
          </span>
        )}
      </dd>
    </div>
  )
}
