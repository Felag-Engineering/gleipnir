import { Link } from 'react-router-dom'
import { Check } from 'lucide-react'
import styles from './OnboardingSteps.module.css'

interface OnboardingStepsProps {
  hasServers: boolean
  hasPolicies: boolean
  hasRuns: boolean
}

interface StepConfig {
  title: React.ReactNode
  desc: string
  done: boolean
}

export function OnboardingSteps({ hasServers, hasPolicies, hasRuns }: OnboardingStepsProps) {
  const steps: StepConfig[] = [
    {
      title: <Link to="/mcp" className={styles.stepLink}>Add a tool source</Link>,
      desc: 'Connect an MCP server to give your agents access to tools.',
      done: hasServers,
    },
    {
      title: <Link to="/policies/new" className={styles.stepLink}>Create a policy</Link>,
      desc: 'Define what your agent does, when it runs, and what tools it can use.',
      done: hasPolicies,
    },
    {
      title: 'Trigger your first run',
      desc: 'Send a webhook, run on a schedule, or trigger manually from the policy page.',
      done: hasRuns,
    },
  ]

  return (
    <div className={styles.container}>
      <p className={styles.headline}>Get started with Gleipnir</p>
      <div className={styles.steps}>
        {steps.map((step, i) => (
          <div key={i} className={styles.step}>
            <div className={`${styles.stepNumber}${step.done ? ` ${styles.stepComplete}` : ''}`}>
              {step.done ? <Check size={12} strokeWidth={3} /> : i + 1}
            </div>
            <div>
              <div className={styles.stepTitle}>{step.title}</div>
              <div className={styles.stepDesc}>{step.desc}</div>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
