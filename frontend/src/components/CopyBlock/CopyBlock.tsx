import { useState } from 'react'
import { Check, Copy } from 'lucide-react'
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
        className={copied ? `${styles.copyBtn} ${styles.copyBtnCopied}` : styles.copyBtn}
        onClick={handleCopy}
        aria-label={copied ? 'Copied' : 'Copy to clipboard'}
      >
        {copied ? (
          <>
            <Check size={12} aria-hidden strokeWidth={2} />
            Copied
          </>
        ) : (
          <>
            <Copy size={12} aria-hidden strokeWidth={2} />
            Copy
          </>
        )}
      </button>
    </div>
  )
}
