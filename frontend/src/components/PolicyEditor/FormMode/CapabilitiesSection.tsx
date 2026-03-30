import { useMemo, useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import { RoleBadge } from '@/components/RoleBadge';
import { useMcpServers } from '@/hooks/useMcpServers';
import { queryKeys } from '@/hooks/queryKeys';
import { apiFetch } from '@/api/fetch';
import type { ApiMcpTool } from '@/api/types';
import type { AssignedTool, CapabilitiesFormState } from './types';
import styles from './CapabilitiesSection.module.css';

export interface CapabilitiesSectionProps {
  value: CapabilitiesFormState;
  onChange: (next: CapabilitiesFormState) => void;
}

type RegistryEntry = { tool: ApiMcpTool; serverName: string };

export function CapabilitiesSection({ value, onChange }: CapabilitiesSectionProps) {
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');

  const { data: servers } = useMcpServers();

  const toolQueries = useQueries({
    queries: (servers ?? []).map(s => ({
      queryKey: queryKeys.servers.tools(s.id),
      queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(s.id)}/tools`),
      enabled: Boolean(s.id),
    })),
  });

  const allRegistryTools: RegistryEntry[] = (servers ?? []).flatMap((srv, i) =>
    (toolQueries[i]?.data ?? []).map(tool => ({ tool, serverName: srv.name }))
  );

  // Reconcile roles: YAML doesn't store capability_role, so tools parsed from
  // YAML default to 'tool'. Use the registry (the DB source of truth) to fix them.
  const registryRoleByKey = useMemo(() => {
    const map = new Map<string, ApiMcpTool['capability_role']>();
    for (const { tool, serverName } of allRegistryTools) {
      map.set(`${serverName}.${tool.name}`, tool.capability_role);
    }
    return map;
  }, [allRegistryTools]);

  const reconciledTools = useMemo(() => {
    let changed = false;
    const result = value.tools.map(t => {
      const key = `${t.serverName}.${t.name}`;
      const registryRole = registryRoleByKey.get(key);
      if (registryRole && registryRole !== t.role) {
        changed = true;
        return { ...t, role: registryRole };
      }
      return t;
    });
    return changed ? result : value.tools;
  }, [value.tools, registryRoleByKey]);

  const assignedIds = new Set(reconciledTools.map(t => t.toolId));
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
    onChange({ tools: value.tools.filter(t => t.toolId !== toolId) });
  }

  function handleToggleApproval(toolId: string) {
    onChange({
      tools: value.tools.map(t =>
        t.toolId === toolId ? { ...t, approvalRequired: !t.approvalRequired } : t
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
      role: tool.capability_role,
      approvalRequired: false,
    };
    onChange({ tools: [...value.tools, assigned] });
    setSearchOpen(false);
    setQuery('');
  }

  function handleSearchOpen() {
    setSearchOpen(true);
  }

  function handleSearchClose() {
    setSearchOpen(false);
    setQuery('');
  }

  return (
    <div className={styles.section}>
      <div className={styles.heading}>Capabilities</div>

      <Legend />

      {reconciledTools.length === 0 ? (
        <div className={styles.emptyState}>
          No tools added yet. Add tools from the registry below.
        </div>
      ) : (
        <div className={styles.toolList}>
          {reconciledTools.map(tool => (
            <AssignedToolRow
              key={tool.toolId}
              tool={tool}
              onRemove={handleRemove}
              onToggleApproval={handleToggleApproval}
            />
          ))}
        </div>
      )}

      {searchOpen ? (
        <SearchPanel
          query={query}
          onQueryChange={setQuery}
          results={filteredRegistry}
          onAdd={handleAddTool}
          onClose={handleSearchClose}
        />
      ) : (
        <button className={styles.addButton} onClick={handleSearchOpen}>
          + Add tool from registry
        </button>
      )}
    </div>
  );
}

function Legend() {
  return (
    <div className={styles.legend}>
      <div className={styles.legendItem}>
        <RoleBadge role="tool" />
        <span className={styles.legendDesc}>available tools, optionally approval-gated</span>
      </div>
      <div className={styles.legendItem}>
        <RoleBadge role="feedback" />
        <span className={styles.legendDesc}>human-in-the-loop channel</span>
      </div>
    </div>
  );
}

interface AssignedToolRowProps {
  tool: AssignedTool;
  onRemove: (toolId: string) => void;
  onToggleApproval: (toolId: string) => void;
}

function AssignedToolRow({ tool, onRemove, onToggleApproval }: AssignedToolRowProps) {
  const displayName = `${tool.serverName}.${tool.name}`;

  return (
    <div className={styles.toolRow}>
      <RoleBadge role={tool.role} />
      <span className={styles.toolName}>{displayName}</span>
      <span className={styles.toolDesc}>{tool.description}</span>
      {tool.role === 'tool' && (
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
        </div>
      )}
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
              <RoleBadge role={tool.capability_role} />
              <span className={styles.toolName}>{serverName}.{tool.name}</span>
              <span className={styles.toolDesc}>{tool.description}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}
