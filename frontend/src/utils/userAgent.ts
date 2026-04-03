// Best-effort user agent parser for display purposes.
// Returns a short human-readable browser name + major version.
export function parseUserAgent(ua: string): string {
  if (!ua) return 'Unknown client'
  if (/Chrome\/[\d.]+/.test(ua) && !/Edg\//.test(ua) && !/OPR\//.test(ua)) {
    const m = ua.match(/Chrome\/([\d.]+)/)
    return `Chrome ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Firefox\/[\d.]+/.test(ua)) {
    const m = ua.match(/Firefox\/([\d.]+)/)
    return `Firefox ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Safari\/[\d.]+/.test(ua) && !/Chrome/.test(ua)) {
    const m = ua.match(/Version\/([\d.]+)/)
    return `Safari ${m?.[1]?.split('.')[0] ?? ''}`
  }
  if (/Edg\/[\d.]+/.test(ua)) {
    const m = ua.match(/Edg\/([\d.]+)/)
    return `Edge ${m?.[1]?.split('.')[0] ?? ''}`
  }
  return ua.slice(0, 60)
}
