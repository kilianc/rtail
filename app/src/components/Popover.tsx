import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

const VIEWPORT_MARGIN = 8

interface Props {
  /** The element the panel is anchored under. */
  anchor: HTMLElement | null
  onClose: () => void
  class?: string
  children: ComponentChildren
}

/**
 * A panel anchored below a trigger button.
 *
 * Replaces angular-rt-popup. That version positioned panels with hardcoded
 * pixel offsets per popover, which is why the settings panel needed a magic
 * `left: -60px` to stay on screen; this one measures itself, clamps to the
 * viewport, and moves its arrow to stay over the trigger.
 */
export function Popover({ anchor, onClose, class: className, children }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: number; left: number; arrow: number } | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!anchor || !el) return

    const place = () => {
      const trigger = anchor.getBoundingClientRect()
      const width = el.offsetWidth
      const centre = trigger.left + trigger.width / 2
      const max = window.innerWidth - width - VIEWPORT_MARGIN
      const left = Math.min(Math.max(centre - width / 2, VIEWPORT_MARGIN), Math.max(max, VIEWPORT_MARGIN))

      setPos({ top: trigger.bottom, left, arrow: centre - left })
    }

    place()

    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [anchor])

  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (ref.current?.contains(target ?? null)) return
      if (anchor?.contains(target ?? null)) return
      onClose()
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if ('Escape' === event.key) onClose()
    }

    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)

    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [anchor, onClose])

  return (
    <div
      class="popover"
      style={{
        top: `${pos?.top ?? 0}px`,
        left: `${pos?.left ?? 0}px`,
        // Avoid a flash at 0,0 on the first paint, before measurement.
        visibility: pos ? 'visible' : 'hidden'
      }}
    >
      <div
        ref={ref}
        class={`popover-content ${className ?? ''}`}
        style={{ ['--arrow-x' as string]: `${pos?.arrow ?? 0}px` }}
      >
        {children}
      </div>
    </div>
  )
}
