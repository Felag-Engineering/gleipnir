import styles from './DeciderLabel.module.css'

interface Props {
  verb: 'Approved' | 'Denied' | 'Answered'
  username: string
}

// DeciderLabel says who settled an approval or feedback request. Callers render
// it only when a user is known: a timeout, a request that predates the
// decided_by column, and a since-deleted account all have no user and show
// nothing at all, rather than guessing at a name.
export function DeciderLabel({ verb, username }: Props) {
  return (
    <span className={styles.label}>
      {verb} by <span className={styles.username}>{username}</span>
    </span>
  )
}
