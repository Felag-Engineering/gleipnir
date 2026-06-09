import { useState } from 'react'
import { usePluginRSS } from '@/hooks/queries/plugins'
import { formatBytes } from '@/utils/format'
import styles from './PluginMemoryBar.module.css'

// PluginMemoryBar renders a one-line summary of aggregate plugin process memory
// in the AdminPluginsPage header. Clicking the summary toggles an expandable
// breakdown table showing RSS per instance, sorted by consumption descending.
//
// Renders nothing while loading or when no instances are running (instance_count === 0).
export function PluginMemoryBar() {
  const { data } = usePluginRSS()
  const [expanded, setExpanded] = useState(false)

  if (!data || data.instance_count === 0) {
    return null
  }

  const { total_bytes, instance_count, instances } = data

  return (
    <div className={styles.container}>
      <button
        type="button"
        className={styles.summary}
        onClick={() => setExpanded((prev) => !prev)}
        aria-expanded={expanded}
      >
        Plugin memory: {formatBytes(total_bytes)} across {instance_count}{' '}
        {instance_count === 1 ? 'instance' : 'instances'}
        <span className={styles.toggle} aria-hidden="true">
          {expanded ? '▲' : '▼'}
        </span>
      </button>

      {expanded && (
        <div className={styles.dropdown}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th className={styles.th}>Instance</th>
                <th className={styles.th}>Plugin</th>
                <th className={styles.thRight}>RSS</th>
              </tr>
            </thead>
            <tbody>
              {instances.map((inst) => (
                <tr key={inst.instance_id}>
                  <td className={styles.td}>{inst.instance_name}</td>
                  <td className={styles.td}>{inst.plugin_id}</td>
                  <td className={styles.tdRight}>{formatBytes(inst.rss_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
