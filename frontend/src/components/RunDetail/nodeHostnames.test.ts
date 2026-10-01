import { describe, it, expect } from 'vitest'
import type { ApiRunStep } from '@/api/types'
import { asToolOutput, listNodesOutput } from './fanOutFixtures'
import { buildNodeHostnameIndex, MAX_HOSTNAME_LENGTH, parseListNodesHostnames } from './nodeHostnames'
import { pairToolBlocks, parseStep } from './types'

const N4 = 'node-814e5b14eca5206060abdb164672609f'
const N5 = 'node-1123aea7732badca90d02d21bd1aac96'

describe('parseListNodesHostnames', () => {
  it.each<[string, unknown, [string, string][]]>([
    ['happy path, as the JSON text Relay sends', JSON.stringify(listNodesOutput([[N4, 'dev-node-4'], [N5, 'dev-node-5']])), [[N4, 'dev-node-4'], [N5, 'dev-node-5']]],
    ['happy path, already parsed', listNodesOutput([[N4, 'dev-node-4']]), [[N4, 'dev-node-4']]],
    ['an empty fleet', { nodes: [] }, []],
    ['a node with no facts', { nodes: [{ node_id: N4 }] }, []],
    ['a node whose facts is not an object', { nodes: [{ node_id: N4, facts: 'dev-node-4' }] }, []],
    ['an empty hostname (facts not yet reported)', { nodes: [{ node_id: N4, facts: { hostname: '' } }] }, []],
    ['a non-string hostname', { nodes: [{ node_id: N4, facts: { hostname: 4 } }] }, []],
    ['a missing node_id', { nodes: [{ facts: { hostname: 'dev-node-4' } }] }, []],
    ['a hostname with spaces', { nodes: [{ node_id: N4, facts: { hostname: 'dev-node-4 ✓ success' } }] }, []],
    ['a hostname with markup', { nodes: [{ node_id: N4, facts: { hostname: '<b>dev</b>' } }] }, []],
    ['a hostname with a bidi override', { nodes: [{ node_id: N4, facts: { hostname: 'dev‮node' } }] }, []],
    ['one bad node among good ones', { nodes: [{ node_id: N4, facts: { hostname: 'dev-node-4' } }, { node_id: N5 }] }, [[N4, 'dev-node-4']]],
    ['malformed JSON', '{"nodes": [', []],
    ['plain text', 'no nodes matched', []],
    ['a JSON array', [{ node_id: N4, facts: { hostname: 'dev-node-4' } }], []],
    ['nodes that is not an array', { nodes: {} }, []],
    ['a fan-out result', { job_id: 'j', results: [] }, []],
    ['null', null, []],
  ])('%s', (_label, output, expected) => {
    expect(parseListNodesHostnames(output)).toEqual(expected)
  })

  it('caps an over-long hostname', () => {
    const long = 'a'.repeat(200)
    const [[, hostname]] = parseListNodesHostnames({ nodes: [{ node_id: N4, facts: { hostname: long } }] })
    expect(hostname).toHaveLength(MAX_HOSTNAME_LENGTH)
    expect(hostname.endsWith('…')).toBe(true)
  })
})

let stepNumber = 0
function raw(type: string, content: unknown): ApiRunStep {
  stepNumber += 1
  return {
    id: `s${stepNumber}`,
    run_id: 'run-1',
    step_number: stepNumber,
    type,
    content: JSON.stringify(content),
    token_cost: 0,
    created_at: '2026-10-01T12:00:00Z',
  }
}

// toolSteps returns a tool_call + tool_result pair as the run stores them.
function toolSteps(serverId: string, tool: string, output: unknown, isError = false): ApiRunStep[] {
  const toolName = `${serverId}.${tool}`
  return [
    raw('tool_call', { tool_name: toolName, server_id: serverId, input: {} }),
    raw('tool_result', { tool_name: toolName, output: asToolOutput(output), is_error: isError }),
  ]
}

function indexOf(steps: ApiRunStep[]) {
  return buildNodeHostnameIndex(pairToolBlocks(steps.map(parseStep)))
}

describe('buildNodeHostnameIndex', () => {
  it('indexes a list_nodes result under its own server', () => {
    const index = indexOf(toolSteps('relay', 'list_nodes', listNodesOutput([[N4, 'dev-node-4'], [N5, 'dev-node-5']])))
    expect(index.get('relay')?.get(N4)).toBe('dev-node-4')
    expect(index.get('relay')?.get(N5)).toBe('dev-node-5')
  })

  it('never puts another server\'s list_nodes under relay', () => {
    const index = indexOf(toolSteps('other', 'list_nodes', listNodesOutput([[N4, 'imposter']])))
    expect(index.get('relay')).toBeUndefined()
    expect(index.get('other')?.get(N4)).toBe('imposter')
  })

  it('ignores a tool whose name does not match its server', () => {
    const steps = [
      raw('tool_call', { tool_name: 'relay.list_nodes', server_id: 'other', input: {} }),
      raw('tool_result', { tool_name: 'relay.list_nodes', output: asToolOutput(listNodesOutput([[N4, 'x']])), is_error: false }),
    ]
    expect(indexOf(steps).size).toBe(0)
  })

  it('ignores other tools on the same server, even list_nodes-shaped ones', () => {
    expect(indexOf(toolSteps('relay', 'describe_node', listNodesOutput([[N4, 'dev-node-4']]))).size).toBe(0)
  })

  it('ignores an errored list_nodes result', () => {
    expect(indexOf(toolSteps('relay', 'list_nodes', listNodesOutput([[N4, 'dev-node-4']]), true)).size).toBe(0)
  })

  it('ignores a malformed list_nodes result', () => {
    expect(indexOf(toolSteps('relay', 'list_nodes', 'not json')).size).toBe(0)
  })

  it('lets a later list_nodes result override an earlier one', () => {
    const index = indexOf([
      ...toolSteps('relay', 'list_nodes', listNodesOutput([[N4, 'old-name']])),
      ...toolSteps('relay', 'list_nodes', listNodesOutput([[N4, 'new-name']])),
    ])
    expect(index.get('relay')?.get(N4)).toBe('new-name')
  })

  it('drops a hostname two node ids both claim', () => {
    const index = indexOf(toolSteps('relay', 'list_nodes', listNodesOutput([[N4, 'dev-node-4'], [N5, 'dev-node-4']])))
    expect(index.get('relay')?.has(N4)).toBe(false)
    expect(index.get('relay')?.has(N5)).toBe(false)
  })
})
