import {
  formatCpu,
  formatMemory,
  type ConsentDiff,
} from '@/components/admin/PluginReviewCard/consentFormat'
import styles from './ConsentChanges.module.css'

interface ConsentChangesProps {
  diff: ConsentDiff
}

// ConsentChanges summarises what accepting a candidate manifest would change
// in network access and resource limits, so the admin sees the delta without
// comparing two full listings. Domains come from the manifest and are rendered
// as plain text.
export function ConsentChanges({ diff }: ConsentChangesProps) {
  const { egressAdded, egressRemoved, memory, cpu } = diff
  const unchanged = egressAdded.length === 0 && egressRemoved.length === 0 && !memory && !cpu

  return (
    <section className={styles.section} aria-label="Changes since last review">
      <h3 className={styles.heading}>Changes since last review</h3>
      {unchanged ? (
        <p className={styles.none}>No change to network access or resource limits.</p>
      ) : (
        <ul className={styles.list}>
          {egressAdded.map((domain) => (
            <li key={`add-${domain}`} className={styles.added}>
              <span className={styles.tag}>New network access</span>
              <span className={styles.mono}>{domain}</span>
            </li>
          ))}
          {egressRemoved.map((domain) => (
            <li key={`rm-${domain}`} className={styles.removed}>
              <span className={styles.tag}>Network access removed</span>
              <span className={styles.mono}>{domain}</span>
            </li>
          ))}
          {memory && (
            <li>
              <span className={styles.tag}>Memory</span>
              {formatMemory(memory.before.memory_mb)} to {formatMemory(memory.after.memory_mb)}
            </li>
          )}
          {cpu && (
            <li>
              <span className={styles.tag}>CPU</span>
              {formatCpu(cpu.before.cpu_millicores)} to {formatCpu(cpu.after.cpu_millicores)}
            </li>
          )}
        </ul>
      )}
    </section>
  )
}
