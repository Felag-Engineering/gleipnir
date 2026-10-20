import { useState, type KeyboardEvent } from 'react'
import type { ApiError } from '@/api/fetch'
import type { ApiPluginInstanceForAudience, ApiUserIdentity } from '@/api/types'
import { Button } from '@/components/Button'
import { FieldError } from '@/components/form/FieldError'
import { usePluginInstancesForAudience } from '@/hooks/queries/admin'
import { useUserIdentities } from '@/hooks/queries/identities'
import { useClearUserIdentity, useSetUserIdentity } from '@/hooks/mutations/identities'
import styles from './IdentityFields.module.css'

const ADMIN_SET = 'admin_set'

const CONFLICT_MESSAGE = 'Already mapped to another user on this instance.'

interface IdentityFieldProps {
  userId: string
  instance: ApiPluginInstanceForAudience
  identity: ApiUserIdentity | undefined
}

function IdentityField({ userId, instance, identity }: IdentityFieldProps) {
  const saved = identity?.external_user_id ?? ''
  const [value, setValue] = useState(saved)
  const setMutation = useSetUserIdentity(userId)
  const clearMutation = useClearUserIdentity(userId)

  const inputId = `identity-${instance.id}`
  const errorId = `${inputId}-error`
  const trimmed = value.trim()
  const isPending = setMutation.isPending || clearMutation.isPending

  const failure = (setMutation.error ?? clearMutation.error) as ApiError | null
  const errorMessage = failure
    ? failure.status === 409
      ? CONFLICT_MESSAGE
      : (failure.detail ?? failure.message)
    : undefined

  function save() {
    if (trimmed === '' || trimmed === saved) return
    clearMutation.reset()
    setMutation.mutate({ instanceId: instance.id, externalUserId: trimmed })
  }

  function clear() {
    setMutation.reset()
    clearMutation.mutate(
      { instanceId: instance.id },
      { onSuccess: () => setValue('') },
    )
  }

  // Enter inside the field saves this mapping instead of submitting the
  // surrounding user form.
  function handleKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key !== 'Enter') return
    e.preventDefault()
    save()
  }

  return (
    <div className={styles.fieldGroup}>
      <label htmlFor={inputId} className={styles.label}>
        {instance.instance_name} user ID
      </label>
      <div className={styles.row}>
        <input
          id={inputId}
          type="text"
          className={styles.input}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={handleKeyDown}
          autoComplete="off"
          aria-invalid={Boolean(errorMessage)}
          aria-describedby={errorId}
        />
        <Button
          variant="secondary"
          size="small"
          onClick={save}
          loading={setMutation.isPending}
          disabled={isPending || trimmed === '' || trimmed === saved}
          aria-label={`Save ${instance.instance_name} user ID`}
        >
          Save
        </Button>
        <Button
          variant="ghost"
          size="small"
          onClick={clear}
          loading={clearMutation.isPending}
          disabled={isPending || identity === undefined}
          aria-label={`Clear ${instance.instance_name} user ID`}
        >
          Clear
        </Button>
      </div>
      <FieldError id={errorId} messages={errorMessage} />
    </div>
  )
}

// One actor-id field per plugin instance that lets an admin set a user's id
// directly (identity_provider.link_methods contains admin_set). Renders
// nothing when there is no such instance. Each field saves on its own, not
// with the surrounding user form.
export function IdentityFields({ userId }: { userId: string }) {
  const instancesQuery = usePluginInstancesForAudience()
  const identitiesQuery = useUserIdentities(userId)

  const instances = (instancesQuery.data ?? []).filter((inst) =>
    (inst.identity_link_methods ?? []).includes(ADMIN_SET),
  )
  if (instances.length === 0) return null

  if (identitiesQuery.isError) {
    return <FieldError messages="Could not load this user's linked IDs." />
  }
  // Fields seed their input from the saved identity, so wait for it.
  if (!identitiesQuery.data) return null

  const identityByInstance = new Map(identitiesQuery.data.map((i) => [i.instance_id, i]))

  return (
    <>
      {instances.map((inst) => {
        const identity = identityByInstance.get(inst.id)
        return (
          <IdentityField
            // Remount when the saved value changes so the input reflects the refetch.
            key={`${inst.id}:${identity?.external_user_id ?? ''}`}
            userId={userId}
            instance={inst}
            identity={identity}
          />
        )
      })}
    </>
  )
}
