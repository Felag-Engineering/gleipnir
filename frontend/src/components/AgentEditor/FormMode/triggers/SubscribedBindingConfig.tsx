import { useState } from 'react';
import { Link } from 'react-router';
import styles from './SubscribedBindingConfig.module.css';
import type { ApiPluginInstanceForAudience } from '@/api/types';
import { AsyncCombobox } from '@/components/form/AsyncCombobox';
import { usePluginOptions } from '@/hooks/usePluginOptions';
import { useTestBindingAgainstSamples } from '@/hooks/mutations/bindingTest';
import { humanize } from '@/components/form/SchemaForm/SchemaForm';

export interface SubscribedBindingConfigProps {
  source: string;       // instance_name — used to find the matching plugin instance
  eventKind: string;    // matches an EventKindDecl.Kind
  binding: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  pluginInstances?: ApiPluginInstanceForAudience[];
}

// BindingSchemaProperties is the shape we expect under binding_schema.properties.
// Each property has at least a `type` and an optional `format`.
interface SchemaProperty {
  type?: string;
  format?: string;
  title?: string;
  description?: string;
  // x-gleipnir-options: present when the field has a dynamic options provider.
  'x-gleipnir-options'?: { source: string; multi?: boolean };
}

interface BindingSchema {
  properties?: Record<string, SchemaProperty>;
}

// resolveBindingSchema extracts the typed binding_schema for the selected
// instance + event kind combination from the plugin-instances list.
function resolveBindingSchema(
  pluginInstances: ApiPluginInstanceForAudience[] | undefined,
  source: string,
  eventKind: string,
): BindingSchema | undefined {
  if (!pluginInstances) return undefined;
  const inst = pluginInstances.find((p) => p.instance_name === source);
  if (!inst) return undefined;
  const ek = inst.event_kinds?.find((e) => e.kind === eventKind);
  if (!ek?.binding_schema) return undefined;
  return ek.binding_schema as BindingSchema;
}

// resolveGuidance extracts the guidance string for the selected instance + event
// kind combination. Returns undefined when not present so callers can guard with
// a truthy check before rendering.
function resolveGuidance(
  pluginInstances: ApiPluginInstanceForAudience[] | undefined,
  source: string,
  eventKind: string,
): string | undefined {
  if (!pluginInstances) return undefined;
  const inst = pluginInstances.find((p) => p.instance_name === source);
  if (!inst) return undefined;
  const ek = inst.event_kinds?.find((e) => e.kind === eventKind);
  return ek?.guidance || undefined;
}

// resolveExamples extracts example payloads for the selected event kind.
function resolveExamples(
  pluginInstances: ApiPluginInstanceForAudience[] | undefined,
  source: string,
  eventKind: string,
): { name: string; payload: Record<string, unknown> }[] {
  if (!pluginInstances) return [];
  const inst = pluginInstances.find((p) => p.instance_name === source);
  if (!inst) return [];
  const ek = inst.event_kinds?.find((e) => e.kind === eventKind);
  return ek?.examples ?? [];
}

// OptionsBindingField renders an AsyncCombobox for a field annotated with
// x-gleipnir-options. It owns its query state and calls usePluginOptions so
// hooks are called at the top level of a component (Rules of Hooks).
function OptionsBindingField({
  name,
  title,
  description,
  source,
  pluginId,
  instanceId,
  value,
  onChange,
}: {
  name: string;
  title?: string;
  description?: string;
  source: string;
  pluginId: string;
  instanceId: string;
  value: unknown;
  onChange: (val: unknown) => void;
}) {
  const [query, setQuery] = useState('');
  const optionsQuery = usePluginOptions({
    pluginId,
    instanceId,
    source,
    query,
    enabled: Boolean(pluginId) && Boolean(instanceId),
  });

  const inputId = `binding-field-${name}`;
  const degraded = !!optionsQuery.data?.degraded;

  // Build a search callback for AsyncCombobox: sets the query (triggering the
  // usePluginOptions query) and returns a Promise resolving to the options.
  // The options are already pre-fetched via the query; we just return the current data.
  function handleSearch(q: string) {
    setQuery(q);
    // Return current cached data immediately; TanStack Query re-fetches in the
    // background when the key changes (query string changes).
    return Promise.resolve(optionsQuery.data?.options ?? []);
  }

  return (
    <div className={styles.row}>
      <label htmlFor={inputId} className={styles.label}>
        {title ?? humanize(name)}
      </label>
      <AsyncCombobox
        id={inputId}
        value={typeof value === 'string' ? value : ''}
        onChange={(v) => onChange(v)}
        onSearch={handleSearch}
        degraded={degraded}
        placeholder="Search…"
      />
      {description && <p className={styles.caption}>{description}</p>}
    </div>
  );
}

// BindingField renders a single operator-aware input for one binding key.
// Delegates to OptionsBindingField when the property has x-gleipnir-options
// and plugin context is available.
function BindingField({
  name,
  prop,
  value,
  onChange,
  pluginId,
  instanceId,
}: {
  name: string;
  prop: SchemaProperty;
  value: unknown;
  onChange: (val: unknown) => void;
  pluginId?: string;
  instanceId?: string;
}) {
  const isBool = prop.type === 'boolean';
  const label = prop.title ?? humanize(name);

  if (isBool) {
    return (
      <div className={styles.row}>
        <label className={styles.checkbox}>
          <input
            type="checkbox"
            checked={!!value}
            onChange={(e) => onChange(e.target.checked)}
          />
          {label}
        </label>
        {prop.description && <p className={styles.caption}>{prop.description}</p>}
      </div>
    );
  }

  const optionsAnnotation = prop['x-gleipnir-options'];
  if (optionsAnnotation && pluginId && instanceId) {
    return (
      <OptionsBindingField
        name={name}
        title={prop.title}
        description={prop.description}
        source={optionsAnnotation.source}
        pluginId={pluginId}
        instanceId={instanceId}
        value={value}
        onChange={onChange}
      />
    );
  }

  const inputId = `binding-field-${name}`;
  return (
    <div className={styles.row}>
      <label htmlFor={inputId} className={styles.label}>
        {label}
      </label>
      <input
        id={inputId}
        className={styles.input}
        type="text"
        value={typeof value === 'string' ? value : ''}
        placeholder={prop.format === 'regex' ? 'RE2 regex…' : prop.format === 'contains' ? 'substring…' : 'exact value…'}
        onChange={(e) => onChange(e.target.value)}
      />
      {prop.description && <p className={styles.caption}>{prop.description}</p>}
    </div>
  );
}

export function SubscribedBindingConfig({
  source,
  eventKind,
  binding,
  onChange,
  pluginInstances,
}: SubscribedBindingConfigProps) {
  const schema = resolveBindingSchema(pluginInstances, source, eventKind);
  const examples = resolveExamples(pluginInstances, source, eventKind);
  const guidance = resolveGuidance(pluginInstances, source, eventKind);

  // Resolve instance details for the mutation and the options endpoint.
  const matchedInstance = pluginInstances?.find((p) => p.instance_name === source);
  const instanceId = matchedInstance?.id ?? '';
  const pluginId = matchedInstance?.plugin_id ?? '';
  const testMutation = useTestBindingAgainstSamples(instanceId, eventKind);

  const properties = schema?.properties ?? {};
  const fieldNames = Object.keys(properties);
  const hasExamples = examples.length > 0;

  function handleFieldChange(name: string, val: unknown) {
    onChange({ ...binding, [name]: val });
  }

  function handleTest() {
    testMutation.mutate({
      binding,
      payloads: examples.map((e) => e.payload),
    });
  }

  // Derive a human-readable error from the mutation result for compile errors.
  // Check for `detail` property duck-type (ApiError shape) before falling back
  // to the generic message so the server's compile-error detail is shown.
  let compileError: string | undefined;
  if (testMutation.isError) {
    const err: unknown = testMutation.error;
    if (err && typeof err === 'object' && 'detail' in err && typeof (err as { detail: unknown }).detail === 'string') {
      compileError = (err as { detail: string }).detail;
    } else if (err instanceof Error) {
      compileError = err.message;
    }
  }

  return (
    <div className={styles.container}>
      {guidance && (
        <div className={styles.guidance}>
          <p className={styles.guidanceTitle}>How this fires</p>
          <p className={styles.guidanceBody}>{guidance}</p>
        </div>
      )}

      {pluginId && instanceId && (
        <p className={styles.explainer}>
          This filters events the instance is already watching. The instance's{' '}
          <Link to={`/admin/plugins/${pluginId}/instances/${instanceId}`}>subscription scope</Link>{' '}
          controls which channels and DMs are delivered.
        </p>
      )}

      {fieldNames.length > 0 && (
        <div className={styles.fields}>
          {fieldNames.map((name) => (
            <BindingField
              key={name}
              name={name}
              prop={properties[name]}
              value={binding[name]}
              onChange={(val) => handleFieldChange(name, val)}
              pluginId={pluginId}
              instanceId={instanceId}
            />
          ))}
        </div>
      )}

      <div className={styles.testSection}>
        <div className={styles.testHeader}>
          <button
            type="button"
            className={[
              styles.testButton,
              testMutation.isPending ? styles.testButtonRunning : '',
            ]
              .filter(Boolean)
              .join(' ')}
            onClick={handleTest}
            disabled={!hasExamples || testMutation.isPending}
            title={hasExamples ? undefined : 'Plugin has no examples'}
          >
            {testMutation.isPending ? 'Testing…' : 'Test against sample'}
          </button>
          {!hasExamples && (
            <span className={styles.testTooltip}>Plugin has no examples</span>
          )}
        </div>

        {compileError && (
          <p className={styles.matchError}>Compile error: {compileError}</p>
        )}

        {testMutation.data && (
          <div className={styles.results}>
            {examples.map((ex, i) => {
              const result = testMutation.data.results[i];
              return (
                <div key={ex.name} className={styles.resultRow}>
                  <span className={styles.resultName}>{ex.name}</span>
                  {result?.error ? (
                    <span className={styles.matchError}>error: {result.error}</span>
                  ) : result?.match ? (
                    <span className={styles.matchTrue}>matched</span>
                  ) : (
                    <span className={styles.matchFalse}>did not match</span>
                  )}
                </div>
              );
            })}
          </div>
        )}

        <p className={styles.note}>
          Only manifest examples — paste-your-own-JSON deferred to v2. Use{' '}
          <code>gleipnir-plugin run --capture</code> /{' '}
          <code>--replay</code> for custom payloads.
        </p>
      </div>
    </div>
  );
}
