import { useState, useEffect } from 'react'
import { Bot } from 'lucide-react'
import { useModels, type ProviderModels } from '@/hooks/queries/users'
import { usePreferences, useUpdatePreferences } from '@/hooks/useSettings'
import styles from './Settings.module.css'

export function DefaultModelSection() {
  const { data: preferences } = usePreferences()
  const { data: providerModels, status: modelsStatus } = useModels()
  const updatePreferences = useUpdatePreferences()

  const [selected, setSelected] = useState<string>('')

  useEffect(() => {
    if (preferences?.default_model !== undefined) {
      setSelected(preferences.default_model ?? '')
    }
  }, [preferences?.default_model])

  function handleChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const value = e.target.value
    setSelected(value)
    updatePreferences.mutate({ default_model: value || undefined })
  }

  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>
          <Bot size={16} strokeWidth={1.5} className={styles.cardTitleIcon} />
          Default Model
        </h2>
        <p className={styles.cardDesc}>Used when an agent does not specify a model.</p>
      </div>
      <div className={styles.cardBody}>
        <div className={styles.fieldGroup}>
          <label htmlFor="default-model" className={styles.label}>
            Model
          </label>
          <select
            id="default-model"
            className={styles.select}
            value={selected}
            onChange={handleChange}
            disabled={modelsStatus === 'pending' || updatePreferences.isPending}
          >
            <option value="">— no default —</option>
            {(providerModels ?? []).map((group: ProviderModels) => (
              <optgroup key={group.provider} label={group.provider}>
                {group.models.map((m: { name: string; display_name: string }) => (
                  <option key={m.name} value={m.name}>
                    {m.display_name}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </div>
      </div>
    </section>
  )
}
