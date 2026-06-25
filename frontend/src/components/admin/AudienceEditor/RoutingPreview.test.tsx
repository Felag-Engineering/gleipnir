import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { RoutingPreview, entryDisplayName } from './RoutingPreview'
import type { ApiAudienceEntry, ApiPluginInstanceForAudience } from '@/api/types'

// --- Fixtures ---

const ULID = '01KVXVCGQNH5WGDAXSKXZRQ7KP'

const PLUGIN_INSTANCES: ApiPluginInstanceForAudience[] = [
  {
    id: ULID,
    plugin_id: 'com.example.slack',
    instance_name: 'slack-e2e',
    state: 'healthy',
    implements_notify: true,
    implements_request: false,
    config_schema: null,
    version: 1,
  },
]

function makeNotifyEntry(pluginInstanceId: string): ApiAudienceEntry {
  return {
    id: 'e1',
    plugin_instance_id: pluginInstanceId,
    position: 0,
    notify: true,
    request: false,
    config: {},
  }
}

const AUTO_ENTRY: ApiAudienceEntry = {
  id: '__auto__',
  plugin_instance_id: '',
  position: 99,
  notify: true,
  request: true,
  config: {},
  auto: true,
}

// --- entryDisplayName unit tests ---

describe('entryDisplayName', () => {
  it('returns instance_name when plugin_instance_id matches a pluginInstances entry', () => {
    const entry = makeNotifyEntry(ULID)
    expect(entryDisplayName(entry, PLUGIN_INSTANCES)).toBe('slack-e2e')
  })

  it('returns the raw ULID when no pluginInstances list is provided', () => {
    const entry = makeNotifyEntry(ULID)
    expect(entryDisplayName(entry)).toBe(ULID)
  })

  it('returns the raw ULID when no match is found in pluginInstances', () => {
    const entry = makeNotifyEntry(ULID)
    expect(entryDisplayName(entry, [])).toBe(ULID)
  })

  it('returns "(unset)" for an empty plugin_instance_id', () => {
    const entry = makeNotifyEntry('')
    expect(entryDisplayName(entry, PLUGIN_INSTANCES)).toBe('(unset)')
  })

  it('returns the built-in fallback label for auto entries', () => {
    expect(entryDisplayName(AUTO_ENTRY, PLUGIN_INSTANCES)).toBe(
      'gleipnir.in-app (built-in fallback)',
    )
  })
})

// --- RoutingPreview rendering tests ---

describe('RoutingPreview — name resolution', () => {
  it('renders instance_name when plugin_instance_id matches a pluginInstances entry', () => {
    render(
      <RoutingPreview
        entries={[makeNotifyEntry(ULID)]}
        disableInAppFallback={true}
        pluginInstances={PLUGIN_INSTANCES}
      />,
    )
    expect(screen.getByText(/slack-e2e/)).toBeInTheDocument()
    expect(screen.queryByText(ULID)).not.toBeInTheDocument()
  })

  it('renders the raw ULID when no pluginInstances list is provided', () => {
    render(
      <RoutingPreview
        entries={[makeNotifyEntry(ULID)]}
        disableInAppFallback={true}
      />,
    )
    expect(screen.getByText(new RegExp(ULID))).toBeInTheDocument()
  })

  it('renders the raw ULID when no match is found in pluginInstances', () => {
    render(
      <RoutingPreview
        entries={[makeNotifyEntry(ULID)]}
        disableInAppFallback={true}
        pluginInstances={[]}
      />,
    )
    expect(screen.getByText(new RegExp(ULID))).toBeInTheDocument()
  })

  it('renders "(unset)" for an entry with an empty plugin_instance_id', () => {
    render(
      <RoutingPreview
        entries={[makeNotifyEntry('')]}
        disableInAppFallback={true}
        pluginInstances={PLUGIN_INSTANCES}
      />,
    )
    expect(screen.getByText(/\(unset\)/)).toBeInTheDocument()
  })

  it('renders the built-in fallback label for auto entries', () => {
    render(
      <RoutingPreview
        entries={[AUTO_ENTRY]}
        disableInAppFallback={false}
        pluginInstances={PLUGIN_INSTANCES}
      />,
    )
    expect(screen.getAllByText(/gleipnir\.in-app \(built-in fallback\)/).length).toBeGreaterThan(0)
  })
})
