import { CircleCheckBig, Circle, ArrowRight } from 'lucide-react'
import type { SetupStep, SetupTab, HealthDetailCopy } from '@/utils/instanceSetup'
import { firstIncompleteBlockingStep } from '@/utils/instanceSetup'
import styles from './InstanceSetupSteps.module.css'

export interface InstanceSetupStepsProps {
  // Ordered onboarding steps (see deriveSetupSteps). When empty, nothing renders.
  steps: SetupStep[]
  // Humanized health_detail copy shown as a subtitle (optional).
  healthDetail?: HealthDetailCopy | null
  // Navigate to one of the settings tabs. The page owns tab state, so CTAs call
  // this rather than routing.
  onNavigate: (tab: SetupTab) => void
}

const STEP_CTA_LABEL: Record<SetupTab, string> = {
  credentials: 'Go to Credentials',
  config: 'Go to Config',
  subscriptions: 'Go to Subscriptions',
}

// InstanceSetupSteps renders the "Steps to healthy" onboarding checklist for a
// plugin instance (#658): a progress count, an optional humanized health_detail
// line, and one row per step with a check/circle icon and a deep-link CTA for
// incomplete steps. The first incomplete blocking step is marked "Next".
export function InstanceSetupSteps({ steps, healthDetail, onNavigate }: InstanceSetupStepsProps) {
  if (steps.length === 0) return null

  const doneCount = steps.filter((s) => s.done).length
  const nextStep = firstIncompleteBlockingStep(steps)

  return (
    <section className={styles.section} aria-label="Steps to healthy">
      <div className={styles.header}>
        <h2 className={styles.title}>Steps to healthy</h2>
        <span className={styles.countBadge}>
          {doneCount}/{steps.length}
        </span>
      </div>

      {healthDetail && (
        <p className={styles.detail}>
          {healthDetail.message}
          {healthDetail.tab && healthDetail.cta && (
            <button
              type="button"
              className={styles.detailCta}
              onClick={() => onNavigate(healthDetail.tab!)}
            >
              {healthDetail.cta}
            </button>
          )}
        </p>
      )}

      <ol className={styles.stepList}>
        {steps.map((step) => {
          const isNext = nextStep?.key === step.key
          return (
            <li key={step.key} className={styles.stepRow}>
              <span className={step.done ? styles.iconDone : styles.iconPending}>
                {step.done ? (
                  <CircleCheckBig size={16} aria-label="done" />
                ) : (
                  <Circle size={16} aria-label="not done" />
                )}
              </span>
              <span className={step.done ? styles.labelDone : styles.label}>
                {step.label}
                {isNext && <span className={styles.nextPill}>Next</span>}
              </span>
              {!step.done && (
                <button
                  type="button"
                  className={styles.cta}
                  onClick={() => onNavigate(step.tab)}
                >
                  {STEP_CTA_LABEL[step.tab]}
                  <ArrowRight size={13} strokeWidth={2} aria-hidden />
                </button>
              )}
            </li>
          )
        })}
      </ol>
    </section>
  )
}
