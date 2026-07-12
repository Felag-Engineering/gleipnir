import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

export type ToastVariant = 'success' | 'error' | 'info'

export interface ToastOptions {
  /** Milliseconds before the toast auto-dismisses. 0 = sticky (no auto-dismiss). */
  duration?: number
}

export interface Toast {
  id: string
  variant: ToastVariant
  message: string
}

export interface ToastApi {
  success: (message: string, opts?: ToastOptions) => void
  error: (message: string, opts?: ToastOptions) => void
  info: (message: string, opts?: ToastOptions) => void
  dismiss: (id: string) => void
}

const DEFAULT_DURATIONS: Record<ToastVariant, number> = {
  success: 4000,
  info: 4000,
  error: 6000,
}

function noop() {}

const noopApi: ToastApi = { success: noop, error: noop, info: noop, dismiss: noop }

interface ToastContextValue {
  toasts: Toast[]
  api: ToastApi
}

// Default value is a working no-op API + empty toast list so useToast() never
// throws when a component renders without a ToastProvider ancestor (the many
// existing standalone page/component tests in this repo do exactly that).
const ToastContext = createContext<ToastContextValue>({ toasts: [], api: noopApi })

/** useToast returns the imperative API for firing toasts: success/error/info/dismiss. */
export function useToast(): ToastApi {
  return useContext(ToastContext).api
}

/** useToastState is consumed by ToastRegion to render the current toast stack. */
export function useToastState(): { toasts: Toast[]; dismiss: (id: string) => void } {
  const { toasts, api } = useContext(ToastContext)
  return { toasts, dismiss: api.dismiss }
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  // Monotonic id counter — crypto.randomUUID is unreliable in jsdom test envs.
  const nextId = useRef(0)
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>())

  const dismiss = useCallback((id: string) => {
    const timer = timers.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.current.delete(id)
    }
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  const addToast = useCallback((variant: ToastVariant, message: string, opts?: ToastOptions) => {
    const id = String(nextId.current++)
    setToasts(prev => [...prev, { id, variant, message }])

    const duration = opts?.duration ?? DEFAULT_DURATIONS[variant]
    if (duration > 0) {
      const timer = setTimeout(() => dismiss(id), duration)
      timers.current.set(id, timer)
    }
  }, [dismiss])

  // Clear any pending auto-dismiss timers on unmount so they never fire
  // against an unmounted component.
  useEffect(() => {
    const pendingTimers = timers.current
    return () => {
      pendingTimers.forEach(timer => clearTimeout(timer))
      pendingTimers.clear()
    }
  }, [])

  const api = useMemo<ToastApi>(() => ({
    success: (message, opts) => addToast('success', message, opts),
    error: (message, opts) => addToast('error', message, opts),
    info: (message, opts) => addToast('info', message, opts),
    dismiss,
  }), [addToast, dismiss])

  const value = useMemo<ToastContextValue>(() => ({ toasts, api }), [toasts, api])

  return <ToastContext.Provider value={value}>{children}</ToastContext.Provider>
}
