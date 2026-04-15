import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useLocalStorage } from './useLocalStorage'

beforeEach(() => {
  localStorage.clear()
})

describe('useLocalStorage', () => {
  it('returns defaultValue when key is not set', () => {
    const { result } = renderHook(() => useLocalStorage('k', false))
    expect(result.current[0]).toBe(false)
  })

  it('reads an existing boolean from localStorage', () => {
    localStorage.setItem('k', 'true')
    const { result } = renderHook(() => useLocalStorage('k', false))
    expect(result.current[0]).toBe(true)
  })

  it('persists the new value to localStorage when setter is called', () => {
    const { result } = renderHook(() => useLocalStorage('k', false))
    act(() => { result.current[1](true) })
    expect(localStorage.getItem('k')).toBe('true')
  })

  it('updates the in-memory state when setter is called', () => {
    const { result } = renderHook(() => useLocalStorage('k', false))
    act(() => { result.current[1](true) })
    expect(result.current[0]).toBe(true)
  })

  it('falls back to defaultValue when stored JSON is corrupt', () => {
    localStorage.setItem('k', 'not-json{{{')
    const { result } = renderHook(() => useLocalStorage('k', 42))
    expect(result.current[0]).toBe(42)
  })

  it('works with string values', () => {
    const { result } = renderHook(() => useLocalStorage('k', 'default'))
    act(() => { result.current[1]('updated') })
    expect(result.current[0]).toBe('updated')
    expect(localStorage.getItem('k')).toBe('"updated"')
  })
})
