import { useState } from 'react'
import styles from './CopyBlock.module.css'

interface Props {
  text: string
  children: React.ReactNode
}

export function CopyBlock({ text, children }: Props) {
  const [copied, setCopied] = useState(false)

  function handleCopy() {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1800)
    })
  }

  return (
    <div className={styles.wrapper}>
      {children}
      <button
        type="button"
        className={styles.copyBtn}
        onClick={handleCopy}
        aria-label="Copy to clipboard"
      >
        {copied ? '✓' : 'Copy'}
      </button>
    </div>
  )
}
