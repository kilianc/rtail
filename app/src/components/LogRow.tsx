/*!
 * One log row, collapsed and expanded.
 *
 * The expanded state is the best thing Stackdriver has and the reason most
 * queries never get typed: every leaf carries "filter to this" and "exclude
 * this", so you refine by pointing at what you can already see. The command
 * bar updates to match, which keeps the two representations honest — whatever
 * you built by clicking is something you could have typed.
 */

import { formatTimestamp } from '../lib/format.js'
import type { Line } from '../lib/types.js'

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
  onToggle: () => void
  onFilter: (action: FilterAction) => void
  onContext: () => void
}

export function LogRow({ line, expanded, selected, active, onToggle, onFilter, onContext }: Props) {
  const level = (line.level ?? '').toUpperCase()

  return (
    <div
      class={`row ${expanded ? 'expanded' : ''} ${selected ? 'selected' : ''} level-${level.toLowerCase() || 'none'}`}
      onClick={onToggle}
    >
      <div class="row-head">
        <time class="row-time" dateTime={new Date(line.timestamp).toISOString()}>
          {formatTimestamp(line.timestamp)}
        </time>

        {level && <span class="row-level">{level}</span>}

        <span class="row-message" dangerouslySetInnerHTML={{ __html: line.html }} />
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

            {Object.entries(line.fields ?? {}).map(([key, value]) => (
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
