import { createElement } from 'react'
import type { ReactNode } from 'react'

// Inline markdown tokenizer supporting bold (**), italic (* and _), and inline code (`).
// An underscore inside a word (snake_case) is literal text, never an italic marker.
// Only inline features — no block-level parsing (headings, lists, links). Unknown or
// unmatched syntax is emitted verbatim. The caller is responsible for container
// white-space: pre-wrap so newlines are preserved.

type Token =
  | { kind: 'text'; value: string }
  | { kind: 'bold'; children: Token[] }
  | { kind: 'italic'; children: Token[] }
  | { kind: 'code'; value: string }

// Delimiter specs: [delimiter, token kind]. Order matters — `**` must be
// checked before `*` so the two-char sequence is not consumed as two singles.
const DELIMITERS: [string, 'bold' | 'italic' | 'code'][] = [
  ['**', 'bold'],
  ['*', 'italic'],
  ['_', 'italic'],
  ['`', 'code'],
]

// WORD_CHAR matches the characters that make an underscore "intraword".
const WORD_CHAR = /[\p{L}\p{N}]/u

function isWordChar(ch: string | undefined): boolean {
  return ch !== undefined && WORD_CHAR.test(ch)
}

// findDelimiter returns the index of the next usable `delim` in `text` at or
// after `from`, or -1. For `_` it follows CommonMark's intraword rule: an
// underscore with a letter or digit on the relevant side neither opens nor
// closes emphasis, so identifiers such as denied_by_policy or
// relay.run_operation render verbatim instead of losing their underscores to
// italics. The other delimiters keep their plain first-occurrence behaviour.
function findDelimiter(text: string, delim: string, from: number, role: 'open' | 'close'): number {
  if (delim !== '_') return text.indexOf(delim, from)
  for (let i = text.indexOf('_', from); i !== -1; i = text.indexOf('_', i + 1)) {
    const neighbour = role === 'open' ? text[i - 1] : text[i + 1]
    if (!isWordChar(neighbour)) return i
  }
  return -1
}

// Tokenize a single line. Scans for the earliest delimiter and, if a closing
// delimiter exists on the same line, emits the corresponding token. Falls
// through to plain text when no pair is found.
function tokenizeLine(line: string): Token[] {
  const tokens: Token[] = []
  let rest = line

  while (rest.length > 0) {
    let earliest = -1
    let matched: [string, 'bold' | 'italic' | 'code'] | null = null

    for (const [delim, kind] of DELIMITERS) {
      const idx = findDelimiter(rest, delim, 0, 'open')
      if (idx !== -1 && (earliest === -1 || idx < earliest)) {
        earliest = idx
        matched = [delim, kind]
      }
    }

    if (matched === null || earliest === -1) {
      tokens.push({ kind: 'text', value: rest })
      break
    }

    const [delim, kind] = matched
    const afterOpen = earliest + delim.length
    const closeIdx = findDelimiter(rest, delim, afterOpen, 'close')

    if (closeIdx === -1) {
      // No closing delimiter — emit everything up to and including the opener
      // literally and continue from after the opener.
      tokens.push({ kind: 'text', value: rest.slice(0, afterOpen) })
      rest = rest.slice(afterOpen)
      continue
    }

    // Emit any text before the opener
    if (earliest > 0) {
      tokens.push({ kind: 'text', value: rest.slice(0, earliest) })
    }

    const inner = rest.slice(afterOpen, closeIdx)
    rest = rest.slice(closeIdx + delim.length)

    if (kind === 'code') {
      tokens.push({ kind: 'code', value: inner })
    } else {
      // Bold / italic: recurse so nested inline tokens are honoured
      tokens.push({ kind, children: tokenizeLine(inner) })
    }
  }

  return tokens
}

function tokensToNodes(tokens: Token[], keyPrefix: string): ReactNode[] {
  return tokens.map((token, i) => {
    const key = `${keyPrefix}-${i}`
    if (token.kind === 'text') {
      return token.value
    }
    if (token.kind === 'code') {
      return createElement('code', { key }, token.value)
    }
    if (token.kind === 'bold') {
      return createElement('strong', { key }, ...tokensToNodes(token.children, key))
    }
    // italic
    return createElement('em', { key }, ...tokensToNodes(token.children, key))
  })
}

// Render a plain string with inline markdown (bold, italic, code) into an
// array of React nodes. Newlines between lines are preserved as literal '\n'
// strings so that a container with white-space: pre-wrap renders them correctly.
export function renderInlineMarkdown(text: string): ReactNode[] {
  const lines = text.split('\n')
  const nodes: ReactNode[] = []

  lines.forEach((line, lineIdx) => {
    const lineNodes = tokensToNodes(tokenizeLine(line), `l${lineIdx}`)
    nodes.push(...lineNodes)
    if (lineIdx < lines.length - 1) {
      nodes.push('\n')
    }
  })

  return nodes
}
