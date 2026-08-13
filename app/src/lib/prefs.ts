/*!
 * Persisted user preferences.
 *
 * Replaces localForage (and its Angular wrapper). Preferences are a few
 * hundred bytes of scalars, so plain localStorage is enough — and synchronous
 * reads mean the app paints with the right theme on the first frame instead of
 * flashing the default while an async store resolves.
 */

import type { Prefs, Theme } from './types.js'

/*!
 * The storage key carries a version, and this is the second.
 *
 * v2 flipped the default theme from light to dark and rebuilt both palettes
 * around it. A value persisted under the old key would silently pin an
 * existing tab to the previous design — which is exactly what happened during
 * development: the redesign shipped, and the only person looking at it kept
 * seeing the old one because their browser remembered a preference from an
 * earlier experiment. Bumping the key is a one-line reset that beats
 * explaining to every user why nothing changed.
 */
const KEY = 'rtail:prefs:v2'

export const FONT_SIZE_MIN = 1
export const FONT_SIZE_MAX = 7
export const FONT_FAMILY_COUNT = 6

const DEFAULTS: Prefs = {
  theme: 'dark',
  fontFamily: 1,
  fontSize: 4,
  favorites: [],
  // Tucked away by default, as the reference is: the chart is the best way to
  // find a spike and the worst use of eighty pixels when you already know what
  // you are looking for.
  timeline: false
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

/**
 * Reads persisted preferences, falling back to defaults for anything missing
 * or malformed. Never throws: a corrupt entry should not stop the app booting.
 */
export function loadPrefs(): Prefs {
  let stored: Partial<Prefs> = {}

  try {
    stored = JSON.parse(localStorage.getItem(KEY) ?? '{}') as Partial<Prefs>
  } catch {
    stored = {}
  }

  return {
    theme: 'light' === stored.theme ? 'light' : 'dark',
    fontFamily: clamp(Number(stored.fontFamily) || DEFAULTS.fontFamily, 1, FONT_FAMILY_COUNT),
    fontSize: clamp(Number(stored.fontSize) || DEFAULTS.fontSize, FONT_SIZE_MIN, FONT_SIZE_MAX),
    favorites: Array.isArray(stored.favorites) ? stored.favorites.filter(isString) : [],
    timeline: true === stored.timeline
  }
}

export function savePrefs(prefs: Prefs): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(prefs))
  } catch {
    // Private browsing, quota, disabled storage — preferences simply don't
    // persist. Not worth interrupting the user over.
  }
}



export function isTheme(value: string): value is Theme {
  return 'dark' === value || 'light' === value
}

function isString(value: unknown): value is string {
  return 'string' === typeof value
}
