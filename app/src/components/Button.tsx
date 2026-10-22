/*!
 * The button.
 *
 * There were nineteen of these before this file existed — `.command-run`,
 * `.results-follow`, `.timerange-apply`, `.row-action`, `.results-more`,
 * `.leaf-filter`, `.explorer-exclude`, and a dozen more — each a hand-written
 * block that chose its own height, padding, radius, weight and hover. They
 * drifted apart the moment any one of them was touched, which is the actual
 * reason the chrome kept looking slightly wrong in ways no single change could
 * fix. Styling a button is not the kind of decision that should be made
 * nineteen times.
 *
 * Stripped of where they sit, all nineteen were four things:
 *
 *   solid    the one action a view is for — Run query, Apply, Jump to latest
 *   outline  a secondary action that still needs an edge — Copy, Reset
 *   ghost    a quiet action inside something else — an example, a filter, a row
 *   icon     a glyph in a square hit area — close, exclude, pin, settings
 *
 * Everything else that differed was *placement* — absolute positioning, a
 * reveal-on-hover opacity, a width — and placement belongs to the container,
 * not to the button. So those call sites keep a class of their own for that
 * one job and take their appearance from here.
 *
 * Five controls deliberately do NOT use this: the stream picker, the time
 * range trigger, the live toggle, and the two disclosure headers. They are
 * compound controls that happen to be rendered as a <button> — a pill with a
 * caret and a count, a dot that pulses — and forcing them through a variant
 * prop would be the same mistake committed in a newer place.
 */

import type { ComponentChildren, JSX, Ref } from 'preact'

type Variant = 'solid' | 'outline' | 'ghost' | 'icon'

interface Props extends Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'size' | 'icon' | 'ref'> {
  variant?: Variant
  /** `md` is a control (22px); `sm` is a row (20px). */
  size?: 'sm' | 'md'
  /** A masked glyph drawn in ::after, by icon name. */
  icon?: string
  /**
   * A toggle that is currently on.
   *
   * Setting it also sets aria-pressed, so a toggle cannot be built that looks
   * pressed without saying so — which was true of four of the originals.
   */
  on?: boolean
  /** Fills the inline size of its container. */
  block?: boolean
  /*!
   * A ref to the <button> itself, for callers that must measure it.
   *
   * Not `ref`, and `ref` is deliberately removed from the props above so that
   * writing it is a type error rather than a silent one. Preact pulls `ref`
   * off a function component's props and binds it to the component instance,
   * so `<Button ref={x}>` compiles, runs, and leaves x.current holding
   * something with no getBoundingClientRect — which is exactly how the two
   * popovers anchored to this would have broken, at runtime, in a way no test
   * here would catch.
   */
  elementRef?: Ref<HTMLButtonElement>
  children?: ComponentChildren
}

export function Button({
  variant = 'ghost',
  size = 'md',
  icon,
  on,
  block,
  elementRef,
  class: extra,
  type,
  children,
  ...rest
}: Props) {
  const classes = [
    'btn',
    `btn-${variant}`,
    `btn-${size}`,
    icon ? `btn-icon-${icon}` : '',
    on ? 'on' : '',
    block ? 'btn-block' : '',
    extra ?? ''
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <button
      ref={elementRef}
      // Buttons inside the query pane sit within no form, but the ones in the
      // time-range popover do — and a bare <button> there submits it and
      // reloads the page. Defaulting to "button" makes that unrepresentable.
      type={type ?? 'button'}
      class={classes}
      aria-pressed={undefined === on ? undefined : on}
      {...rest}
    >
      {children}
    </button>
  )
}
