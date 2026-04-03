import styles from './ParamChip.module.css'

interface Props {
  name: string
  type: string
  required: boolean
}

export function ParamChip({ name, type, required }: Props) {
  return (
    <div className={styles.chip}>
      <span className={styles.name}>{name}</span>
      <span className={styles.type}>{type}</span>
      {required && <span className={styles.required}>required</span>}
    </div>
  )
}
