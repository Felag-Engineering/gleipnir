import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'

function createMatchMedia(darkMatches: boolean) {
  return (query: string) => ({
    matches: darkMatches && query === '(prefers-color-scheme: dark)',
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })
}

// jsdom does not implement matchMedia — provide a minimal mock before importing the hook
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation(createMatchMedia(false)),
})

import { useTheme, THEME_STORAGE_KEY } from './useTheme'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  vi.mocked(window.matchMedia).mockImplementation(createMatchMedia(false))
})

describe('useTheme', () => {
  it('defaults to system when localStorage is empty', () => {
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('system')
  })

  it('reads stored preference "light" from localStorage', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'light')
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('light')
  })

  it('reads stored preference "dark" from localStorage', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'dark')
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('dark')
  })

  it('falls back to "system" for invalid localStorage value', () => {
    localStorage.setItem(THEME_STORAGE_KEY, 'purple')
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('system')
  })

  it('setTheme persists to localStorage', () => {
    const { result } = renderHook(() => useTheme())
    act(() => { result.current.setTheme('light') })
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('light')
  })

  it('setTheme("light") sets data-theme="light" on documentElement', () => {
    const { result } = renderHook(() => useTheme())
    act(() => { result.current.setTheme('light') })
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('setTheme("dark") sets data-theme="dark" on documentElement', () => {
    const { result } = renderHook(() => useTheme())
    act(() => { result.current.setTheme('dark') })
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('resolvedTheme is "dark" when OS prefers dark and theme is "system"', () => {
    vi.mocked(window.matchMedia).mockImplementation(createMatchMedia(true))
    const { result } = renderHook(() => useTheme())
    expect(result.current.resolvedTheme).toBe('dark')
  })

  it('resolvedTheme is "light" when OS prefers light and theme is "system"', () => {
    // matchMedia('(prefers-color-scheme: dark)').matches === false => light
    const { result } = renderHook(() => useTheme())
    expect(result.current.resolvedTheme).toBe('light')
  })

  it('resolvedTheme matches explicit preference regardless of OS', () => {
    vi.mocked(window.matchMedia).mockImplementation(createMatchMedia(true))
    localStorage.setItem(THEME_STORAGE_KEY, 'light')
    const { result } = renderHook(() => useTheme())
    expect(result.current.resolvedTheme).toBe('light')
  })
})
