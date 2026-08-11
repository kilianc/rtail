/*!
 * Persisted user preferences.
 *
 * Replaces localForage (and its Angular wrapper). Preferences are a few
 * hundred bytes of scalars, so plain localStorage is enough — and synchronous
 * reads mean the app paints with the right theme on the first frame instead of
 * flashing the default while an async store resolves.
 */

import type { Prefs, Theme } from './types.ts'

const KEY = 'rtail:prefs'

export const FONT_SIZE_MIN = 1
export const FONT_SIZE_MAX = 7
export const FONT_FAMILY_COUNT = 6

const DEFAULTS: Prefs = {
  theme: 'dark',
  fontFamily: 1,
  fontSize: 4,
  ascending: true,
  sidebarWidth: 240,
  favorites: [],
  hiddenTimestamps: []
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
    ascending: 'boolean' === typeof stored.ascending ? stored.ascending : DEFAULTS.ascending,
    sidebarWidth: clamp(Number(stored.sidebarWidth) || DEFAULTS.sidebarWidth, 180, 600),
    favorites: Array.isArray(stored.favorites) ? stored.favorites.filter(isString) : [],
    hiddenTimestamps: Array.isArray(stored.hiddenTimestamps)
      ? stored.hiddenTimestamps.filter(isString)
      : []
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

/** Remembers which stream was open, so a reload lands back on it. */
export function loadActiveStream(): string | null {
  try {
    return localStorage.getItem('rtail:activeStream')
  } catch {
    return null
  }
}

export function saveActiveStream(stream: string | null): void {
  try {
    if (null === stream) localStorage.removeItem('rtail:activeStream')
    else localStorage.setItem('rtail:activeStream', stream)
  } catch {
    // see savePrefs
  }
}

export function isTheme(value: string): value is Theme {
  return 'dark' === value || 'light' === value
}

function isString(value: unknown): value is string {
  return 'string' === typeof value
}
