// extractElicitationLink finds the URL a server put in an elicitation message
// so the card can present it as an explicit "open in browser" step
// (mcp-realignment-spec.md §6.1, URL mode).
//
// It DETECTS rather than receives: MRTR has no typed field for a continuation
// URL today, so the only place one can arrive is inside the server-controlled
// message text. That makes the parsing security-relevant rather than cosmetic —
// the string is chosen by the server, and the operator is being invited to
// click it.
//
// Two rules follow from that:
//
//   - Only http and https. A `javascript:` or `data:` URL in an anchor is a
//     script the server got a human to run; refusing every other scheme is the
//     only version of this that is safe to render at all.
//   - The host is returned separately so the card can show WHERE the link goes
//     rather than leaving it to be read out of a long string. A look-alike
//     domain is the whole attack, and it is invisible in a truncated URL.
//
// Only the first link is returned. A message carrying several is a message
// asking the operator to choose between destinations, which is not a step —
// and picking one for them would be arbitrary.

export interface ElicitationLink {
  /** The full URL, safe to place in href. */
  href: string
  /** The hostname alone, for display. */
  host: string
}

// URL_PATTERN matches a bare http(s) URL. Trailing punctuation that commonly
// ends a sentence is excluded from the match so "see https://example.com." does
// not produce a link with a dot on the end.
const URL_PATTERN = /https?:\/\/[^\s<>"']+/i

const TRAILING_PUNCTUATION = /[.,;:!?)\]}]+$/

export function extractElicitationLink(message: string): ElicitationLink | null {
  if (!message) return null

  const match = URL_PATTERN.exec(message)
  if (!match) return null

  const candidate = match[0].replace(TRAILING_PUNCTUATION, '')

  let parsed: URL
  try {
    parsed = new URL(candidate)
  } catch {
    return null
  }

  // Re-check the scheme against the parsed value rather than trusting the
  // regex: a URL constructor is the authority on what the browser will do with
  // the string, and that is what an anchor hands it.
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return null
  }
  if (!parsed.hostname) {
    return null
  }

  return { href: parsed.href, host: parsed.hostname }
}
