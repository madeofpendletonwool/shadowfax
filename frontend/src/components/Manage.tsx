import { useCallback, useEffect, useMemo, useState } from 'react'
import { manageApi, ManageList, ManagedTransfer, transferTitle, UnauthorizedError } from '../manageApi'
import { formatBytes, formatRemaining, methodLabel, timeAgo } from '../format'
import { btn, ConfirmButton, ExpiryBar, expiryTone, Icons, useNow } from './ui'
import TransferDetail from './TransferDetail'

interface Props {
  selectedPin: string | null
  onSelect: (pin: string | null, replace?: boolean) => void
  onUnauthorized: () => void
  onNewUpload: () => void
}

type SortKey = 'newest' | 'oldest' | 'expiring' | 'largest' | 'downloads' | 'name'
type Filter = 'all' | 'web' | 'api' | 'note' | 'untouched' | 'expiring'

const SORTS: { key: SortKey; label: string }[] = [
  { key: 'newest', label: 'Newest first' },
  { key: 'oldest', label: 'Oldest first' },
  { key: 'expiring', label: 'Expiring soonest' },
  { key: 'largest', label: 'Largest' },
  { key: 'downloads', label: 'Most downloaded' },
  { key: 'name', label: 'Name' },
]

const FILTERS: { key: Filter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'web', label: 'Web' },
  { key: 'api', label: 'API' },
  { key: 'note', label: 'Has note' },
  { key: 'untouched', label: 'Never downloaded' },
  { key: 'expiring', label: 'Expiring < 1h' },
]

const REFRESH_MS = 30_000

function TransferIcon({ t }: { t: ManagedTransfer }) {
  const Icon = t.files.length > 1 ? Icons.Stack : t.files.length === 1 ? Icons.File : Icons.Note
  return (
    <div className="w-10 h-10 rounded-xl bg-indigo-500/10 text-indigo-400 flex items-center justify-center flex-shrink-0">
      <Icon className="w-5 h-5" />
    </div>
  )
}

function MethodBadge({ method }: { method: string }) {
  const web = method === 'web'
  const Icon = web ? Icons.Globe : Icons.Terminal
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[11px] font-medium ${
        web ? 'bg-sky-500/10 text-sky-300' : method ? 'bg-violet-500/10 text-violet-300' : 'bg-white/5 text-white/40'
      }`}
    >
      <Icon className="w-3 h-3" />
      {methodLabel(method)}
    </span>
  )
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="glass rounded-2xl px-4 py-3">
      <p className="text-xs text-white/40 uppercase tracking-wider">{label}</p>
      <p className="text-2xl font-semibold text-white mt-1 tabular-nums">{value}</p>
      {hint && <p className="text-xs text-white/40 mt-0.5 truncate">{hint}</p>}
    </div>
  )
}

export default function Manage({ selectedPin, onSelect, onUnauthorized, onNewUpload }: Props) {
  const [data, setData] = useState<ManageList | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<SortKey>('newest')
  const [filter, setFilter] = useState<Filter>('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const now = useNow()

  const handleError = useCallback(
    (err: unknown) => {
      if (err instanceof UnauthorizedError) onUnauthorized()
      setError(err instanceof Error ? err.message : 'Something went wrong')
    },
    [onUnauthorized]
  )

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const list = await manageApi.list()
      setData(list)
      setError(null)
      // Drop selections for transfers that expired or were deleted.
      setSelected(prev => new Set([...prev].filter(p => list.transfers.some(t => t.pin === p))))
    } catch (err) {
      handleError(err)
    }
    setLoading(false)
  }, [handleError])

  useEffect(() => {
    if (selectedPin) return
    load()
    const id = setInterval(load, REFRESH_MS)
    return () => clearInterval(id)
  }, [load, selectedPin])

  // Only the "expiring" filter depends on the clock; don't re-sort every second otherwise.
  const clock = filter === 'expiring' ? now : 0

  const visible = useMemo(() => {
    if (!data) return []
    const q = query.trim().toLowerCase()
    let list = data.transfers.filter(t => {
      if (q) {
        const hay = [t.label, t.pin, ...t.files.map(f => f.name)].join('\n').toLowerCase()
        if (!hay.includes(q)) return false
      }
      switch (filter) {
        case 'web': return t.upload.method === 'web'
        case 'api': return t.upload.method === 'raw' || t.upload.method === 'multipart'
        case 'note': return t.has_text
        case 'untouched': return t.download_count === 0
        case 'expiring': return new Date(t.expires_at).getTime() - clock < 3600e3
        default: return true
      }
    })
    const by: Record<SortKey, (a: ManagedTransfer, b: ManagedTransfer) => number> = {
      newest: (a, b) => b.created_at.localeCompare(a.created_at),
      oldest: (a, b) => a.created_at.localeCompare(b.created_at),
      expiring: (a, b) => a.expires_at.localeCompare(b.expires_at),
      largest: (a, b) => b.total_size - a.total_size,
      downloads: (a, b) => b.download_count - a.download_count,
      name: (a, b) => transferTitle(a).localeCompare(transferTitle(b)),
    }
    return [...list].sort(by[sort])
  }, [data, query, filter, sort, clock])

  const toggle = (pin: string) =>
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(pin)) next.delete(pin)
      else next.add(pin)
      return next
    })

  const allVisibleSelected = visible.length > 0 && visible.every(t => selected.has(t.pin))
  const toggleAll = () =>
    setSelected(allVisibleSelected ? new Set() : new Set(visible.map(t => t.pin)))

  const bulk = async (action: (pin: string) => Promise<unknown>) => {
    setBusy(true)
    const results = await Promise.allSettled([...selected].map(action))
    const failed = results.filter(r => r.status === 'rejected') as PromiseRejectedResult[]
    if (failed.length) handleError(new Error(`${failed.length} of ${results.length} failed: ${failed[0].reason?.message ?? ''}`))
    if (failed.some(f => f.reason instanceof UnauthorizedError)) onUnauthorized()
    setSelected(new Set())
    setBusy(false)
    load()
  }

  if (selectedPin) {
    return (
      <TransferDetail
        pin={selectedPin}
        onBack={() => onSelect(null)}
        onPinChange={pin => onSelect(pin, true)}
        onDeleted={() => onSelect(null, true)}
        onUnauthorized={onUnauthorized}
      />
    )
  }

  const stats = data?.stats
  const nextExpiry = stats?.next_expiry ? new Date(stats.next_expiry).getTime() - now : null

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold text-white">Your uploads</h1>
          <p className="text-white/50 mt-1">Every live transfer on this server. Extend, rename, download, or shred them.</p>
        </div>
        <div className="flex items-center gap-2">
          <button onClick={load} className={btn.ghost} disabled={loading} title="Refresh">
            <Icons.Refresh className={`w-4 h-4 ${loading ? 'animate-spin-slow' : ''}`} />
            Refresh
          </button>
          <button onClick={onNewUpload} className={btn.primary}>
            <Icons.Plus className="w-4 h-4" />
            New upload
          </button>
        </div>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <Stat label="Transfers" value={String(stats?.transfers ?? '–')} />
        <Stat label="Files" value={String(stats?.files ?? '–')} />
        <Stat label="Stored" value={stats ? formatBytes(stats.bytes) : '–'} hint="plaintext size" />
        <Stat
          label="Downloads"
          value={String(stats?.downloads ?? '–')}
          hint={nextExpiry !== null ? `next expiry in ${formatRemaining(nextExpiry)}` : undefined}
        />
      </div>

      {error && (
        <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm animate-fade-in">
          {error}
        </div>
      )}

      {/* Toolbar */}
      <div className="glass rounded-2xl p-3 space-y-3">
        <div className="flex flex-col sm:flex-row gap-2">
          <label className="flex-1 flex items-center gap-2 bg-white/5 border border-white/10 rounded-xl px-3 focus-within:border-indigo-500">
            <Icons.Search className="w-4 h-4 text-white/30" />
            <input
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="Search by name, label, or PIN"
              className="flex-1 bg-transparent py-2 text-sm text-white placeholder-white/30 focus:outline-none"
            />
          </label>
          <select
            value={sort}
            onChange={e => setSort(e.target.value as SortKey)}
            className="bg-white/5 border border-white/10 rounded-xl px-3 py-2 text-sm text-white/80 focus:outline-none focus:border-indigo-500"
          >
            {SORTS.map(s => (
              <option key={s.key} value={s.key} className="bg-neutral-900">{s.label}</option>
            ))}
          </select>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {FILTERS.map(f => (
            <button
              key={f.key}
              onClick={() => setFilter(f.key)}
              className={`px-3 py-1 rounded-lg text-xs font-medium transition-all ${
                filter === f.key ? 'bg-indigo-600 text-white' : 'bg-white/5 text-white/50 hover:text-white hover:bg-white/10'
              }`}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      {/* Bulk actions */}
      {selected.size > 0 && (
        <div className="glass rounded-2xl px-4 py-3 flex flex-wrap items-center gap-2 animate-fade-in border-indigo-500/30">
          <span className="text-sm text-white/70 mr-auto">{selected.size} selected</span>
          <button className={btn.ghost} disabled={busy} onClick={() => bulk(p => manageApi.update(p, { extend_secs: 86400 }))}>
            +1 day
          </button>
          <button
            className={btn.ghost}
            disabled={busy}
            onClick={() => bulk(p => manageApi.update(p, { expires_in: data?.max_expiry_secs ?? 604800 }))}
          >
            Extend to max
          </button>
          <ConfirmButton disabled={busy} onConfirm={() => bulk(p => manageApi.remove(p))} confirmLabel={`Delete ${selected.size}?`}>
            <Icons.Trash className="w-4 h-4" />
            Delete
          </ConfirmButton>
          <button className={btn.ghost} onClick={() => setSelected(new Set())}>Clear</button>
        </div>
      )}

      {/* List */}
      <div className="glass rounded-2xl overflow-hidden">
        <div className="px-4 py-2.5 border-b border-white/5 flex items-center gap-3 text-xs text-white/40">
          <input
            type="checkbox"
            checked={allVisibleSelected}
            onChange={toggleAll}
            disabled={visible.length === 0}
            className="accent-indigo-500 w-4 h-4"
            aria-label="Select all"
          />
          <span>
            {data ? `${visible.length} of ${data.transfers.length}` : 'Loading…'}
          </span>
        </div>

        {data && visible.length === 0 && (
          <div className="px-4 py-14 text-center space-y-3">
            <div className="inline-flex w-12 h-12 rounded-2xl bg-white/5 text-white/30 items-center justify-center">
              <Icons.Stack className="w-6 h-6" />
            </div>
            <p className="text-white/50 text-sm">
              {data.transfers.length === 0 ? 'Nothing stored right now.' : 'No transfers match.'}
            </p>
            {data.transfers.length === 0 && (
              <button onClick={onNewUpload} className={btn.primary}>Upload something</button>
            )}
          </div>
        )}

        <ul className="divide-y divide-white/5">
          {visible.map(t => {
            const left = new Date(t.expires_at).getTime() - now
            return (
              <li
                key={t.pin}
                onClick={() => onSelect(t.pin)}
                className="px-4 py-3 flex items-center gap-3 hover:bg-white/5 cursor-pointer transition-colors group"
              >
                <input
                  type="checkbox"
                  checked={selected.has(t.pin)}
                  onClick={e => e.stopPropagation()}
                  onChange={() => toggle(t.pin)}
                  className="accent-indigo-500 w-4 h-4 flex-shrink-0"
                  aria-label={`Select ${transferTitle(t)}`}
                />
                <TransferIcon t={t} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 min-w-0">
                    <p className="text-sm font-medium text-white/90 truncate">{transferTitle(t)}</p>
                    {t.has_text && t.files.length > 0 && (
                      <span title="Includes a note"><Icons.Note className="w-3.5 h-3.5 text-white/30 flex-shrink-0" /></span>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1 mt-0.5 text-xs text-white/40">
                    <span className="font-mono text-white/60">{t.pin}</span>
                    <span>·</span>
                    <span>{t.file_count ? `${t.file_count} file${t.file_count !== 1 ? 's' : ''}, ${formatBytes(t.total_size)}` : 'note only'}</span>
                    <span>·</span>
                    <span title={t.created_at}>uploaded {timeAgo(t.created_at, now)}</span>
                    <MethodBadge method={t.upload.method} />
                  </div>
                </div>
                <div className="hidden sm:flex flex-col items-end text-xs flex-shrink-0 w-24">
                  <span className="text-white/70 tabular-nums flex items-center gap-1">
                    <Icons.Download className="w-3.5 h-3.5 text-white/30" />
                    {t.download_count}
                  </span>
                  <span className="text-white/30">
                    {t.last_download_at ? timeAgo(t.last_download_at, now) : 'never'}
                  </span>
                </div>
                <div className="w-24 sm:w-28 flex-shrink-0 space-y-1.5">
                  <p className={`text-xs font-mono text-right tabular-nums ${expiryTone(left)}`}>{formatRemaining(left)}</p>
                  <ExpiryBar createdAt={t.created_at} expiresAt={t.expires_at} now={now} />
                </div>
              </li>
            )
          })}
        </ul>
      </div>
    </div>
  )
}
