export function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1)
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

// Compact "time left" for lists: 2d 4h, 3h 12m, 4m 09s.
export function formatRemaining(ms: number): string {
  if (ms <= 0) return 'Expired'
  const s = Math.floor(ms / 1000)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m ${String(s % 60).padStart(2, '0')}s`
}

export function formatDuration(secs: number): string {
  if (secs <= 0) return '—'
  const d = Math.floor(secs / 86400)
  const h = Math.floor((secs % 86400) / 3600)
  const m = Math.floor((secs % 3600) / 60)
  const parts: string[] = []
  if (d) parts.push(`${d} day${d !== 1 ? 's' : ''}`)
  if (h) parts.push(`${h} hour${h !== 1 ? 's' : ''}`)
  if (!d && m) parts.push(`${m} minute${m !== 1 ? 's' : ''}`)
  return parts.join(' ') || 'under a minute'
}

export function timeAgo(iso: string, now = Date.now()): string {
  const diff = Math.round((now - new Date(iso).getTime()) / 1000)
  if (diff < 45) return 'just now'
  if (diff < 3600) return `${Math.round(diff / 60)}m ago`
  if (diff < 86400) return `${Math.round(diff / 3600)}h ago`
  return `${Math.round(diff / 86400)}d ago`
}

export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

// Value for <input type="datetime-local">, which wants local time without a zone.
export function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// A short human label for a User-Agent: "Chrome on macOS", "curl 8.4".
export function describeAgent(ua: string): string {
  if (!ua) return 'Unknown client'
  const tool = ua.match(/^(curl|Wget|python-requests|python-httpx|Go-http-client|node-fetch|undici|axios|HTTPie|PowerShell|okhttp)[/ ]?([\d.]+)?/i)
  if (tool) return tool[2] ? `${tool[1]} ${tool[2]}` : tool[1]

  let browser = ''
  if (/Edg\//.test(ua)) browser = 'Edge'
  else if (/OPR\//.test(ua)) browser = 'Opera'
  else if (/Firefox\//.test(ua)) browser = 'Firefox'
  else if (/Chrome\//.test(ua)) browser = 'Chrome'
  else if (/Safari\//.test(ua)) browser = 'Safari'

  let os = ''
  if (/iPhone|iPad|iPod/.test(ua)) os = 'iOS'
  else if (/Android/.test(ua)) os = 'Android'
  else if (/Mac OS X|Macintosh/.test(ua)) os = 'macOS'
  else if (/Windows/.test(ua)) os = 'Windows'
  else if (/CrOS/.test(ua)) os = 'ChromeOS'
  else if (/Linux/.test(ua)) os = 'Linux'

  if (browser && os) return `${browser} on ${os}`
  if (browser || os) return browser || os
  return ua.split(/[\s/]/)[0] || 'Unknown client'
}

export const UPLOAD_METHODS: Record<string, { label: string; detail: string }> = {
  web: { label: 'Web UI', detail: 'Uploaded from the browser' },
  multipart: { label: 'API · multipart', detail: 'POST /api/transfer (e.g. curl -F)' },
  raw: { label: 'API · raw', detail: 'PUT /api/upload/{name} (e.g. curl -T)' },
}

export const AUTH_METHODS: Record<string, string> = {
  session: 'Signed-in browser session',
  api_key: 'API key',
  none: 'No login (open instance)',
}

export function methodLabel(m: string): string {
  return UPLOAD_METHODS[m]?.label ?? 'Unknown'
}
