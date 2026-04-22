import { createElement } from 'react'
import type { ReactNode } from 'react'

// Inline markdown tokenizer supporting bold (**), italic (* and _), and inline code (`).
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
      const idx = rest.indexOf(delim)
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
    const closeIdx = rest.indexOf(delim, afterOpen)

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
