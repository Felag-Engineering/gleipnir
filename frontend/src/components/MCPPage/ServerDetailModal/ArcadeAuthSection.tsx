import { useMemo, useState } from 'react'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { groupToolsByToolkit } from '@/utils/arcade'
import { useArcadeAuthorizeToolkit, useArcadeAuthorizeWait } from '@/hooks/mutations/arcade'
import styles from './ArcadeAuthSection.module.css'

type ToolkitStatus = 'unknown' | 'authorized' | 'pending' | 'failed'

interface ToolkitState {
  status: ToolkitStatus
  url?: string
  authId?: string
  error?: string
}

interface Props {
  server: ApiMcpServer
  tools: ApiMcpTool[]
  canManage: boolean
}

export function ArcadeAuthSection({ server, tools, canManage }: Props) {
  const [byToolkit, setByToolkit] = useState<Map<string, ToolkitState>>(new Map())
  // inFlight tracks which toolkits are currently authorizing. While any toolkit
  // is in-flight, all other Authorize buttons are disabled to prevent concurrent
  // mutations through the single shared waitMutation hook instance.
  const [inFlight, setInFlight] = useState<Set<string>>(new Set())

  const groups = useMemo(() => groupToolsByToolkit(tools), [tools])

  const authorize = useArcadeAuthorizeToolkit(server.id)
  const waitMutation = useArcadeAuthorizeWait(server.id)

  async function handleAuthorize(toolkit: string) {
    if (!canManage) return
    if (inFlight.size > 0) return

    setInFlight((prev) => new Set(prev).add(toolkit))
    try {
      const resp = await authorize.mutateAsync({ toolkit })

      if (resp.status === 'completed') {
        setByToolkit((prev) => new Map(prev).set(toolkit, { status: 'authorized' }))
        return
      }

      if (resp.status === 'pending') {
        window.open(resp.url, '_blank', 'noopener')
        setByToolkit((prev) =>
          new Map(prev).set(toolkit, { status: 'pending', url: resp.url, authId: resp.auth_id }),
        )

        // Frontend re-issue loop: each /wait call is bounded to ~10s server-side,
        // well under HTTP WriteTimeout (15s default). Loop terminates when the
        // response reaches a terminal status or mutateAsync throws.
        let waitResp = await waitMutation.mutateAsync({ toolkit, auth_id: resp.auth_id })
        while (waitResp.status === 'pending') {
          waitResp = await waitMutation.mutateAsync({ toolkit, auth_id: waitResp.auth_id })
        }

        if (waitResp.status === 'completed') {
          setByToolkit((prev) => new Map(prev).set(toolkit, { status: 'authorized' }))
        } else {
          // status === 'failed'
          setByToolkit((prev) =>
            new Map(prev).set(toolkit, {
              status: 'failed',
              error: 'error' in waitResp ? waitResp.error : undefined,
            }),
          )
        }
      }
    } catch (err) {
      setByToolkit((prev) =>
        new Map(prev).set(toolkit, {
          status: 'failed',
          error: err instanceof Error ? err.message : 'Authorization failed',
        }),
      )
    } finally {
      setInFlight((prev) => {
        const next = new Set(prev)
        next.delete(toolkit)
        return next
      })
    }
  }

  if (groups.size === 0) return null

  return (
    <div className={styles.section}>
      <p className={styles.title}>Toolkit authorization</p>
      {Array.from(groups.entries()).map(([toolkit, tkTools]) => {
        const state = byToolkit.get(toolkit)
        const isThisInFlight = inFlight.has(toolkit)
        const anyInFlight = inFlight.size > 0

        return (
          <div key={toolkit} className={styles.row}>
            <span className={styles.toolkitName}>
              {toolkit}
              <span className={styles.toolCount}> ({tkTools.length} {tkTools.length === 1 ? 'tool' : 'tools'})</span>
            </span>

            {state?.status === 'authorized' && (
              <span className={styles.badgeAuthorized}>✓ Authorized</span>
            )}
            {state?.status === 'pending' && (
              <span className={styles.badgePending}>⚠ Action needed</span>
            )}
            {state?.status === 'failed' && (
              <span className={styles.badgeFailed} title={state.error || undefined}>
                ✗ Failed{state.error ? `: ${state.error}` : ''}
              </span>
            )}

            {canManage && (
              <>
                {isThisInFlight ? (
                  <span className={styles.spinner} aria-label="Authorizing…" role="status" />
                ) : (
                  <button
                    type="button"
                    className={styles.authorizeBtn}
                    disabled={anyInFlight}
                    onClick={() => void handleAuthorize(toolkit)}
                  >
                    {state?.status === 'pending' ? 'Authorize →' : 'Check →'}
                  </button>
                )}
              </>
            )}
          </div>
        )
      })}
    </div>
  )
}
