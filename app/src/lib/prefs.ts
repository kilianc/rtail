/*!
 * Persisted user preferences.
 *
 * Replaces localForage (and its Angular wrapper). Preferences are a few
 * hundred bytes of scalars, so plain localStorage is enough — and synchronous
 * reads mean the app paints with the right theme on the first frame instead of
 * flashing the default while an async store resolves.
 */

import type { Prefs, Theme } from './types.js'

const KEY = 'rtail:prefs'

export const FONT_SIZE_MIN = 1
export const FONT_SIZE_MAX = 7
export const FONT_FAMILY_COUNT = 6

const DEFAULTS: Prefs = {
  theme: 'dark',
  fontFamily: 1,
  fontSize: 4,
  favorites: []
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
    favorites: Array.isArray(stored.favorites) ? stored.favorites.filter(isString) : []
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
