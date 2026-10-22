/*!
 * The field explorer.
 *
 * A sidebar of the most common values per field, *within the current result
 * set*. It answers "what is even in here" before you know what to search for,
 * which is the question you actually have when you open a service you have
 * never seen — and it is the reason Stackdriver's explorer is usable on
 * somebody else's system.
 *
 * Values load lazily, one query per expanded field, because computing top
 * values for forty fields nobody opened would cost more than the search did.
 */

import { useEffect, useState } from 'preact/hooks'
import { api, type Field, type FieldValue, type Params } from '../lib/api.js'
import { formatCount } from '../lib/format.js'
import { Button } from './Button.js'

interface Props {
  fields: Field[]
  params: Params
  /** Fields the current query mentions. */
  active: string[]
  /** Fields already promoted to columns. */
  columns: string[]
  onFilter: (field: string, value: string, negated: boolean) => void
  onToggleColumn: (field: string) => void
}

/** Fields worth opening first — the ones a person almost always wants. */
const FAVOURED = ['level', 'service', 'stream', 'host', 'status', 'env', 'region']

/*!
 * The envelope keys worth listing.
 *
 * `ts` and `seq` are on every record by construction and have no distribution
 * to explore — a sidebar entry for "every line has a timestamp" is a row of
 * noise — so the section is the four that genuinely vary.
 */
const ENVELOPE = ['stream', 'host', 'level', 'is_json']

/*!
 * Severity order, worst first.
 *
 * The breakdown is sorted by this rather than by count, because the question
 * it answers is "how bad is it" — and sorted by frequency the healthy case
 * puts INFO on top and buries the twelve errors at the bottom, which is the
 * one line anybody opened the panel to see.
 */
const SEVERITY = [
  'EMERGENCY', 'FATAL', 'ALERT', 'CRITICAL', 'ERROR', 'WARN',
  'NOTICE', 'INFO', 'DEBUG', 'TRACE'
]

/** How much of this field is in the current result set, lifetime if unknown. */
const weight = (field: Field) => field.matches ?? field.occurrences ?? 0

export function FieldExplorer({ fields, params, active, columns, onFilter, onToggleColumn }: Props) {
  const [open, setOpen] = useState<string[]>(() =>
    fields.filter((field) => FAVOURED.includes(field.name)).slice(0, 3).map((field) => field.name)
  )

  /*!
   * Deduplicated by name, envelope first.
   *
   * A payload with its own `level` key produces two catalog entries: the
   * envelope column the normalizer lifted it into, and the promoted field.
   * Both are the same data and a query resolves the name to the envelope, so
   * listing both offers a choice that does not exist — and the two would show
   * different value distributions whenever the normalizer canonicalised
   * something, which reads as a bug in the data.
   */
  const unique = new Map<string, Field>()
  for (const field of fields) {
    const existing = unique.get(field.name)
    if (!existing || ('envelope' === field.kind && 'envelope' !== existing.kind)) {
      unique.set(field.name, field)
    }
  }

  const ranked = [...unique.values()]
    .filter((field) => 'envelope' !== field.kind || ENVELOPE.includes(field.name))
    .sort((a, b) => {
      const favour = Number(FAVOURED.includes(b.name)) - Number(FAVOURED.includes(a.name))
      if (favour) return favour
      // By what is in the current result set, where that is known: ranking a
      // sidebar by lifetime volume puts a field with nothing in the window
      // at the top, which is the opposite of helpful.
      return weight(b) - weight(a) || a.name.localeCompare(b.name)
    })

  /*!
   * Two sections, promoted above envelope.
   *
   * They are two different kinds of fact. The envelope is the shape rtail
   * imposes and is the same on every install; the promoted keys are whatever
   * this system happens to log, which is the part worth reading. Labelling
   * them says which is which without a legend.
   */
  const promoted = ranked.filter((field) => 'envelope' !== field.kind)
  const envelope = ranked.filter((field) => 'envelope' === field.kind)

  const group = (field: Field) => (
    <FieldGroup
      key={field.name}
      field={field}
      params={params}
      open={open.includes(field.name)}
      active={active.includes(field.name)}
      column={columns.includes(field.name)}
      onToggleColumn={() => onToggleColumn(field.name)}
      onToggle={() =>
        setOpen((current) =>
          current.includes(field.name)
            ? current.filter((name) => name !== field.name)
            : [...current, field.name]
        )
      }
      onFilter={onFilter}
    />
  )

  return (
    <aside class="explorer">
      <div class="explorer-title">
        <span>fields</span>
        <span class="explorer-gap" />
        <span class="explorer-total">{ranked.length}</span>
      </div>

      <Severities params={params} onFilter={onFilter} />

      <div class="explorer-list">
        {0 === ranked.length && <p class="explorer-empty">No fields yet</p>}

        {promoted.length > 0 && <h4 class="explorer-section">Promoted</h4>}
        {promoted.map(group)}

        {envelope.length > 0 && <h4 class="explorer-section">Envelope</h4>}
        {envelope.map(group)}
      </div>
    </aside>
  )
}

/*!
 * The severity breakdown, pinned above the field list.
 *
 * Level is the one field everybody checks first, and checking it by expanding
 * a row in a list of forty is three interactions for the question the screen
 * opens with. Pinned, with a share bar each, it answers "how bad is it" before
 * anything is clicked — and each row is still a filter.
 */
function Severities({
  params,
  onFilter
}: {
  params: Params
  onFilter: (field: string, value: string, negated: boolean) => void
}) {
  const [values, setValues] = useState<FieldValue[]>([])

  useEffect(() => {
    const controller = new AbortController()

    api
      .fields({ ...params, field: 'level', top: 5 }, controller.signal)
      .then((response) =>
        setValues(
          [...response.values].sort((a, b) => {
            const rank = (value: FieldValue) => {
              const at = SEVERITY.indexOf(value.value.toUpperCase())
              return at < 0 ? SEVERITY.length : at
            }

            return rank(a) - rank(b) || b.count - a.count
          })
        )
      )
      .catch(() => setValues([]))

    return () => controller.abort()
  }, [params.q, params.lang, params.stream, params.from, params.to])

  if (0 === values.length) return null

  return (
    <div class="explorer-levels">
      {values.map((value) => (
        <button
          key={value.value}
          class={`explorer-level level-${value.value.toLowerCase()}`}
          title={`Filter to level=${value.value}`}
          onClick={() => onFilter('level', value.value, false)}
        >
          <span class="explorer-level-name">{value.value || '(none)'}</span>
          <span class="explorer-level-count">{value.count.toLocaleString()}</span>
          <span class="explorer-level-bar" style={{ '--share': `${value.share * 100}%` } as never} />
        </button>
      ))}
    </div>
  )
}

interface GroupProps {
  field: Field
  params: Params
  open: boolean
  active: boolean
  column: boolean
  onToggle: () => void
  onToggleColumn: () => void
  onFilter: (field: string, value: string, negated: boolean) => void
}

function FieldGroup({ field, params, open, active, column, onToggle, onToggleColumn, onFilter }: GroupProps) {
  const [values, setValues] = useState<FieldValue[] | null>(null)
  const [error, setError] = useState(false)

  // Re-fetches whenever the surrounding query changes, because "top values"
  // means top values of what is currently on screen — a sidebar showing the
  // distribution of some other result set is worse than showing nothing.
  useEffect(() => {
    if (!open) return

    const controller = new AbortController()

    setError(false)
    api
      .fields({ ...params, field: field.name, top: 8 }, controller.signal)
      .then((response) => setValues(response.values))
      .catch((err) => {
        if ('AbortError' !== err.name) setError(true)
      })

    return () => controller.abort()
  }, [open, field.name, params.q, params.stream, params.from, params.to])

  return (
    <section class={`explorer-field ${open ? 'open' : ''} ${active ? 'active' : ''} ${column ? 'pinned' : ''}`}>
      <button class="explorer-head" aria-expanded={open} onClick={onToggle}>
        <span class="explorer-name">{field.name}</span>

        {/*
          The count is scoped to the query when the server could count it, and
          falls back to the lifetime tally when it could not. Scoped is the
          number the panel is actually asked for — a field with four million
          lifetime records and none in the last ten minutes was reading as the
          most interesting thing in the list. Zero is rendered rather than
          hidden: "this field exists here and none of your matches have it" is
          worth knowing, and it is why the field is at the bottom.

          Envelope fields have no lifetime tally at all — they are on every
          record by construction, so the catalog does not count them — so with
          no scoped count either, the kind stands in rather than leaving half
          the column blank, which reads as data failing to load.
        */}
        <span
          class={`explorer-kind kind-${field.kind}`}
          title={
            undefined !== field.matches
              ? `${field.matches.toLocaleString()} matching · ${field.occurrences.toLocaleString()} all time · ${field.kind}`
              : field.occurrences
                ? `${field.occurrences.toLocaleString()} records, all time · ${field.kind}`
                : `on every record · ${field.kind}`
          }
        >
          {undefined !== field.matches
            ? formatCount(field.matches)
            : field.occurrences
              ? formatCount(field.occurrences)
              : field.kind}
        </span>
      </button>

      {/*
        Pinning a field as a column is an action on the row, so it appears on
        the row — on hover, or permanently once the column is pinned. It used
        to sit in a gutter of its own in front of every name, which indented
        the whole list past an empty square and read as a checkbox nobody had
        ever ticked.
      */}
      <Button
        variant="icon"
        size="sm"
        on={column}
        class="explorer-column"
        title={column ? `Remove the ${field.name} column` : `Show ${field.name} as a column`}
        onClick={onToggleColumn}
      >
        ⊞
      </Button>

      {open && (
        <div class="explorer-values">
          {error && <p class="explorer-note">Could not load values</p>}
          {!error && null === values && <p class="explorer-note">Loading…</p>}
          {!error && values && 0 === values.length && <p class="explorer-note">No values</p>}

          {values?.map((value) => (
            <div class="explorer-value" key={value.value}>
              <button
                class="explorer-value-main"
                title={`Filter to ${field.name}=${value.value}`}
                onClick={() => onFilter(field.name, value.value, false)}
              >
                {/*
                  The share bar sits behind the label rather than beside it:
                  the sidebar is narrow, and a value that needs the width
                  should get it.
                */}
                <span class="explorer-share" style={{ width: `${value.share * 100}%` }} />
                <span class="explorer-label">{value.value || '(empty)'}</span>
                <span class="explorer-count">{value.count.toLocaleString()}</span>
              </button>

              <Button
                variant="icon"
                size="sm"
                class="explorer-exclude"
                title={`Exclude ${field.name}=${value.value}`}
                aria-label={`Exclude ${value.value}`}
                onClick={() => onFilter(field.name, value.value, true)}
              >
                ⊖
              </Button>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
