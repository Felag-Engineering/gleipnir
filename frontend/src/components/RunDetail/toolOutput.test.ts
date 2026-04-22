import { describe, it, expect } from 'vitest'
import { parseToolOutput } from './toolOutput'

describe('parseToolOutput', () => {
  it('MCP text-only content array → joined string', () => {
    const raw = JSON.stringify([
      { type: 'text', text: 'INFO app started' },
      { type: 'text', text: 'INFO ready' },
    ])
    expect(parseToolOutput(raw)).toBe('INFO app started\nINFO ready')
  })

  it('single text item → string (no trailing newline)', () => {
    const raw = JSON.stringify([{ type: 'text', text: 'done' }])
    expect(parseToolOutput(raw)).toBe('done')
  })

  it('MCP content with an image item → raw array (fallback)', () => {
    const raw = JSON.stringify([
      { type: 'text', text: 'Saved' },
      { type: 'image', data: 'abc123' },
    ])
    const result = parseToolOutput(raw)
    expect(Array.isArray(result)).toBe(true)
    expect(result).toEqual([
      { type: 'text', text: 'Saved' },
      { type: 'image', data: 'abc123' },
    ])
  })

  it('empty array → returned as-is (fallback)', () => {
    const raw = JSON.stringify([])
    const result = parseToolOutput(raw)
    expect(Array.isArray(result)).toBe(true)
    expect((result as unknown[]).length).toBe(0)
  })

  it('non-array JSON (plain object) → unchanged object', () => {
    const obj = { lines: ['a', 'b'], count: 2 }
    const result = parseToolOutput(JSON.stringify(obj))
    expect(result).toEqual(obj)
  })

  it('unparseable string → original string unchanged', () => {
    const raw = 'permission denied: /tmp/report.txt'
    expect(parseToolOutput(raw)).toBe(raw)
  })

  it('JSON-encoded string (e.g. "Message sent") → unwrapped string', () => {
    // JSON.parse('"Message sent"') → 'Message sent' (a string).
    // parseToolOutput returns it as-is — not an array, so no envelope logic.
    const raw = '"Message sent"'
    expect(parseToolOutput(raw)).toBe('Message sent')
  })
})
