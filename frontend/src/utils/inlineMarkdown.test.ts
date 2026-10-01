import type React from 'react'
import { describe, it, expect } from 'vitest'
import { renderInlineMarkdown } from './inlineMarkdown'

// Helper: flatten the ReactNode[] returned by renderInlineMarkdown into a
// plain string representation for easy assertion. React elements are
// represented as <tag>content</tag>.
function stringify(nodes: ReturnType<typeof renderInlineMarkdown>): string {
  return nodes
    .map((node) => {
      if (typeof node === 'string') return node
      if (typeof node === 'number') return String(node)
      if (node === null || node === undefined || typeof node === 'boolean') return ''
      // ReactElement
      const el = node as React.ReactElement<{ children?: unknown }>
      const tag = el.type as string
      const children = el.props.children
      const childStr = Array.isArray(children)
        ? children
            .map((c: unknown) =>
              typeof c === 'string' ? c : stringify([c as ReturnType<typeof renderInlineMarkdown>[number]]),
            )
            .join('')
        : typeof children === 'string'
          ? children
          : stringify([children as ReturnType<typeof renderInlineMarkdown>[number]])
      return `<${tag}>${childStr}</${tag}>`
    })
    .join('')
}

describe('renderInlineMarkdown', () => {
  it('plain text — no change', () => {
    expect(stringify(renderInlineMarkdown('Hello world'))).toBe('Hello world')
  })

  it('empty string', () => {
    expect(stringify(renderInlineMarkdown(''))).toBe('')
  })

  it('**bold** produces <strong>', () => {
    expect(stringify(renderInlineMarkdown('**Result**'))).toBe('<strong>Result</strong>')
  })

  it('*italic* produces <em>', () => {
    expect(stringify(renderInlineMarkdown('*italic*'))).toBe('<em>italic</em>')
  })

  it('_italic_ produces <em>', () => {
    expect(stringify(renderInlineMarkdown('_italic_'))).toBe('<em>italic</em>')
  })

  it.each([
    ['a snake_case identifier', 'outcome denied_by_policy on 2 Nodes', 'outcome denied_by_policy on 2 Nodes'],
    ['two identifiers on one line', 'relay.run_operation then relay.list_nodes', 'relay.run_operation then relay.list_nodes'],
    ['an identifier inside bold', '**denied_by_policy** (2)', '<strong>denied_by_policy</strong> (2)'],
    ['a trailing underscore after a word', 'snake_ and _case', 'snake_ and _case'],
    ['digits around the underscore', 'exit_code 0 at 12_000ms', 'exit_code 0 at 12_000ms'],
  ])('leaves intraword underscores literal: %s', (_label, input, expected) => {
    expect(stringify(renderInlineMarkdown(input))).toBe(expected)
  })

  it('still italicizes _word_ next to a snake_case identifier', () => {
    expect(stringify(renderInlineMarkdown('_note_ on denied_by_policy'))).toBe(
      '<em>note</em> on denied_by_policy',
    )
  })

  it('`code` produces <code>', () => {
    expect(stringify(renderInlineMarkdown('`foo`'))).toBe('<code>foo</code>')
  })

  it('adjacent markers on one line', () => {
    const result = stringify(renderInlineMarkdown('**bold** and *italic* and `code`'))
    expect(result).toBe('<strong>bold</strong> and <em>italic</em> and <code>code</code>')
  })

  it('unmatched opener stays literal', () => {
    expect(stringify(renderInlineMarkdown('**foo'))).toBe('**foo')
  })

  it('unmatched * stays literal', () => {
    expect(stringify(renderInlineMarkdown('*foo'))).toBe('*foo')
  })

  it('newlines preserved between lines', () => {
    const result = stringify(renderInlineMarkdown('line one\nline two'))
    expect(result).toBe('line one\nline two')
  })

  it('mixed content with newlines', () => {
    const result = stringify(renderInlineMarkdown('**Result:** The `foo` function returned *success*.'))
    expect(result).toBe('<strong>Result:</strong> The <code>foo</code> function returned <em>success</em>.')
  })

  it('text before and after a bold span', () => {
    const result = stringify(renderInlineMarkdown('before **mid** after'))
    expect(result).toBe('before <strong>mid</strong> after')
  })

  it('multiple lines each with markup', () => {
    const result = stringify(renderInlineMarkdown('**line1**\n*line2*'))
    expect(result).toBe('<strong>line1</strong>\n<em>line2</em>')
  })
})
