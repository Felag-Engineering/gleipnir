import { describe, it, expect } from 'vitest'
import { parseUserAgent } from '@/utils/userAgent'

describe('parseUserAgent', () => {
  it('returns "Unknown client" for empty string', () => {
    expect(parseUserAgent('')).toBe('Unknown client')
  })

  it('parses Chrome', () => {
    const ua =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36'
    expect(parseUserAgent(ua)).toBe('Chrome 124')
  })

  it('parses Firefox', () => {
    const ua = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:124.0) Gecko/20100101 Firefox/124.0'
    expect(parseUserAgent(ua)).toBe('Firefox 124')
  })

  it('parses Safari (excludes Chrome UA)', () => {
    const ua =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15'
    expect(parseUserAgent(ua)).toBe('Safari 17')
  })

  it('parses Edge (Edg/ token takes precedence over Chrome)', () => {
    const ua =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0'
    expect(parseUserAgent(ua)).toBe('Edge 124')
  })

  it('truncates unknown UA strings to 60 characters', () => {
    const ua = 'SomeCustomBot/1.0 (totally-unknown-agent-string-that-is-very-long-indeed-yes-it-is)'
    expect(parseUserAgent(ua)).toBe(ua.slice(0, 60))
    expect(parseUserAgent(ua).length).toBe(60)
  })

  it('returns short unknown UA strings as-is', () => {
    const ua = 'curl/7.88.1'
    expect(parseUserAgent(ua)).toBe('curl/7.88.1')
  })
})
