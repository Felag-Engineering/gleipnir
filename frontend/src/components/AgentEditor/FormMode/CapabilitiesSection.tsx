import { useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import { useMcpServers } from '@/hooks/queries/servers';
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

type RegistryEntry = { tool: ApiMcpTool; serverName: string };

export function CapabilitiesSection({ value, onChange, errors = [] }: CapabilitiesSectionProps) {
  const capabilityRootErrors = errors.filter(e => e.field === 'capabilities').map(e => e.message);
  const feedbackTimeoutErrors = errors.filter(e => e.field === 'capabilities.feedback.timeout').map(e => e.message);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');

  const { data: servers } = useMcpServers();

  const toolQueries = useQueries({
    queries: (servers ?? []).map(s => ({
      queryKey: queryKeys.servers.toolsAll(s.id),
      queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(s.id)}/tools?include_disabled=true`),
      enabled: Boolean(s.id),
    })),
  });

  const allRegistryTools: RegistryEntry[] = (servers ?? []).flatMap((srv, i) =>
    (toolQueries[i]?.data ?? []).map(tool => ({ tool, serverName: srv.name }))
  );

  // Build the set of identifiers for disabled tools so assigned-tool rows can
  // show a warning badge. Each disabled tool adds two entries: the UUID (used
  // when the tool was added through the picker in this session) and the
  // dot-notation composite key "serverName.toolName" (used when the tool was
  // parsed from an existing policy YAML via yamlToFormState).
  const disabledToolIds = new Set(
    allRegistryTools
      .filter(({ tool }) => !tool.enabled)
      .flatMap(({ tool, serverName }) => [tool.id, `${serverName}.${tool.name}`])
  );

  const assignedIds = new Set(value.tools.map(t => t.toolId));
  const q = query.toLowerCase().trim();
  const filteredRegistry = allRegistryTools.filter(({ tool, serverName }) => {
    if (assignedIds.has(tool.id)) return false;
    if (!q) return true;
    return (
      tool.name.toLowerCase().includes(q) ||
      serverName.toLowerCase().includes(q) ||
      tool.description.toLowerCase().includes(q)
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

  function handleAddTool(tool: ApiMcpTool, serverName: string) {
    const assigned: AssignedTool = {
      toolId: tool.id,
      serverId: tool.server_id,
      serverName,
      name: tool.name,
      description: tool.description,
      approvalRequired: false,
      approvalTimeout: '',
    };
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
            return (
              <AssignedToolRow
                key={tool.toolId}
                tool={tool}
                rowIndex={i}
                rowIssues={rowIssues}
                isDisabled={disabledToolIds.has(tool.toolId)}
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
          results={filteredRegistry}
          onAdd={handleAddTool}
          onClose={() => { setSearchOpen(false); setQuery(''); }}
        />
      ) : (
        <button className={styles.addButton} onClick={() => setSearchOpen(true)}>
          + Add tool from registry
        </button>
      )}

      <div className={styles.feedbackSection}>
        <div className={shared.heading}>Feedback</div>
        <div className={styles.feedbackRow}>
          <button
            role="switch"
            aria-checked={value.feedback.enabled}
            className={styles.toggleButton}
            onClick={handleFeedbackToggle}
            title={value.feedback.enabled ? 'Feedback enabled — click to disable' : 'Feedback disabled — click to enable'}
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
  onRemove: (toolId: string) => void;
  onToggleApproval: (toolId: string) => void;
  onTimeoutChange: (toolId: string, timeout: string) => void;
}

function AssignedToolRow({ tool, rowIndex, rowIssues, isDisabled, onRemove, onToggleApproval, onTimeoutChange }: AssignedToolRowProps) {
  const displayName = `${tool.serverName}.${tool.name}`;
  const toolErrors = rowIssues.filter(e => e.field === `capabilities.tools[${rowIndex}].tool`).map(e => e.message);
  const timeoutErrors = rowIssues.filter(e => e.field === `capabilities.tools[${rowIndex}].timeout`).map(e => e.message);

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
  results: RegistryEntry[];
  onAdd: (tool: ApiMcpTool, serverName: string) => void;
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
          results.map(({ tool, serverName }) => (
            <button
              key={tool.id}
              className={styles.resultRow}
              onClick={() => onAdd(tool, serverName)}
            >
              <span className={styles.toolName}>{serverName}.{tool.name}</span>
              <span className={styles.toolDesc}>{tool.description}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}
