import styles from './SubscribedBindingConfig.module.css';
import type { ApiPluginInstanceForAudience } from '@/api/types';
import { useTestBindingAgainstSamples } from '@/hooks/mutations/bindingTest';

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
  description?: string;
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

// BindingField renders a single operator-aware input for one binding key.
function BindingField({
  name,
  prop,
  value,
  onChange,
}: {
  name: string;
  prop: SchemaProperty;
  value: unknown;
  onChange: (val: unknown) => void;
}) {
  const isMentionOnly = name === 'mention_only' && prop.type === 'boolean';
  const isBool = prop.type === 'boolean';

  if (isMentionOnly || isBool) {
    return (
      <div className={styles.row}>
        <label className={styles.checkbox}>
          <input
            type="checkbox"
            checked={!!value}
            onChange={(e) => onChange(e.target.checked)}
          />
          {name}
          {isMentionOnly && (
            <span className={styles.testTooltip}>(mention-only: only fire when @mentioned)</span>
          )}
        </label>
      </div>
    );
  }

  const inputId = `binding-field-${name}`;
  return (
    <div className={styles.row}>
      <label htmlFor={inputId} className={styles.label}>
        {name}
        {prop.format && prop.format !== '' && (
          <span className={styles.testTooltip}> ({prop.format})</span>
        )}
      </label>
      <input
        id={inputId}
        className={styles.input}
        type="text"
        value={typeof value === 'string' ? value : ''}
        placeholder={prop.format === 'regex' ? 'RE2 regex…' : prop.format === 'contains' ? 'substring…' : 'exact value…'}
        onChange={(e) => onChange(e.target.value)}
      />
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

  // Find the instance ID for the mutation (endpoint needs the instance ID, not name).
  const instanceId = pluginInstances?.find((p) => p.instance_name === source)?.id ?? '';
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
      {fieldNames.length > 0 && (
        <div className={styles.fields}>
          {fieldNames.map((name) => (
            <BindingField
              key={name}
              name={name}
              prop={properties[name]}
              value={binding[name]}
              onChange={(val) => handleFieldChange(name, val)}
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
