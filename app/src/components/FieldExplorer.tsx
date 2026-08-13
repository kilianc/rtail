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

export function FieldExplorer({ fields, params, active, columns, onFilter, onToggleColumn }: Props) {
  const [open, setOpen] = useState<string[]>(() =>
    fields.filter((field) => FAVOURED.includes(field.name)).slice(0, 3).map((field) => field.name)
  )
  const [filter, setFilter] = useState('')

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
    .filter((field) => 'envelope' !== field.kind || ['level', 'stream', 'host'].includes(field.name))
    .filter((field) => field.name.toLowerCase().includes(filter.toLowerCase()))
    .sort((a, b) => {
      const favour = Number(FAVOURED.includes(b.name)) - Number(FAVOURED.includes(a.name))
      if (favour) return favour
      return (b.occurrences ?? 0) - (a.occurrences ?? 0) || a.name.localeCompare(b.name)
    })

  return (
    <aside class="explorer">
      <div class="explorer-title">Log fields</div>

      <div class="explorer-search">
        <input
          type="text"
          placeholder="Filter fields"
          aria-label="Filter fields"
          value={filter}
          onInput={(event) => setFilter(event.currentTarget.value)}
        />
      </div>

      <div class="explorer-list">
        {0 === ranked.length && <p class="explorer-empty">No fields yet</p>}

        {ranked.map((field) => (
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
        ))}
      </div>
    </aside>
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
    <section class={`explorer-field ${open ? 'open' : ''} ${active ? 'active' : ''}`}>
      {/*
        The column toggle rides on the section header rather than hiding in a
        menu: promoting a field is a thing you do while scanning the list of
        fields, which is exactly when you are already looking at this row.
      */}
      <button
        class={`explorer-column ${column ? 'on' : ''}`}
        title={column ? `Remove the ${field.name} column` : `Show ${field.name} as a column`}
        aria-pressed={column}
        onClick={onToggleColumn}
      >
        ⊞
      </button>

      <button class="explorer-head" aria-expanded={open} onClick={onToggle}>
        <span class="explorer-name">{field.name}</span>

        {/*
          The count, not the type — which is what the real panel shows, and it
          is the more useful of the two here: the type is already in the
          autocomplete, whereas "how much of this is there" is the question the
          panel exists to answer. The type stays available on hover.
        */}
        {/*
          Envelope fields carry no occurrence count — they are on every record
          by construction, so the catalog does not tally them. Falling back to
          the kind keeps the column from being blank for half the list, which
          reads as data failing to load rather than as a count that does not
          apply.
        */}
        <span
          class={`explorer-kind kind-${field.kind}`}
          title={
            field.occurrences
              ? `${field.occurrences.toLocaleString()} records, all time · ${field.kind}`
              : `on every record · ${field.kind}`
          }
        >
          {field.occurrences ? field.occurrences.toLocaleString() : field.kind}
        </span>
      </button>

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

              <button
                class="explorer-exclude"
                title={`Exclude ${field.name}=${value.value}`}
                aria-label={`Exclude ${value.value}`}
                onClick={() => onFilter(field.name, value.value, true)}
              >
                ⊖
              </button>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
