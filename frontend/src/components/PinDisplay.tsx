import { useState } from 'react'
import type { TransferInfo } from '../App'

interface Props {
  transfer: TransferInfo
  onSendAnother: () => void
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

function formatExpiry(expiresAt: string): string {
  const exp = new Date(expiresAt)
  const now = new Date()
  const diffMs = exp.getTime() - now.getTime()

  if (diffMs <= 0) return 'Expired'

  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)
  const diffDays = Math.floor(diffHours / 24)

  if (diffDays >= 1) return `${diffDays} day${diffDays !== 1 ? 's' : ''}`
  if (diffHours >= 1) return `${diffHours} hour${diffHours !== 1 ? 's' : ''}`
  return `${diffMins} minute${diffMins !== 1 ? 's' : ''}`
}

function CheckIcon() {
  return (
    <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
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

function ShareIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M8.684 13.342C8.886 12.938 9 12.482 9 12c0-.482-.114-.938-.316-1.342m0 2.684a3 3 0 110-2.684m0 2.684l6.632 3.316m-6.632-6l6.632-3.316m0 0a3 3 0 105.367-2.684 3 3 0 00-5.367 2.684zm0 9.316a3 3 0 105.368 2.684 3 3 0 00-5.368-2.684z" />
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

export default function PinDisplay({ transfer, onSendAnother }: Props) {
  const [copied, setCopied] = useState(false)
  const [copiedLink, setCopiedLink] = useState(false)

  const pin = transfer.pin.padStart(4, '0')

  const copyPin = async () => {
    await navigator.clipboard.writeText(pin)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const copyLink = async () => {
    const url = `${window.location.origin}?pin=${pin}`
    await navigator.clipboard.writeText(url)
    setCopiedLink(true)
    setTimeout(() => setCopiedLink(false), 2000)
  }

  return (
    <div className="space-y-6 animate-slide-up">
      {/* Success header */}
      <div className="text-center space-y-2">
        <div className="inline-flex items-center justify-center w-14 h-14 rounded-full bg-emerald-500/20 text-emerald-400 mx-auto mb-2">
          <CheckIcon />
        </div>
        <h1 className="text-3xl font-bold text-white">Files ready!</h1>
        <p className="text-white/50">
          Share this PIN to let others download your files
        </p>
      </div>

      {/* PIN display */}
      <div className="glass rounded-2xl p-8 text-center space-y-4">
        <p className="text-sm text-white/40 uppercase tracking-widest font-medium">Your PIN</p>
        <div className="flex items-center justify-center gap-3">
          {pin.split('').map((digit, i) => (
            <div
              key={i}
              className="w-16 h-20 flex items-center justify-center text-4xl font-mono font-bold
                bg-white/10 border border-white/20 rounded-xl text-white
                shadow-lg shadow-black/20"
            >
              {digit}
            </div>
          ))}
        </div>

        <div className="flex items-center justify-center gap-2 pt-2">
          <button
            onClick={copyPin}
            className={`flex items-center gap-2 px-4 py-2 rounded-xl text-sm font-medium transition-all duration-200 ${
              copied
                ? 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                : 'bg-white/10 text-white/70 hover:bg-white/20 hover:text-white'
            }`}
          >
            {copied ? <CheckIcon /> : <CopyIcon />}
            {copied ? 'Copied!' : 'Copy PIN'}
          </button>
          <button
            onClick={copyLink}
            className={`flex items-center gap-2 px-4 py-2 rounded-xl text-sm font-medium transition-all duration-200 ${
              copiedLink
                ? 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                : 'bg-white/10 text-white/70 hover:bg-white/20 hover:text-white'
            }`}
          >
            {copiedLink ? <CheckIcon /> : <ShareIcon />}
            {copiedLink ? 'Copied!' : 'Share link'}
          </button>
        </div>
      </div>

      {/* Expiry info */}
      <div className="glass rounded-2xl px-4 py-3 flex items-center gap-3">
        <div className="w-2 h-2 rounded-full bg-emerald-400 flex-shrink-0" />
        <p className="text-sm text-white/60">
          Expires in <span className="text-white font-medium">{formatExpiry(transfer.expires_at)}</span>
        </p>
      </div>

      {/* File list */}
      <div className="glass rounded-2xl overflow-hidden">
        <div className="px-4 py-3 border-b border-white/5">
          <p className="text-sm font-medium text-white/60">
            {transfer.files.length} file{transfer.files.length !== 1 ? 's' : ''} uploaded
          </p>
        </div>
        <div className="divide-y divide-white/5">
          {transfer.files.map((file, i) => (
            <div key={i} className="px-4 py-3 flex items-center gap-3">
              <FileIcon />
              <span className="flex-1 text-sm text-white/80 truncate">{file.name}</span>
              <span className="text-xs text-white/40 flex-shrink-0">{formatBytes(file.size)}</span>
            </div>
          ))}
        </div>
      </div>

      {/* Send another */}
      <button
        onClick={onSendAnother}
        className="w-full py-3 rounded-2xl font-medium text-sm text-white/60
          hover:text-white bg-white/5 hover:bg-white/10
          border border-white/10 hover:border-white/20
          transition-all duration-200"
      >
        Send another transfer
      </button>
    </div>
  )
}
