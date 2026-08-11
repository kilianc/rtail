import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import type { ComponentChildren } from 'preact'

interface Props {
  /** The element the panel is anchored under. */
  anchor: HTMLElement | null
  onClose: () => void
  class?: string
  children: ComponentChildren
}

/**
 * A panel anchored under a trigger, flush to the viewport edge it runs into.
 *
 * It measures itself and clamps, rather than carrying a hardcoded offset per
 * popover the way the library it replaced did.
 */
export function Popover({ anchor, onClose, class: className, children }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!anchor || !el) return

    const place = () => {
      const trigger = anchor.getBoundingClientRect()
      const width = el.offsetWidth

      // Right-align to the trigger, then pull back if that would overflow.
      const left = Math.max(0, Math.min(trigger.right - width, window.innerWidth - width))

      setPos({ top: Math.round(trigger.bottom), left: Math.round(left) })
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
      <div ref={ref} class={`popover-content ${className ?? ''}`}>
        {children}
      </div>
    </div>
  )
}
