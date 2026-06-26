import { useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import { useMcpServers } from '@/hooks/queries/servers';
import { usePluginInstancesForAudience } from '@/hooks/queries/admin';
import { queryKeys } from '@/hooks/queryKeys';
import { apiFetch } from '@/api/fetch';
import type { ApiMcpTool } from '@/api/types';
import type { AssignedTool, CapabilitiesFormState, SectionIssues, FormIssue } from './types';
import shared from './FormSections.module.css';
import styles from './CapabilitiesSection.module.css';
import { FieldError } from '@/components/form/FieldError';

export interface CapabilitiesSectionProps {
  value: CapabilitiesFormState;
  onChange: (next: CapabilitiesFormState) => void;
  errors?: SectionIssues;
}

// RegistryEntry discriminates between MCP tools and plugin tools in the picker.
type McpRegistryEntry = { kind: 'mcp'; tool: ApiMcpTool; serverName: string };
type PluginRegistryEntry = {
  kind: 'plugin';
  instanceName: string;
  instanceId: string;
  name: string;
  description: string;
};
type RegistryEntry = McpRegistryEntry | PluginRegistryEntry;

// Stable per-entry display values, computed once for filtering and rendering.
interface EntryDisplay {
  displayId: string;       // used for dedup: UUID for MCP, dot-name for plugin
  displayName: string;     // e.g. "Filesystem Tools.read_file" or "slack-e2e.send_message"
  description: string;
  sourceLabel: string;     // e.g. "mcp:Filesystem Tools" or "plugin:slack-e2e"
  entry: RegistryEntry;
}

function buildDisplay(e: RegistryEntry): EntryDisplay {
  if (e.kind === 'mcp') {
    return {
      displayId: e.tool.id,
      displayName: `${e.serverName}.${e.tool.name}`,
      description: e.tool.description,
      sourceLabel: `mcp:${e.serverName}`,
      entry: e,
    };
  }
  const dotName = `${e.instanceName}.${e.name}`;
  return {
    displayId: dotName,
    displayName: dotName,
    description: e.description,
    sourceLabel: `plugin:${e.instanceName}`,
    entry: e,
  };
}

export function CapabilitiesSection({ value, onChange, errors = [] }: CapabilitiesSectionProps) {
  const capabilityRootErrors = errors.filter(e => e.field === 'capabilities').map(e => e.message);
  const feedbackTimeoutErrors = errors.filter(e => e.field === 'capabilities.feedback.timeout').map(e => e.message);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');

  const { data: servers } = useMcpServers();
  const { data: pluginInstances } = usePluginInstancesForAudience();

  const toolQueries = useQueries({
    queries: (servers ?? []).map(s => ({
      queryKey: queryKeys.servers.toolsAll(s.id),
      queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(s.id)}/tools?include_disabled=true`),
      enabled: Boolean(s.id),
    })),
  });

  // Build MCP registry entries from all discovered tools.
  const mcpEntries: McpRegistryEntry[] = (servers ?? []).flatMap((srv, i) =>
    (toolQueries[i]?.data ?? []).map(tool => ({ kind: 'mcp' as const, tool, serverName: srv.name }))
  );

  // Build plugin registry entries from every instance that declares tools.
  // Channel/trigger-only instances contribute no rows (inst.tools is absent or empty).
  const pluginEntries: PluginRegistryEntry[] = (pluginInstances ?? []).flatMap(inst =>
    (inst.tools ?? []).map(t => ({
      kind: 'plugin' as const,
      instanceName: inst.instance_name,
      instanceId: inst.id,
      name: t.name,
      description: t.description,
    }))
  );

  // Build the set of identifiers for disabled MCP tools so assigned-tool rows
  // can show a warning badge. Each disabled tool adds two entries: the UUID
  // (used when the tool was added through the picker in this session) and the
  // dot-notation composite key "serverName.toolName" (used when the tool was
  // parsed from an existing policy YAML via yamlToFormState).
  // Plugin tools have no enable/disable gate, so disabledToolIds is MCP-only.
  const disabledToolIds = new Set(
    mcpEntries
      .filter(({ tool }) => !tool.enabled)
      .flatMap(({ tool, serverName }) => [tool.id, `${serverName}.${tool.name}`])
  );

  // Build the set of known plugin instance names for source reconciliation.
  // A granted tool whose serverName matches → show plugin label at render.
  const knownPluginInstanceNames = new Set(
    (pluginInstances ?? []).map(inst => inst.instance_name)
  );

  // Build display entries for the full combined registry.
  const allDisplayEntries: EntryDisplay[] = [
    ...mcpEntries.map(buildDisplay),
    ...pluginEntries.map(buildDisplay),
  ];

  // assignedIds tracks tools already in the list for dedup.
  // MCP tools can be assigned via UUID (picker) OR dot-name (YAML round-trip),
  // so we add both forms. Plugin entries dedup by dot-name only.
  const assignedIds = new Set<string>();
  for (const t of value.tools) {
    assignedIds.add(t.toolId);           // dot-name for YAML-loaded; UUID for picker-added MCP
    // Also add the dot-name form so YAML-loaded MCP grants suppress the tool from the picker.
    assignedIds.add(`${t.serverName}.${t.name}`);
  }

  const q = query.toLowerCase().trim();
  const filteredDisplay = allDisplayEntries.filter(d => {
    // Exclude tools already in the assigned list (by UUID or dot-name).
    if (assignedIds.has(d.displayId)) return false;
    if (!q) return true;
    return (
      d.displayName.toLowerCase().includes(q) ||
      d.sourceLabel.toLowerCase().includes(q) ||
      d.description.toLowerCase().includes(q)
    );
  });

  function handleRemove(toolId: string) {
    onChange({ ...value, tools: value.tools.filter(t => t.toolId !== toolId) });
  }

  // handleToggleApproval intentionally does NOT reset approvalTimeout when toggling
  // off. The value is preserved in state but omitted from YAML when approval is off
  // (see formStateToYaml). This lets users toggle approval on/off without losing
  // a timeout they typed.
  function handleToggleApproval(toolId: string) {
    onChange({
      ...value,
      tools: value.tools.map(t =>
        t.toolId === toolId ? { ...t, approvalRequired: !t.approvalRequired } : t
      ),
    });
  }

  function handleTimeoutChange(toolId: string, timeout: string) {
    onChange({
      ...value,
      tools: value.tools.map(t =>
        t.toolId === toolId ? { ...t, approvalTimeout: timeout } : t
      ),
    });
  }

  function handleAddEntry(d: EntryDisplay) {
    let assigned: AssignedTool;
    if (d.entry.kind === 'mcp') {
      const { tool, serverName } = d.entry;
      assigned = {
        toolId: tool.id,
        serverId: tool.server_id,
        serverName,
        name: tool.name,
        description: tool.description,
        source: 'mcp',
        approvalRequired: false,
        approvalTimeout: '',
      };
    } else {
      const { instanceName, instanceId, name, description } = d.entry;
      assigned = {
        toolId: `${instanceName}.${name}`,
        serverId: instanceId,
        serverName: instanceName,
        name,
        description,
        source: 'plugin',
        approvalRequired: false,
        approvalTimeout: '',
      };
    }
    onChange({ ...value, tools: [...value.tools, assigned] });
    setSearchOpen(false);
    setQuery('');
  }

  function handleFeedbackToggle() {
    onChange({
      ...value,
      feedback: { ...value.feedback, enabled: !value.feedback.enabled },
    });
  }

  function handleFeedbackTimeoutChange(timeout: string) {
    onChange({
      ...value,
      feedback: { ...value.feedback, timeout },
    });
  }

  // Resolve source label for a granted tool.
  // Parse-time grants always arrive with source:'mcp' but may actually be plugin
  // tools — reconcile by checking the live plugin instance list.
  function resolveSourceLabel(tool: AssignedTool): string {
    // Tools added through the plugin picker are explicitly tagged.
    if (tool.source === 'plugin') {
      return `plugin:${tool.serverName}`;
    }
    // A parse-time grant always arrives as source:'mcp', so reconcile against the
    // live lists. Prefer the MCP label on the (runtime-impossible) name collision
    // where an MCP server and a plugin instance share a name — toolregistry rejects
    // such a namespace conflict, so this only governs the cosmetic label.
    const isMcpServer = (servers ?? []).some(s => s.name === tool.serverName);
    if (isMcpServer) {
      return `mcp:${tool.serverName}`;
    }
    if (knownPluginInstanceNames.has(tool.serverName)) {
      return `plugin:${tool.serverName}`;
    }
    // serverName matches neither a known MCP server nor a known plugin instance.
    // This can happen when an MCP server was renamed after the grant was saved
    // (name-based reconciliation limitation) or after a plugin is uninstalled.
    return '';
  }

  return (
    <div className={shared.section}>
      <div className={shared.heading}>Capabilities</div>

      <FieldError messages={capabilityRootErrors} />
      {value.tools.length === 0 ? (
        <div className={styles.emptyState}>
          No tools added yet. Add tools from the registry below.
        </div>
      ) : (
        <div className={styles.toolList}>
          {value.tools.map((tool, i) => {
            const rowIssues = errors.filter(e => e.field.startsWith(`capabilities.tools[${i}].`));
            const sourceLabel = resolveSourceLabel(tool);
            const isDisabled = disabledToolIds.has(tool.toolId);
            return (
              <AssignedToolRow
                key={tool.toolId}
                tool={tool}
                rowIndex={i}
                rowIssues={rowIssues}
                isDisabled={isDisabled}
                sourceLabel={sourceLabel}
                onRemove={handleRemove}
                onToggleApproval={handleToggleApproval}
                onTimeoutChange={handleTimeoutChange}
              />
            );
          })}
        </div>
      )}

      {searchOpen ? (
        <SearchPanel
          query={query}
          onQueryChange={setQuery}
          results={filteredDisplay}
          onAdd={handleAddEntry}
          onClose={() => { setSearchOpen(false); setQuery(''); }}
        />
      ) : (
        <button className={styles.addButton} onClick={() => setSearchOpen(true)}>
          + Add tool from registry
        </button>
      )}

      <div className={styles.feedbackSection}>
        <div className={shared.heading}>Feedback request</div>
        <p className={shared.label}>
          Lets the agent pause and ask a human operator for input. The request is routed to the
          channels in the policy&apos;s audience (the entries with Request enabled).
        </p>
        <div className={styles.feedbackRow}>
          <button
            role="switch"
            aria-checked={value.feedback.enabled}
            className={styles.toggleButton}
            onClick={handleFeedbackToggle}
            title={value.feedback.enabled ? 'Feedback request enabled — click to disable' : 'Feedback request disabled — click to enable'}
          >
            <span className={`${styles.toggleTrack} ${value.feedback.enabled ? styles.toggleTrackOn : styles.toggleTrackOff}`}>
              <span className={`${styles.toggleThumb} ${value.feedback.enabled ? styles.toggleThumbOn : styles.toggleThumbOff}`} />
            </span>
          </button>
          <span className={styles.feedbackLabel}>
            {value.feedback.enabled ? 'Enabled — agent can consult a human operator' : 'Disabled'}
          </span>
        </div>
        {value.feedback.enabled && (
          <div className={styles.feedbackFields}>
            <div className={styles.feedbackRow} data-field="capabilities.feedback.timeout">
              <span className={styles.feedbackLabel}>Timeout</span>
              <input
                className={styles.feedbackInput}
                type="text"
                placeholder="e.g. 30m"
                value={value.feedback.timeout}
                onChange={e => handleFeedbackTimeoutChange(e.target.value)}
              />
            </div>
            <FieldError messages={feedbackTimeoutErrors} />
            <div className={styles.feedbackRow}>
              <span className={styles.feedbackLabel}>On timeout</span>
              <span className={styles.feedbackLabel}>fail</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

interface AssignedToolRowProps {
  tool: AssignedTool;
  rowIndex: number;
  rowIssues: FormIssue[];
  isDisabled: boolean;
  sourceLabel: string; // empty string → unknown source
  onRemove: (toolId: string) => void;
  onToggleApproval: (toolId: string) => void;
  onTimeoutChange: (toolId: string, timeout: string) => void;
}

function AssignedToolRow({ tool, rowIndex, rowIssues, isDisabled, sourceLabel, onRemove, onToggleApproval, onTimeoutChange }: AssignedToolRowProps) {
  const displayName = `${tool.serverName}.${tool.name}`;
  const toolErrors = rowIssues.filter(e => e.field === `capabilities.tools[${rowIndex}].tool`).map(e => e.message);
  const timeoutErrors = rowIssues.filter(e => e.field === `capabilities.tools[${rowIndex}].timeout`).map(e => e.message);

  const isUnknownSource = !sourceLabel;

  return (
    <div className={styles.toolRow} data-field={`capabilities.tools[${rowIndex}].tool`} data-disabled={isDisabled ? 'true' : undefined}>
      <span className={styles.toolName}>{displayName}</span>
      {isDisabled && (
        <span
          className={styles.disabledBadge}
          title="Tool is disabled — runs will fail until it is re-enabled on the Tools page"
        >
          Disabled
        </span>
      )}
      {isUnknownSource ? (
        <span
          className={styles.unknownBadge}
          title="Source server or plugin instance not found — this grant is preserved but cannot be resolved"
        >
          Unknown source
        </span>
      ) : (
        <span className={styles.sourceLabel}>{sourceLabel}</span>
      )}
      <span className={styles.toolDesc}>{tool.description}</span>
      <FieldError messages={toolErrors} />
      <div className={styles.approvalToggle}>
        <span className={styles.approvalLabel}>approval</span>
        <button
          role="switch"
          aria-checked={tool.approvalRequired}
          className={styles.toggleButton}
          onClick={() => onToggleApproval(tool.toolId)}
          title={tool.approvalRequired ? 'Approval required — click to disable' : 'No approval required — click to enable'}
        >
          <span className={`${styles.toggleTrack} ${tool.approvalRequired ? styles.toggleTrackOn : styles.toggleTrackOff}`}>
            <span className={`${styles.toggleThumb} ${tool.approvalRequired ? styles.toggleThumbOn : styles.toggleThumbOff}`} />
          </span>
        </button>
        {tool.approvalRequired && (
          <input
            className={styles.approvalTimeoutInput}
            type="text"
            placeholder="e.g. 30m"
            value={tool.approvalTimeout}
            onChange={e => onTimeoutChange(tool.toolId, e.target.value)}
            aria-label={`Approval timeout for ${displayName}`}
          />
        )}
      </div>
      <FieldError messages={timeoutErrors} />
      <button
        className={styles.removeButton}
        onClick={() => onRemove(tool.toolId)}
        aria-label={`Remove ${displayName}`}
      >
        ×
      </button>
    </div>
  );
}

interface SearchPanelProps {
  query: string;
  onQueryChange: (q: string) => void;
  results: EntryDisplay[];
  onAdd: (d: EntryDisplay) => void;
  onClose: () => void;
}

function SearchPanel({ query, onQueryChange, results, onAdd, onClose }: SearchPanelProps) {
  return (
    <div className={styles.searchPanel}>
      <div className={styles.searchHeader}>
        <input
          className={styles.searchInput}
          type="text"
          placeholder="Filter by tool name, server, or description…"
          value={query}
          onChange={e => onQueryChange(e.target.value)}
          autoFocus
        />
        <button className={styles.cancelButton} onClick={onClose}>
          Cancel
        </button>
      </div>

      <div className={styles.searchResults}>
        {results.length === 0 ? (
          <div className={styles.searchEmpty}>No tools match your search.</div>
        ) : (
          results.map(d => (
            <button
              key={d.displayId}
              className={styles.resultRow}
              onClick={() => onAdd(d)}
            >
              <span className={styles.toolName}>{d.displayName}</span>
              <span className={styles.sourceLabel}>{d.sourceLabel}</span>
              <span className={styles.toolDesc}>{d.description}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}
