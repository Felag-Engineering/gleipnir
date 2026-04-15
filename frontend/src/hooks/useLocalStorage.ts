import { useState } from 'react'

/**
 * Persists a state value to localStorage under `key`. Reads the stored value
 * on first render; writes back whenever the setter is called.
 *
 * Serialization is JSON: booleans, numbers, strings, and plain objects all
 * round-trip correctly. If localStorage is unavailable (private browsing,
 * quota exceeded) or the stored value cannot be parsed, `defaultValue` is
 * returned and writes are silently ignored.
 */
export function useLocalStorage<T>(key: string, defaultValue: T): [T, (value: T) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key)
      return raw !== null ? (JSON.parse(raw) as T) : defaultValue
    } catch {
      return defaultValue
    }
  })

  function persist(next: T): void {
    setValue(next)
    try {
      localStorage.setItem(key, JSON.stringify(next))
    } catch {
      // localStorage unavailable; state update still applies for this session
    }
  }

  return [value, persist]
}
