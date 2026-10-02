import { describe, it, expect } from 'vitest'
import { parseSchema, topLevelParamCount } from './schemaParams'

describe('parseSchema', () => {
  it('returns no params for a schema without properties', () => {
    expect(parseSchema({ type: 'object' })).toEqual({ params: [], combinator: undefined })
    expect(parseSchema({})).toEqual({ params: [], combinator: undefined })
  })

  it('reads type, required, description, allowed values, default and limits', () => {
    const { params } = parseSchema({
      type: 'object',
      required: ['command'],
      properties: {
        command: { type: 'string', description: 'What to run.', minLength: 1, maxLength: 10 },
        shell: { type: 'string', enum: ['sh', 'bash'], default: 'sh' },
        retries: { type: 'integer', minimum: 0 },
        ratio: { type: 'number', exclusiveMaximum: 1 },
        mode: { const: 'fast' },
        when: { type: 'string', format: 'date-time', pattern: '^2' },
      },
    })

    expect(params).toEqual([
      { path: 'command', depth: 0, type: 'string', required: true, description: 'What to run.',
        allowedValues: undefined, defaultValue: undefined, constraints: ['1–10 chars'] },
      { path: 'shell', depth: 0, type: 'string', required: false, description: undefined,
        allowedValues: ['sh', 'bash'], defaultValue: 'sh', constraints: [] },
      { path: 'retries', depth: 0, type: 'integer', required: false, description: undefined,
        allowedValues: undefined, defaultValue: undefined, constraints: ['min 0'] },
      { path: 'ratio', depth: 0, type: 'number', required: false, description: undefined,
        allowedValues: undefined, defaultValue: undefined, constraints: ['< 1'] },
      { path: 'mode', depth: 0, type: 'enum', required: false, description: undefined,
        allowedValues: ['fast'], defaultValue: undefined, constraints: [] },
      { path: 'when', depth: 0, type: 'string', required: false, description: undefined,
        allowedValues: undefined, defaultValue: undefined, constraints: ['format: date-time', 'pattern: ^2'] },
    ])
  })

  it('labels array, union and untyped properties', () => {
    const { params } = parseSchema({
      properties: {
        tags: { type: 'array', items: { type: 'string' }, maxItems: 5, uniqueItems: true },
        bare: { type: 'array' },
        id: { type: ['string', 'integer'] },
        either: { anyOf: [{ type: 'string' }, { type: 'null' }] },
        anything: {},
        ref: { $ref: '#/$defs/Thing' },
      },
    })
    const byPath = Object.fromEntries(params.map((p) => [p.path, p]))

    expect(byPath.tags.type).toBe('array of string')
    expect(byPath.tags.constraints).toEqual(['max 5 items', 'unique items'])
    expect(byPath.bare.type).toBe('array')
    expect(byPath.id.type).toBe('string | integer')
    expect(byPath.either.type).toBe('string | null')
    expect(byPath.anything.type).toBe('any')
    expect(byPath.ref.type).toBe('reference')
  })

  it('stringifies non-string enum and default values', () => {
    const { params } = parseSchema({
      properties: { level: { type: 'integer', enum: [1, 2], default: 1 }, flags: { type: 'object', default: { a: true } } },
    })
    expect(params[0].allowedValues).toEqual(['1', '2'])
    expect(params[0].defaultValue).toBe('1')
    expect(params[1].defaultValue).toBe('{"a":true}')
  })

  it('flattens nested object and array-item properties with dotted paths', () => {
    const { params } = parseSchema({
      properties: {
        options: {
          type: 'object',
          required: ['dir'],
          properties: { dir: { type: 'string', description: 'Working directory.' } },
        },
        hosts: {
          type: 'array',
          items: { type: 'object', properties: { name: { type: 'string' } } },
        },
      },
    })

    expect(params.map((p) => [p.path, p.depth, p.required])).toEqual([
      ['options', 0, false],
      ['options.dir', 1, true],
      ['hosts', 0, false],
      ['hosts[].name', 1, false],
    ])
    expect(params[1].description).toBe('Working directory.')
  })

  it('stops descending past the depth cap', () => {
    const deep = {
      properties: {
        a: { type: 'object', properties: { b: { type: 'object', properties: { c: { type: 'object', properties: { d: { type: 'string' } } } } } } },
      },
    }
    expect(parseSchema(deep).params.map((p) => p.path)).toEqual(['a', 'a.b', 'a.b.c'])
  })

  it('ignores a blank description', () => {
    const { params } = parseSchema({ properties: { x: { type: 'string', description: '   ' } } })
    expect(params[0].description).toBeUndefined()
  })

  it.each(['oneOf', 'anyOf', 'allOf', '$ref'])('reports a root %s as a combinator', (key) => {
    const value = key === '$ref' ? '#/$defs/Input' : [{ type: 'object' }]
    expect(parseSchema({ [key]: value }).combinator).toBe(key)
  })
})

describe('topLevelParamCount', () => {
  it('counts only top-level properties', () => {
    expect(topLevelParamCount({ properties: { a: {}, b: { type: 'object', properties: { c: {} } } } })).toBe(2)
    expect(topLevelParamCount({})).toBe(0)
  })
})
