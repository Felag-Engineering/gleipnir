import { describe, it, expect } from 'vitest'
import { extractElicitationLink } from './elicitationLink'

describe('extractElicitationLink', () => {
  it('finds a bare URL and separates the host', () => {
    const link = extractElicitationLink('Finish authorising at https://auth.example.com/consent?id=7')
    expect(link).toEqual({
      href: 'https://auth.example.com/consent?id=7',
      host: 'auth.example.com',
    })
  })

  it('drops sentence punctuation that is not part of the URL', () => {
    const link = extractElicitationLink('Open https://example.com/step.')
    expect(link?.href).toBe('https://example.com/step')
  })

  // The message is chosen by the server and the operator is being invited to
  // click. Anything that is not http(s) is a scheme that does something other
  // than navigate, and a `javascript:` anchor is a script a server got a human
  // to run.
  it.each([
    ['javascript:alert(1)', 'javascript'],
    ['data:text/html,<script>alert(1)</script>', 'data'],
    ['file:///etc/passwd', 'file'],
    ['vbscript:msgbox(1)', 'vbscript'],
  ])('refuses %s', (message) => {
    expect(extractElicitationLink(message)).toBeNull()
  })

  it('refuses a scheme smuggled after prose', () => {
    expect(extractElicitationLink('Click here: javascript:alert(1)')).toBeNull()
  })

  it('returns only the first link', () => {
    const link = extractElicitationLink('Either https://one.example.com or https://two.example.com')
    expect(link?.host).toBe('one.example.com')
  })

  it.each([
    ['no url at all', 'Please confirm the deployment.'],
    ['empty message', ''],
    ['scheme with no host', 'https://'],
  ])('returns null for %s', (_name, message) => {
    expect(extractElicitationLink(message)).toBeNull()
  })

  // A look-alike domain is the attack this display exists to expose, so the
  // host must be the parsed hostname rather than anything read out of the
  // string — userinfo before an @ is exactly how a URL is made to read as one
  // domain while going to another.
  it('reports the real host, not the userinfo that precedes it', () => {
    const link = extractElicitationLink('Go to https://accounts.google.com@evil.example.com/login')
    expect(link?.host).toBe('evil.example.com')
  })
})
