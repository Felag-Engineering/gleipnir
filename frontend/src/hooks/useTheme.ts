import { useEffect, useState } from 'react'

export type ThemePreference = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'gleipnir-theme'

function isValidPreference(v: unknown): v is ThemePreference {
  return v === 'system' || v === 'light' || v === 'dark'
}

function getSystemTheme(): ResolvedTheme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function resolveTheme(pref: ThemePreference): ResolvedTheme {
  if (pref === 'light' || pref === 'dark') return pref
  return getSystemTheme()
}

export function useTheme(): {
  theme: ThemePreference
  setTheme: (t: ThemePreference) => void
  resolvedTheme: ResolvedTheme
} {
  const [theme, setThemeState] = useState<ThemePreference>(() => {
    try {
      const stored = localStorage.getItem(THEME_STORAGE_KEY)
      return isValidPreference(stored) ? stored : 'system'
    } catch {
      return 'system'
    }
  })

  const [resolvedTheme, setResolvedTheme] = useState<ResolvedTheme>(() =>
    resolveTheme(theme)
  )

  // Apply theme to DOM and persist to localStorage whenever preference changes
  useEffect(() => {
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme)
    } catch {
      // localStorage may be unavailable in private browsing
    }
    document.documentElement.setAttribute('data-theme', theme)
    setResolvedTheme(resolveTheme(theme))
  }, [theme])

  // Update resolvedTheme when OS preference changes and theme is 'system'
  useEffect(() => {
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    function handleChange() {
      if (theme === 'system') {
        setResolvedTheme(getSystemTheme())
      }
    }
    mediaQuery.addEventListener('change', handleChange)
    return () => mediaQuery.removeEventListener('change', handleChange)
  }, [theme])

  function setTheme(t: ThemePreference) {
    setThemeState(t)
  }

  return { theme, setTheme, resolvedTheme }
}
