/*!
 * A variable-height virtualizer.
 *
 * Hand-written rather than a dependency, because the requirement is narrow —
 * a single vertical list, most rows exactly one line tall, a few expanded to
 * arbitrary heights — and a virtualizer is the one piece of a log viewer that
 * has to be genuinely good. Owning ~120 lines beats owning a transitive tree
 * to get them.
 *
 * The model is a prefix-sum over row heights. Rows default to an estimate and
 * are corrected once measured, which is all the expansion case needs: an
 * expanded row reports its real height on the next frame and everything below
 * it shifts by the difference.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'

export interface Slice {
  /** Index of the first row to render. */
  start: number
  /** Index one past the last row to render. */
  end: number
  /** Total scrollable height. */
  total: number
  /** Pixel offset of each rendered row, indexed from `start`. */
  offsets: number[]
}

export interface Virtual extends Slice {
  /** Attach to the scrolling element. */
  scrollRef: (element: HTMLElement | null) => void
  /** Attach to each rendered row so it can be measured. */
  measure: (index: number) => (element: HTMLElement | null) => void
  /** Scroll a row into view. */
  scrollTo: (index: number, align?: 'start' | 'center') => void
  /** Scroll to the newest row. */
  scrollToEnd: () => void
  /** True when the viewport is at the bottom, within a small tolerance. */
  atEnd: boolean
}

/** How many rows to render beyond the viewport, to hide scroll latency. */
const OVERSCAN = 8

export function useVirtual(count: number, estimate: number): Virtual {
  const [scroll, setScroll] = useState(0)
  const [height, setHeight] = useState(0)
  const [atEnd, setAtEnd] = useState(true)

  /*!
   * The scroller lives in state, not a ref.
   *
   * A ref plus a mount-once effect looks equivalent and is not: the list
   * renders an empty state before any data arrives, so the scrolling element
   * does not exist on the first pass. The effect would run, find nothing, and
   * never run again — leaving the scroll position permanently stuck at zero
   * while the user scrolled a viewport that rendered rows from the top.
   */
  const [container, setContainer] = useState<HTMLElement | null>(null)

  // Bumped when a measurement changes, so the prefix sum recomputes. Heights
  // live in a ref, and mutating a ref cannot invalidate a memo on its own.
  const [version, bump] = useState(0)

  const heights = useRef<number[]>([])

  // Heights persist across renders and are only reset when the list shrinks
  // past them, so scrolling back to a measured row does not re-flow.
  if (heights.current.length > count) {
    heights.current.length = count
  }

  const offsets = useMemo(() => {
    const out = new Array<number>(count + 1)
    out[0] = 0

    for (let i = 0; i < count; i++) {
      out[i + 1] = out[i] + (heights.current[i] ?? estimate)
    }

    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [count, estimate, version])

  const total = offsets[count] ?? 0

  const scrollRef = useCallback((element: HTMLElement | null) => setContainer(element), [])

  useEffect(() => {
    if (!container) return

    const onScroll = () => {
      setScroll(container.scrollTop)
      setAtEnd(container.scrollHeight - container.scrollTop - container.clientHeight < 24)
    }

    const observer = new ResizeObserver(() => setHeight(container.clientHeight))
    observer.observe(container)

    container.addEventListener('scroll', onScroll, { passive: true })

    setHeight(container.clientHeight)
    onScroll()

    return () => {
      container.removeEventListener('scroll', onScroll)
      observer.disconnect()
    }
  }, [container])

  /*!
   * measure records a row's real height.
   *
   * Only a genuine change triggers a re-render — a row that measures the same
   * as last frame must not, or expanding one row would loop forever.
   */
  const measure = useCallback(
    (index: number) => (element: HTMLElement | null) => {
      if (!element) return

      const measured = element.offsetHeight
      if (0 === measured) return

      if (heights.current[index] !== measured) {
        heights.current[index] = measured
        bump((n) => n + 1)
      }
    },
    []
  )

  // Binary search for the first row that reaches the viewport.
  const find = useCallback(
    (offset: number) => {
      let lo = 0
      let hi = count

      while (lo < hi) {
        const mid = (lo + hi) >> 1
        if (offsets[mid + 1] <= offset) lo = mid + 1
        else hi = mid
      }

      return Math.min(lo, Math.max(0, count - 1))
    },
    [count, offsets]
  )

  const start = Math.max(0, find(scroll) - OVERSCAN)
  const end = Math.min(count, find(scroll + height) + 1 + OVERSCAN)

  const scrollTo = useCallback(
    (index: number, align: 'start' | 'center' = 'start') => {
      if (!container) return

      const top = offsets[Math.max(0, Math.min(index, count - 1))] ?? 0
      const adjust = 'center' === align ? container.clientHeight / 2 - estimate / 2 : 0

      container.scrollTop = Math.max(0, top - adjust)
    },
    [container, count, estimate, offsets]
  )

  const scrollToEnd = useCallback(() => {
    if (!container) return
    container.scrollTop = container.scrollHeight
  }, [container])

  return {
    start,
    end,
    total,
    offsets: offsets.slice(start, end),
    scrollRef,
    measure,
    scrollTo,
    scrollToEnd,
    atEnd
  }
}
