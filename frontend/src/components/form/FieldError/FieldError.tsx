import styles from './FieldError.module.css'

export interface FieldErrorProps {
  /** DOM id — lets the field set aria-describedby to this element. */
  id?: string
  /** One or more error messages. A single string is treated as one message. */
  messages?: string | string[]
}

/**
 * Renders a small inline error message beneath a form field. Returns null
 * when there are no messages to show. Use `id` + `aria-describedby` on the
 * associated input to wire up the accessibility relationship.
 */
export function FieldError({ id, messages }: FieldErrorProps) {
  const list = messages === undefined || messages === null
    ? []
    : Array.isArray(messages)
      ? messages.filter(m => m.length > 0)
      : messages.length > 0
        ? [messages]
        : []

  if (list.length === 0) return null

  if (list.length === 1) {
    return (
      <div id={id} role="alert" className={styles.fieldError}>
        {list[0]}
      </div>
    )
  }

  return (
    <ul id={id} role="alert" className={`${styles.fieldError} ${styles.fieldErrorList}`}>
      {list.map((msg, i) => (
        <li key={i}>{msg}</li>
      ))}
    </ul>
  )
}
