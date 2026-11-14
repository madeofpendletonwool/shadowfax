import { DragEvent, useCallback, useEffect, useRef, useState } from 'react'
import { manageApi, ManagedTransfer, TransferEvent, transferTitle, triggerDownload, UnauthorizedError } from '../manageApi'
import {
  AUTH_METHODS,
  describeAgent,
  formatBytes,
  formatDateTime,
  formatDuration,
  formatRemaining,
  timeAgo,
  toLocalInput,
  UPLOAD_METHODS,
} from '../format'
import { btn, Card, ConfirmButton, ExpiryBar, expiryTone, Icons, useCopy, useNow } from './ui'

interface Props {
  pin: string
  onBack: () => void
  onPinChange: (pin: string) => void
  onDeleted: () => void
  onUnauthorized: () => void
}

const EXTEND_OPTIONS = [
  { label: '+1h', secs: 3600 },
  { label: '+6h', secs: 21600 },
  { label: '+1d', secs: 86400 },
  { label: '+3d', secs: 259200 },
]

const EVENT_META: Record<string, { label: string; tone: string; Icon: (p: { className?: string }) => JSX.Element }> = {
  created: { label: 'Uploaded', tone: 'text-emerald-400 bg-emerald-500/10', Icon: Icons.Upload },
  opened: { label: 'PIN entered', tone: 'text-sky-300 bg-sky-500/10', Icon: Icons.Eye },
  download: { label: 'Downloaded', tone: 'text-indigo-300 bg-indigo-500/10', Icon: Icons.Download },
  download_interrupted: { label: 'Download interrupted', tone: 'text-amber-400 bg-amber-500/10', Icon: Icons.Download },
  owner_download: { label: 'You downloaded', tone: 'text-white/60 bg-white/5', Icon: Icons.Download },
  expiry_changed: { label: 'Expiry changed', tone: 'text-amber-300 bg-amber-500/10', Icon: Icons.Clock },
  renamed: { label: 'Renamed', tone: 'text-white/60 bg-white/5', Icon: Icons.Pencil },
  note_edited: { label: 'Note edited', tone: 'text-white/60 bg-white/5', Icon: Icons.Note },
  pin_rotated: { label: 'PIN rotated', tone: 'text-violet-300 bg-violet-500/10', Icon: Icons.Key },
  files_added: { label: 'Files added', tone: 'text-emerald-400 bg-emerald-500/10', Icon: Icons.Plus },
  file_deleted: { label: 'File deleted', tone: 'text-red-400 bg-red-500/10', Icon: Icons.Trash },
}

function Row({ label, children, title }: { label: string; children: React.ReactNode; title?: string }) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-2.5 text-sm">
      <dt className="text-white/40 flex-shrink-0">{label}</dt>
      <dd className="text-white/80 text-right min-w-0 break-words" title={title}>{children}</dd>
    </div>
  )
}

function EventItem({ e, now }: { e: TransferEvent; now: number }) {
  const meta = EVENT_META[e.kind] ?? { label: e.kind, tone: 'text-white/60 bg-white/5', Icon: Icons.Sparkle }
  return (
    <li className="px-4 py-3 flex gap-3">
      <div className={`w-8 h-8 rounded-lg flex items-center justify-center flex-shrink-0 ${meta.tone}`}>
        <meta.Icon className="w-4 h-4" />
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline justify-between gap-3">
          <p className="text-sm text-white/85">{meta.label}</p>
          <time className="text-xs text-white/35 flex-shrink-0" title={formatDateTime(e.at)}>{timeAgo(e.at, now)}</time>
        </div>
        {e.detail && <p className="text-xs text-white/50 mt-0.5 break-words">{e.detail}</p>}
        {(e.ip || e.user_agent) && (
          <p className="text-xs text-white/30 mt-0.5 truncate" title={e.user_agent}>
            {[e.ip, e.user_agent ? describeAgent(e.user_agent) : ''].filter(Boolean).join(' · ')}
          </p>
        )}
      </div>
    </li>
  )
}

export default function TransferDetail({ pin, onBack, onPinChange, onDeleted, onUnauthorized }: Props) {
  const [t, setT] = useState<ManagedTransfer | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [editingLabel, setEditingLabel] = useState(false)
  const [labelDraft, setLabelDraft] = useState('')
  const [editingNote, setEditingNote] = useState(false)
  const [noteDraft, setNoteDraft] = useState('')
  const [customExpiry, setCustomExpiry] = useState('')
  const [uploadPct, setUploadPct] = useState<number | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [showAllEvents, setShowAllEvents] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const [copied, copy] = useCopy()
  const now = useNow()

  const fail = useCallback(
    (err: unknown) => {
      if (err instanceof UnauthorizedError) onUnauthorized()
      setError(err instanceof Error ? err.message : 'Something went wrong')
    },
    [onUnauthorized]
  )

  const load = useCallback(async () => {
    try {
      setT(await manageApi.get(pin))
      setError(null)
    } catch (err) {
      fail(err)
    }
  }, [pin, fail])

  useEffect(() => {
    load()
    const id = setInterval(load, 15_000)
    return () => clearInterval(id)
  }, [load])

  // Expired while open: go back rather than showing a dead page.
  useEffect(() => {
    if (t && new Date(t.expires_at).getTime() <= now) onDeleted()
  }, [t, now, onDeleted])

  const run = async (fn: () => Promise<ManagedTransfer | void>, message?: string) => {
    setBusy(true)
    setError(null)
    try {
      const next = await fn()
      if (next) {
        setT(next)
        if (next.pin !== pin) onPinChange(next.pin)
      }
      if (message) {
        setNotice(message)
        setTimeout(() => setNotice(null), 2500)
      }
    } catch (err) {
      fail(err)
    }
    setBusy(false)
  }

  const setExpiry = (body: Record<string, unknown>, msg: string) =>
    run(async () => manageApi.update(pin, body), msg)

  const uploadFiles = async (files: File[]) => {
    if (!files.length) return
    setUploadPct(0)
    setError(null)
    try {
      setT(await manageApi.addFiles(pin, files, setUploadPct))
      setNotice(`${files.length} file${files.length !== 1 ? 's' : ''} added`)
      setTimeout(() => setNotice(null), 2500)
    } catch (err) {
      fail(err)
    }
    setUploadPct(null)
  }

  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    uploadFiles(Array.from(e.dataTransfer.files))
  }

  const deleteFile = (id: number) =>
    run(async () => {
      const res = await manageApi.removeFile(pin, id)
      if ('transfer_deleted' in res) {
        onDeleted()
        return
      }
      return res
    }, 'File deleted')

  if (!t) {
    return (
      <div className="space-y-4">
        <button onClick={onBack} className={btn.ghost}><Icons.Back className="w-4 h-4" />All uploads</button>
        {error ? (
          <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm">{error}</div>
        ) : (
          <div className="glass rounded-2xl h-64 animate-pulse" />
        )}
      </div>
    )
  }

  const left = new Date(t.expires_at).getTime() - now
  const maxLeft = new Date(t.max_expires_at).getTime()
  const atMax = maxLeft - new Date(t.expires_at).getTime() < 60e3
  const lifetime = (new Date(t.expires_at).getTime() - new Date(t.created_at).getTime()) / 1000
  const method = UPLOAD_METHODS[t.upload.method]
  const events = t.events ?? []
  const shownEvents = showAllEvents ? events : events.slice(0, 8)
  const recipients = events.filter(e => e.kind === 'download' || e.kind === 'opened')
  const uniqueIPs = new Set(recipients.map(e => e.ip).filter(Boolean)).size
  const overhead = t.disk_size && t.total_size ? t.disk_size - t.total_size : 0

  return (
    <div className="space-y-5 animate-fade-in">
      <button onClick={onBack} className={btn.ghost}><Icons.Back className="w-4 h-4" />All uploads</button>

      {/* Title + PIN */}
      <div className="glass rounded-2xl p-5 space-y-4">
        <div className="flex items-start gap-3">
          <div className="flex-1 min-w-0">
            {editingLabel ? (
              <form
                className="flex gap-2"
                onSubmit={e => {
                  e.preventDefault()
                  setEditingLabel(false)
                  run(async () => manageApi.update(pin, { label: labelDraft }), 'Renamed')
                }}
              >
                <input
                  autoFocus
                  value={labelDraft}
                  maxLength={120}
                  onChange={e => setLabelDraft(e.target.value)}
                  onKeyDown={e => e.key === 'Escape' && setEditingLabel(false)}
                  placeholder={t.files[0]?.name ?? 'Label'}
                  className="flex-1 bg-white/5 border border-white/10 rounded-lg px-3 py-1.5 text-lg text-white focus:outline-none focus:border-indigo-500"
                />
                <button className={btn.primary} type="submit">Save</button>
                <button className={btn.ghost} type="button" onClick={() => setEditingLabel(false)}>Cancel</button>
              </form>
            ) : (
              <button
                onClick={() => { setLabelDraft(t.label); setEditingLabel(true) }}
                className="group flex items-center gap-2 text-left max-w-full"
                title="Rename"
              >
                <h1 className="text-2xl font-bold text-white truncate">{transferTitle(t)}</h1>
                <Icons.Pencil className="w-4 h-4 text-white/20 group-hover:text-white/60 flex-shrink-0" />
              </button>
            )}
            <p className="text-sm text-white/40 mt-1">
              Uploaded {formatDateTime(t.created_at)} · {t.file_count} file{t.file_count !== 1 ? 's' : ''}
              {t.total_size > 0 && ` · ${formatBytes(t.total_size)}`}
              {t.has_text && ' · note'}
            </p>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-1.5">
            {t.pin.split('').map((d, i) => (
              <span key={i} className="w-9 h-11 flex items-center justify-center font-mono text-xl font-bold bg-white/10 border border-white/15 rounded-lg text-white">
                {d}
              </span>
            ))}
          </div>
          <div className="flex flex-wrap gap-2">
            <button className={btn.ghost} onClick={() => copy('pin', t.pin)}>
              {copied === 'pin' ? <Icons.Check className="w-4 h-4 text-emerald-400" /> : <Icons.Copy className="w-4 h-4" />}
              {copied === 'pin' ? 'Copied' : 'Copy PIN'}
            </button>
            <button className={btn.ghost} onClick={() => copy('link', t.share_url)}>
              {copied === 'link' ? <Icons.Check className="w-4 h-4 text-emerald-400" /> : <Icons.Link className="w-4 h-4" />}
              {copied === 'link' ? 'Copied' : 'Share link'}
            </button>
            <ConfirmButton
              className={btn.ghost}
              disabled={busy}
              title="Issue a new PIN. The current PIN and share link stop working immediately."
              confirmLabel="Revoke current PIN?"
              onConfirm={() => run(() => manageApi.rotatePin(pin), 'New PIN issued')}
            >
              <Icons.Key className="w-4 h-4" />
              New PIN
            </ConfirmButton>
          </div>
        </div>
      </div>

      {error && (
        <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm animate-fade-in">{error}</div>
      )}
      {notice && (
        <div className="bg-emerald-500/10 border border-emerald-500/20 rounded-xl px-4 py-2.5 text-emerald-400 text-sm animate-fade-in">{notice}</div>
      )}

      <div className="grid lg:grid-cols-5 gap-5 items-start">
        <div className="lg:col-span-3 space-y-5">
          {/* Expiry */}
          <Card title="Time remaining">
            <div className="p-4 space-y-4">
              <div className="flex items-end justify-between gap-3">
                <p className={`text-3xl font-semibold font-mono tabular-nums ${expiryTone(left)}`}>{formatRemaining(left)}</p>
                <p className="text-xs text-white/40 text-right">
                  expires {formatDateTime(t.expires_at)}
                  <br />
                  {t.extend_count > 0 ? `extended ${t.extend_count}×` : 'never extended'}
                </p>
              </div>
              <ExpiryBar createdAt={t.created_at} expiresAt={t.expires_at} now={now} />
              <div className="flex flex-wrap gap-2">
                {EXTEND_OPTIONS.map(o => (
                  <button
                    key={o.secs}
                    className={btn.ghost}
                    disabled={busy || atMax}
                    onClick={() => setExpiry({ extend_secs: o.secs }, `Extended ${o.label.slice(1)}`)}
                  >
                    {o.label}
                  </button>
                ))}
                <button
                  className={btn.primary}
                  disabled={busy || atMax}
                  onClick={() => setExpiry({ expires_at: t.max_expires_at }, 'Extended to the maximum')}
                  title={`Server allows up to ${formatDuration((maxLeft - now) / 1000)} from now`}
                >
                  Max
                </button>
              </div>
              <form
                className="flex flex-wrap gap-2 items-center"
                onSubmit={e => {
                  e.preventDefault()
                  if (!customExpiry) return
                  setExpiry({ expires_at: new Date(customExpiry).toISOString() }, 'Expiry updated')
                  setCustomExpiry('')
                }}
              >
                <input
                  type="datetime-local"
                  value={customExpiry}
                  min={toLocalInput(new Date(now + 120e3))}
                  max={toLocalInput(new Date(maxLeft))}
                  onChange={e => setCustomExpiry(e.target.value)}
                  className="bg-white/5 border border-white/10 rounded-lg px-3 py-1.5 text-sm text-white/80 focus:outline-none focus:border-indigo-500 [color-scheme:dark]"
                />
                <button type="submit" className={btn.ghost} disabled={!customExpiry || busy}>Set exact time</button>
              </form>
              <p className="text-xs text-white/30">
                Earlier times shorten the transfer. The server caps expiry at {formatDuration((maxLeft - now) / 1000)} from now
                {atMax && ' — this transfer is already there'}.
              </p>
            </div>
          </Card>

          {/* Files */}
          <Card
            title={`Files${t.file_count ? ` · ${formatBytes(t.total_size)}` : ''}`}
            actions={
              t.owner_zip_url && t.files.length > 1 ? (
                <button className={btn.ghost} onClick={() => triggerDownload(t.owner_zip_url!)}>
                  <Icons.Stack className="w-4 h-4" />
                  Download ZIP
                </button>
              ) : undefined
            }
          >
            {t.files.length > 0 && (
              <ul className="divide-y divide-white/5">
                {t.files.map(f => (
                  <li key={f.id} className="px-4 py-3 flex items-center gap-3 group">
                    <Icons.File className="w-4 h-4 text-indigo-400 flex-shrink-0" />
                    <div className="flex-1 min-w-0">
                      <p className="text-sm text-white/85 truncate" title={f.name}>{f.name}</p>
                      <p className="text-xs text-white/35 truncate">
                        {formatBytes(f.size)} · {f.content_type} · {f.download_count} download{f.download_count !== 1 ? 's' : ''}
                        {f.added_at && f.added_at.slice(0, 19) !== t.created_at.slice(0, 19) && ` · added ${timeAgo(f.added_at, now)}`}
                        {!f.encrypted && ' · not encrypted (legacy)'}
                      </p>
                    </div>
                    <div className="flex items-center gap-1 flex-shrink-0">
                      <button className={btn.icon} title="Download" onClick={() => triggerDownload(f.owner_url, f.name)}>
                        <Icons.Download className="w-4 h-4" />
                      </button>
                      <button className={btn.icon} title="Copy direct download link (works for anyone with it)" onClick={() => copy(`f${f.id}`, f.download_url)}>
                        {copied === `f${f.id}` ? <Icons.Check className="w-4 h-4 text-emerald-400" /> : <Icons.Link className="w-4 h-4" />}
                      </button>
                      <ConfirmButton
                        className={btn.icon}
                        title="Delete this file"
                        confirmLabel={<span className="text-xs px-1">Delete?</span>}
                        disabled={busy}
                        onConfirm={() => deleteFile(f.id)}
                      >
                        <Icons.Trash className="w-4 h-4" />
                      </ConfirmButton>
                    </div>
                  </li>
                ))}
              </ul>
            )}
            <div
              onClick={() => uploadPct === null && fileInput.current?.click()}
              onDragOver={e => { e.preventDefault(); setDragOver(true) }}
              onDragLeave={() => setDragOver(false)}
              onDrop={onDrop}
              className={`m-3 rounded-xl border-2 border-dashed px-4 py-5 text-center text-sm cursor-pointer transition-all ${
                dragOver ? 'border-indigo-500 bg-indigo-500/10 text-indigo-300' : 'border-white/10 text-white/40 hover:border-white/25 hover:text-white/60'
              }`}
            >
              <input
                ref={fileInput}
                type="file"
                multiple
                className="hidden"
                onChange={e => { uploadFiles(Array.from(e.target.files ?? [])); e.target.value = '' }}
              />
              {uploadPct !== null ? (
                <div className="space-y-2">
                  <p>Uploading… {uploadPct}%</p>
                  <div className="h-1.5 bg-white/10 rounded-full overflow-hidden">
                    <div className="h-full bg-indigo-500 transition-all" style={{ width: `${uploadPct}%` }} />
                  </div>
                </div>
              ) : (
                <span className="inline-flex items-center gap-2"><Icons.Plus className="w-4 h-4" />Drop files or click to add them to this transfer</span>
              )}
            </div>
          </Card>

          {/* Note */}
          <Card
            title="Note"
            actions={
              !editingNote && (
                <>
                  {t.text && (
                    <button className={btn.ghost} onClick={() => copy('note', t.text!)}>
                      {copied === 'note' ? <Icons.Check className="w-4 h-4 text-emerald-400" /> : <Icons.Copy className="w-4 h-4" />}
                      Copy
                    </button>
                  )}
                  <button className={btn.ghost} onClick={() => { setNoteDraft(t.text ?? ''); setEditingNote(true) }}>
                    <Icons.Pencil className="w-4 h-4" />
                    {t.text ? 'Edit' : 'Add'}
                  </button>
                </>
              )
            }
          >
            <div className="p-4">
              {editingNote ? (
                <div className="space-y-3">
                  <textarea
                    autoFocus
                    value={noteDraft}
                    onChange={e => setNoteDraft(e.target.value)}
                    className="w-full h-36 bg-white/5 border border-white/10 rounded-xl px-3 py-2 text-sm text-white/90 font-mono focus:outline-none focus:border-indigo-500 resize-y"
                  />
                  <div className="flex gap-2 justify-end">
                    <button className={btn.ghost} onClick={() => setEditingNote(false)}>Cancel</button>
                    <button
                      className={btn.primary}
                      disabled={busy}
                      onClick={() => {
                        setEditingNote(false)
                        run(async () => manageApi.update(pin, { text: noteDraft.trim() }), noteDraft.trim() ? 'Note saved' : 'Note removed')
                      }}
                    >
                      Save
                    </button>
                  </div>
                  <p className="text-xs text-white/30">Saving re-encrypts the note under a fresh key; the old one is shredded.</p>
                </div>
              ) : t.text ? (
                <pre className="text-sm text-white/75 whitespace-pre-wrap break-words font-mono bg-white/5 rounded-xl p-3 max-h-64 overflow-y-auto">{t.text}</pre>
              ) : (
                <p className="text-sm text-white/30">No note attached.</p>
              )}
            </div>
          </Card>
        </div>

        <div className="lg:col-span-2 space-y-5">
          {/* Reach */}
          <div className="grid grid-cols-2 gap-3">
            <div className="glass rounded-2xl px-4 py-3">
              <p className="text-xs text-white/40 uppercase tracking-wider">Downloads</p>
              <p className="text-2xl font-semibold text-white tabular-nums">{t.download_count}</p>
              <p className="text-xs text-white/35 truncate">{t.last_download_at ? `last ${timeAgo(t.last_download_at, now)}` : 'not yet'}</p>
            </div>
            <div className="glass rounded-2xl px-4 py-3">
              <p className="text-xs text-white/40 uppercase tracking-wider">Recipients</p>
              <p className="text-2xl font-semibold text-white tabular-nums">{uniqueIPs}</p>
              <p className="text-xs text-white/35">unique IPs</p>
            </div>
          </div>

          {/* Provenance */}
          <Card title="How it was uploaded">
            <dl className="divide-y divide-white/5">
              <Row label="Method">
                {method ? method.label : 'Unknown (uploaded before tracking)'}
                {method && <span className="block text-xs text-white/40 font-mono mt-0.5">{method.detail}</span>}
              </Row>
              <Row label="Auth">{AUTH_METHODS[t.upload.auth] ?? 'Unknown'}</Row>
              <Row label="Client" title={t.upload.user_agent}>{t.upload.user_agent ? describeAgent(t.upload.user_agent) : '—'}</Row>
              <Row label="From IP"><span className="font-mono text-xs">{t.upload.ip || '—'}</span></Row>
              <Row label="Uploaded">{formatDateTime(t.created_at)}</Row>
              <Row label="Asked to keep">{t.upload.requested_expiry_secs ? formatDuration(t.upload.requested_expiry_secs) : '—'}</Row>
              <Row label="Total lifetime">{formatDuration(lifetime)}</Row>
            </dl>
          </Card>

          {/* Storage */}
          <Card title="Storage">
            <dl className="divide-y divide-white/5">
              <Row label="Encryption">
                <span className="inline-flex items-center gap-1.5">
                  <Icons.Lock className={`w-3.5 h-3.5 ${t.encrypted ? 'text-emerald-400' : 'text-amber-400'}`} />
                  {t.encrypted ? 'AES-256-GCM, per-file keys' : 'Partly plaintext (legacy upload)'}
                </span>
              </Row>
              <Row label="Plaintext size">{formatBytes(t.total_size)}</Row>
              <Row label="On disk">{t.disk_size ? formatBytes(t.disk_size) : '—'}{overhead > 0 && <span className="text-white/35"> (+{formatBytes(overhead)} auth tags)</span>}</Row>
              <Row label="On expiry">Files unlinked, keys destroyed</Row>
            </dl>
          </Card>

          {/* Activity */}
          <Card title={`Activity${events.length ? ` · ${events.length}` : ''}`}>
            {events.length === 0 ? (
              <p className="px-4 py-6 text-sm text-white/30 text-center">No activity recorded yet.</p>
            ) : (
              <>
                <ul className="divide-y divide-white/5">
                  {shownEvents.map((e, i) => <EventItem key={i} e={e} now={now} />)}
                </ul>
                {events.length > shownEvents.length && (
                  <button onClick={() => setShowAllEvents(true)} className="w-full py-2.5 text-xs text-white/50 hover:text-white border-t border-white/5">
                    Show all {events.length}
                  </button>
                )}
              </>
            )}
          </Card>

          {/* Danger */}
          <Card title="Danger zone">
            <div className="p-4 space-y-3">
              <p className="text-xs text-white/40">
                Deleting removes the files and destroys their keys immediately. The PIN stops working and this can't be undone.
              </p>
              <ConfirmButton
                className={`${btn.danger} w-full py-2.5`}
                disabled={busy}
                confirmLabel="Really delete? Click again"
                onConfirm={() => run(async () => { await manageApi.remove(pin); onDeleted() })}
              >
                <Icons.Trash className="w-4 h-4" />
                Delete transfer now
              </ConfirmButton>
            </div>
          </Card>
        </div>
      </div>
    </div>
  )
}
