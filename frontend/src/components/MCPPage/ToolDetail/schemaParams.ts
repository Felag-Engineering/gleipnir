// Flattens a tool's JSON Schema input into rows the tool detail view can show.
//
// The schema comes from the MCP server, so every string here is
// server-controlled: callers render the values as plain text nodes only.

export interface ParsedParam {
  /** Dotted path from the root, e.g. "options.limit". `[]` marks array items. */
  path: string
  /** Nesting depth (0 = top-level property), used for indentation. */
  depth: number
  /** Human-readable type, e.g. "string", "array of string", "string | number". */
  type: string
  required: boolean
  description?: string
  /** Allowed values from `enum` or a single `const`, already stringified. */
  allowedValues?: string[]
  /** `default`, stringified. */
  defaultValue?: string
  /** Other constraints worth knowing before writing a policy, e.g. "max 100". */
  constraints: string[]
}

export interface ParsedSchema {
  params: ParsedParam[]
  /**
   * Set when the root uses a combinator (oneOf/anyOf/allOf) or a $ref instead
   * of plain `properties`. The params list is then incomplete, and the raw
   * schema is the only faithful view.
   */
  combinator?: string
}

// Nested objects past this depth are summarized by their type only. Deep
// schemas are rare in tool inputs, and a cap keeps a pathological or
// self-referential-looking schema from producing an unreadable wall of rows.
const MAX_DEPTH = 3

type Schema = Record<string, unknown>

function isSchema(v: unknown): v is Schema {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function stringify(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

function typeLabel(schema: Schema): string {
  const t = schema.type
  if (Array.isArray(t)) return t.map(String).join(' | ')
  if (t === 'array') {
    const items = isSchema(schema.items) ? schema.items : undefined
    return items ? `array of ${typeLabel(items)}` : 'array'
  }
  if (typeof t === 'string') return t
  for (const key of ['oneOf', 'anyOf'] as const) {
    const options = schema[key]
    if (Array.isArray(options) && options.length > 0) {
      return options.filter(isSchema).map(typeLabel).join(' | ')
    }
  }
  if ('enum' in schema || 'const' in schema) return 'enum'
  if (typeof schema.$ref === 'string') return 'reference'
  return 'any'
}

function constraintsOf(schema: Schema): string[] {
  const out: string[] = []
  const num = (key: string) => (typeof schema[key] === 'number' ? (schema[key] as number) : undefined)

  if (typeof schema.format === 'string') out.push(`format: ${schema.format}`)
  if (typeof schema.pattern === 'string') out.push(`pattern: ${schema.pattern}`)

  const pairs: Array<[string, string, string]> = [
    ['minimum', 'maximum', ''],
    ['minLength', 'maxLength', ' chars'],
    ['minItems', 'maxItems', ' items'],
  ]
  for (const [minKey, maxKey, unit] of pairs) {
    const min = num(minKey)
    const max = num(maxKey)
    if (min !== undefined && max !== undefined) out.push(`${min}–${max}${unit}`)
    else if (min !== undefined) out.push(`min ${min}${unit}`)
    else if (max !== undefined) out.push(`max ${max}${unit}`)
  }
  const exMin = num('exclusiveMinimum')
  const exMax = num('exclusiveMaximum')
  if (exMin !== undefined) out.push(`> ${exMin}`)
  if (exMax !== undefined) out.push(`< ${exMax}`)
  if (schema.uniqueItems === true) out.push('unique items')
  return out
}

function walk(properties: Schema, required: string[], prefix: string, depth: number, out: ParsedParam[]) {
  for (const [name, raw] of Object.entries(properties)) {
    const prop = isSchema(raw) ? raw : {}
    const path = prefix ? `${prefix}.${name}` : name

    let allowedValues: string[] | undefined
    if (Array.isArray(prop.enum)) allowedValues = prop.enum.map(stringify)
    else if ('const' in prop) allowedValues = [stringify(prop.const)]

    out.push({
      path,
      depth,
      type: typeLabel(prop),
      required: required.includes(name),
      description: typeof prop.description === 'string' && prop.description.trim() ? prop.description : undefined,
      allowedValues,
      defaultValue: 'default' in prop ? stringify(prop.default) : undefined,
      constraints: constraintsOf(prop),
    })

    if (depth + 1 >= MAX_DEPTH) continue

    // Recurse into an object's properties, or an array's item properties, so
    // a nested field's description is not hidden behind "object".
    const child = prop.type === 'array' && isSchema(prop.items) ? prop.items : prop
    const childPath = child === prop ? path : `${path}[]`
    if (isSchema(child.properties)) {
      const childRequired = Array.isArray(child.required) ? child.required.map(String) : []
      walk(child.properties, childRequired, childPath, depth + 1, out)
    }
  }
}

export function parseSchema(schema: Record<string, unknown>): ParsedSchema {
  const params: ParsedParam[] = []
  if (isSchema(schema.properties)) {
    const required = Array.isArray(schema.required) ? schema.required.map(String) : []
    walk(schema.properties, required, '', 0, params)
  }

  let combinator: string | undefined
  for (const key of ['oneOf', 'anyOf', 'allOf', '$ref']) {
    if (key in schema) {
      combinator = key
      break
    }
  }
  return { params, combinator }
}

/** Number of top-level parameters, for list summaries. */
export function topLevelParamCount(schema: Record<string, unknown>): number {
  return isSchema(schema.properties) ? Object.keys(schema.properties).length : 0
}
