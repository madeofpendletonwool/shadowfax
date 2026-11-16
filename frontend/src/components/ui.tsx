import { useEffect, useRef, useState, ReactNode } from 'react'

// Re-renders every `ms` so countdowns stay live.
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(id)
  }, [ms])
  return now
}

export function useCopy(): [string | null, (key: string, text: string) => void] {
  const [copied, setCopied] = useState<string | null>(null)
  const timer = useRef<number>()
  const copy = (key: string, text: string) => {
    navigator.clipboard?.writeText(text).catch(() => {})
    setCopied(key)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(null), 1800)
  }
  return [copied, copy]
}

type IconProps = { className?: string }
const icon = (d: string, strokeWidth = 2) =>
  function Icon({ className = 'w-4 h-4' }: IconProps) {
    return (
      <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={strokeWidth} aria-hidden>
        <path strokeLinecap="round" strokeLinejoin="round" d={d} />
      </svg>
    )
  }

export const Icons = {
  File: icon('M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z'),
  Stack: icon('M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4'),
  Note: icon('M7 8h10M7 12h10M7 16h6M5 21h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v14a2 2 0 002 2z'),
  Download: icon('M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4'),
  Upload: icon('M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5m-13.5-9L12 3m0 0l4.5 4.5M12 3v13.5'),
  Trash: icon('M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16'),
  Clock: icon('M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z'),
  Copy: icon('M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z'),
  Link: icon('M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1'),
  Check: icon('M4.5 12.75l6 6 9-13.5', 2.5),
  Back: icon('M15 19l-7-7 7-7'),
  Refresh: icon('M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15'),
  Key: icon('M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z'),
  Pencil: icon('M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z'),
  Eye: icon('M15 12a3 3 0 11-6 0 3 3 0 016 0zm6.458 0C20.268 7.943 16.477 5 12 5 7.523 5 3.732 7.943 2.458 12 3.732 16.057 7.523 19 12 19s8.268-2.943 9.542-7z'),
  Lock: icon('M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z'),
  Plus: icon('M12 4v16m8-8H4'),
  Search: icon('M21 21l-4.35-4.35M17 11a6 6 0 11-12 0 6 6 0 0112 0z'),
  Terminal: icon('M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z'),
  Globe: icon('M21 12a9 9 0 01-9 9m9-9a9 9 0 00-9-9m9 9H3m9 9a9 9 0 01-9-9m9 9c1.657 0 3-4.03 3-9s-1.343-9-3-9m0 18c-1.657 0-3-4.03-3-9s1.343-9 3-9m-9 9a9 9 0 019-9'),
  Sparkle: icon('M5 3v4M3 5h4M6 17v4m-2-2h4m5-16l2.286 6.857L21 12l-5.714 2.143L13 21l-2.286-6.857L5 12l5.714-2.143L13 3z'),
}

const base = 'inline-flex items-center justify-center gap-1.5 rounded-lg text-sm font-medium transition-all duration-200 disabled:opacity-40 disabled:cursor-not-allowed'

export const btn = {
  ghost: `${base} px-3 py-1.5 bg-white/5 text-white/70 hover:bg-white/10 hover:text-white`,
  primary: `${base} px-3 py-1.5 bg-indigo-600 text-white hover:bg-indigo-500 shadow-lg shadow-indigo-500/20`,
  danger: `${base} px-3 py-1.5 bg-red-500/10 text-red-400 hover:bg-red-500/20 border border-red-500/20`,
  icon: `${base} p-2 text-white/40 hover:text-white hover:bg-white/10`,
}

// A destructive action that needs a second click within a few seconds.
export function ConfirmButton({
  onConfirm,
  children,
  confirmLabel = 'Click again to confirm',
  className = btn.danger,
  title,
  disabled,
}: {
  onConfirm: () => void
  children: ReactNode
  confirmLabel?: ReactNode
  className?: string
  title?: string
  disabled?: boolean
}) {
  const [armed, setArmed] = useState(false)
  useEffect(() => {
    if (!armed) return
    const id = setTimeout(() => setArmed(false), 3500)
    return () => clearTimeout(id)
  }, [armed])
  return (
    <button
      type="button"
      title={title}
      disabled={disabled}
      className={`${className} ${armed ? '!bg-red-600 !text-white !border-red-500' : ''}`}
      onClick={e => {
        e.stopPropagation()
        if (armed) {
          setArmed(false)
          onConfirm()
        } else setArmed(true)
      }}
    >
      {armed ? confirmLabel : children}
    </button>
  )
}

export function Card({ title, actions, children, className = '' }: {
  title?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`glass rounded-2xl overflow-hidden ${className}`}>
      {(title || actions) && (
        <header className="px-4 py-3 border-b border-white/5 flex items-center justify-between gap-3">
          <h2 className="text-sm font-medium text-white/60">{title}</h2>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  )
}

export function ExpiryBar({ createdAt, expiresAt, now }: { createdAt: string; expiresAt: string | null; now: number }) {
  if (!expiresAt) {
    return <div className="h-1.5 bg-indigo-400/60 rounded-full" title="Never expires" />
  }
  const start = new Date(createdAt).getTime()
  const end = new Date(expiresAt).getTime()
  const left = end - now
  const pct = Math.max(0, Math.min(100, (left / Math.max(1, end - start)) * 100))
  const color = left < 10 * 60e3 ? 'bg-red-500' : left < 60 * 60e3 ? 'bg-amber-400' : 'bg-emerald-400'
  return (
    <div className="h-1.5 bg-white/10 rounded-full overflow-hidden">
      <div className={`h-full ${color} rounded-full transition-all duration-1000`} style={{ width: `${pct}%` }} />
    </div>
  )
}

export function expiryTone(ms: number): string {
  if (ms < 10 * 60e3) return 'text-red-400'
  if (ms < 60 * 60e3) return 'text-amber-400'
  return 'text-white'
}
