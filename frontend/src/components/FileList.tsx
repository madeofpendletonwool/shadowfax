import { useState, useEffect } from 'react'
import type { TransferInfo } from '../App'

interface Props {
  transfer: TransferInfo
  onTransferAnother: () => void
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

function formatCountdown(expiresAt: string): string {
  const exp = new Date(expiresAt)
  const now = new Date()
  const diffMs = exp.getTime() - now.getTime()

  if (diffMs <= 0) return 'Expired'

  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)
  const diffDays = Math.floor(diffHours / 24)

  if (diffDays >= 1) {
    const remainingHours = diffHours % 24
    return `${diffDays}d ${remainingHours}h`
  }
  if (diffHours >= 1) {
    const remainingMins = diffMins % 60
    return `${diffHours}h ${remainingMins}m`
  }
  const remainingSecs = Math.floor((diffMs % 60000) / 1000)
  return `${diffMins}m ${remainingSecs}s`
}

function DownloadIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
    </svg>
  )
}

function ArchiveIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
    </svg>
  )
}

function FileIcon() {
  return (
    <svg className="w-4 h-4 text-indigo-400 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
    </svg>
  )
}

function ClockIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
    </svg>
  )
}

function CopyIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
    </svg>
  )
}

function CheckIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
    </svg>
  )
}

export default function FileList({ transfer, onTransferAnother }: Props) {
  const [countdown, setCountdown] = useState(() => formatCountdown(transfer.expires_at))
  const [downloadingAll, setDownloadingAll] = useState(false)
  const [copiedText, setCopiedText] = useState(false)

  useEffect(() => {
    const interval = setInterval(() => {
      setCountdown(formatCountdown(transfer.expires_at))
    }, 1000)
    return () => clearInterval(interval)
  }, [transfer.expires_at])

  const downloadFile = (filename: string) => {
    const url = `/api/transfer/${transfer.pin}/download/${encodeURIComponent(filename)}`
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
  }

  const downloadAll = () => {
    setDownloadingAll(true)
    const url = `/api/transfer/${transfer.pin}/zip`
    const a = document.createElement('a')
    a.href = url
    a.download = `shadowfax-${transfer.pin}.zip`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => setDownloadingAll(false), 2000)
  }

  const isExpired = new Date(transfer.expires_at) <= new Date()
  const totalSize = transfer.files.reduce((acc, f) => acc + f.size, 0)

  const copyText = async () => {
    if (!transfer.text) return
    await navigator.clipboard.writeText(transfer.text)
    setCopiedText(true)
    setTimeout(() => setCopiedText(false), 2000)
  }

  return (
    <div className="space-y-6 animate-slide-up">
      {/* Header */}
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold text-white">Transfer Ready</h1>
        <p className="text-white/50">
          PIN <span className="font-mono text-white font-semibold">{transfer.pin}</span>
          {transfer.files.length > 0 && (
            <>
              {' '}&mdash; {transfer.files.length} file{transfer.files.length !== 1 ? 's' : ''}
              {' '}({formatBytes(totalSize)})
            </>
          )}
          {transfer.text && !transfer.files.length && (
            <>{' '}&mdash; text snippet</>
          )}
        </p>
      </div>

      {/* Expiry countdown */}
      <div className={`glass rounded-2xl px-4 py-3 flex items-center gap-3 ${isExpired ? 'border-red-500/30' : ''}`}>
        <div className={`flex-shrink-0 ${isExpired ? 'text-red-400' : 'text-amber-400'}`}>
          <ClockIcon />
        </div>
        <p className="text-sm text-white/60">
          {isExpired ? (
            <span className="text-red-400 font-medium">This transfer has expired</span>
          ) : (
            <>Expires in <span className="text-white font-semibold font-mono">{countdown}</span></>
          )}
        </p>
      </div>

      {/* Text content */}
      {transfer.text && (
        <div className="glass rounded-2xl overflow-hidden animate-fade-in">
          <div className="px-4 py-3 border-b border-white/5 flex items-center justify-between">
            <p className="text-sm font-medium text-white/60">Shared Text</p>
            <button
              onClick={copyText}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-200 ${
                copiedText
                  ? 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                  : 'bg-white/10 text-white/70 hover:bg-white/20 hover:text-white'
              }`}
            >
              {copiedText ? <CheckIcon /> : <CopyIcon />}
              {copiedText ? 'Copied!' : 'Copy'}
            </button>
          </div>
          <div className="px-4 py-3">
            <pre className="text-sm text-white/80 whitespace-pre-wrap break-words font-mono bg-white/5 rounded-xl p-4 max-h-64 overflow-y-auto">
              {transfer.text}
            </pre>
          </div>
        </div>
      )}

      {/* Download all button */}
      {transfer.files.length > 1 && !isExpired && (
        <button
          onClick={downloadAll}
          disabled={downloadingAll}
          className="w-full py-3.5 rounded-2xl font-semibold text-sm
            bg-gradient-to-r from-indigo-600 to-violet-600
            hover:from-indigo-500 hover:to-violet-500
            text-white shadow-xl shadow-indigo-500/20
            flex items-center justify-center gap-2
            transition-all duration-200 active:scale-[0.99]
            disabled:opacity-70 disabled:cursor-not-allowed"
        >
          <ArchiveIcon />
          {downloadingAll ? 'Starting download...' : 'Download All (ZIP)'}
        </button>
      )}

      {/* File list */}
      {transfer.files.length > 0 && (
        <div className="glass rounded-2xl overflow-hidden">
          <div className="px-4 py-3 border-b border-white/5 flex items-center justify-between">
            <p className="text-sm font-medium text-white/60">Files</p>
            <p className="text-xs text-white/40">{formatBytes(totalSize)} total</p>
          </div>
          <div className="divide-y divide-white/5">
            {transfer.files.map((file, i) => (
              <div key={i} className="px-4 py-3 flex items-center gap-3 hover:bg-white/5 transition-colors group">
                <FileIcon />
                <span className="flex-1 text-sm text-white/80 truncate min-w-0">{file.name}</span>
                <span className="text-xs text-white/40 flex-shrink-0 mr-2">{formatBytes(file.size)}</span>
                {!isExpired && (
                  <button
                    onClick={() => downloadFile(file.name)}
                    className="flex-shrink-0 p-2 rounded-lg text-white/40
                      hover:text-indigo-400 hover:bg-indigo-500/10
                      opacity-0 group-hover:opacity-100
                      transition-all duration-200"
                    title={`Download ${file.name}`}
                  >
                    <DownloadIcon />
                  </button>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Single file download (if only one file) */}
      {transfer.files.length === 1 && !isExpired && (
        <button
          onClick={() => downloadFile(transfer.files[0].name)}
          className="w-full py-3.5 rounded-2xl font-semibold text-sm
            bg-gradient-to-r from-indigo-600 to-violet-600
            hover:from-indigo-500 hover:to-violet-500
            text-white shadow-xl shadow-indigo-500/20
            flex items-center justify-center gap-2
            transition-all duration-200 active:scale-[0.99]"
        >
          <DownloadIcon />
          Download {transfer.files[0].name}
        </button>
      )}

      {/* Transfer another */}
      <button
        onClick={onTransferAnother}
        className="w-full py-3 rounded-2xl font-medium text-sm text-white/60
          hover:text-white bg-white/5 hover:bg-white/10
          border border-white/10 hover:border-white/20
          transition-all duration-200"
      >
        Access another transfer
      </button>
    </div>
  )
}
