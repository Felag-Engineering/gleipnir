import { Button } from '@/components/Button'
import spinnerStyles from '@/styles/spinner.module.css'

export interface ModalFooterProps {
  onCancel: () => void
  onSubmit?: () => void
  formId?: string
  isLoading: boolean
  submitLabel: string
  loadingLabel?: string
  submitDisabled?: boolean
  variant?: 'primary' | 'danger'
}

export function ModalFooter({
  onCancel,
  onSubmit,
  formId,
  isLoading,
  submitLabel,
  loadingLabel,
  submitDisabled,
  variant = 'primary',
}: ModalFooterProps) {
  return (
    <>
      <Button type="button" variant="ghost" onClick={onCancel} disabled={isLoading}>
        Cancel
      </Button>
      <Button
        type={formId ? 'submit' : 'button'}
        form={formId}
        variant={variant}
        onClick={onSubmit}
        disabled={isLoading || submitDisabled}
      >
        {isLoading ? (
          <>
            <span
              className={variant === 'danger' ? spinnerStyles.spinnerDanger : spinnerStyles.spinner}
              aria-hidden="true"
            />
            {loadingLabel ?? submitLabel}
          </>
        ) : (
          submitLabel
        )}
      </Button>
    </>
  )
}
