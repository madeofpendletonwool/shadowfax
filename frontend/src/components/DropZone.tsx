import { useState, useRef, useCallback, DragEvent, ChangeEvent } from 'react'
import type { TransferInfo } from '../App'

interface Props {
  onUploadSuccess: (info: TransferInfo) => void
}

const EXPIRY_OPTIONS = [
  { label: '1 hour', value: 3600 },
  { label: '6 hours', value: 21600 },
  { label: '24 hours', value: 86400 },
  { label: '3 days', value: 259200 },
  { label: '7 days', value: 604800 },
]

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

function UploadIcon() {
  return (
    <svg className="w-10 h-10" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5m-13.5-9L12 3m0 0l4.5 4.5M12 3v13.5" />
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

function XIcon() {
  return (
    <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
    </svg>
  )
}

export default function DropZone({ onUploadSuccess }: Props) {
  const [files, setFiles] = useState<File[]>([])
  const [isDragOver, setIsDragOver] = useState(false)
  const [expiry, setExpiry] = useState(3600)
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [text, setText] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const addFiles = useCallback((newFiles: FileList | File[]) => {
    const arr = Array.from(newFiles)
    setFiles(prev => {
      const existing = new Set(prev.map(f => f.name + f.size))
      const filtered = arr.filter(f => !existing.has(f.name + f.size))
      return [...prev, ...filtered]
    })
    setError(null)
  }, [])

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
    setIsDragOver(true)
  }

  const handleDragLeave = (e: DragEvent) => {
    e.preventDefault()
    setIsDragOver(false)
  }

  const handleDrop = (e: DragEvent) => {
    e.preventDefault()
    setIsDragOver(false)
    if (e.dataTransfer.files.length > 0) {
      addFiles(e.dataTransfer.files)
    }
  }

  const handleFileInput = (e: ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files.length > 0) {
      addFiles(e.target.files)
      e.target.value = ''
    }
  }

  const removeFile = (index: number) => {
    setFiles(prev => prev.filter((_, i) => i !== index))
  }

  const handleUpload = async () => {
    if (files.length === 0 && text.trim() === '') return
    setUploading(true)
    setProgress(0)
    setError(null)

    const formData = new FormData()
    for (const file of files) {
      formData.append('files', file, file.name)
    }
    if (text.trim()) {
      formData.append('text', text.trim())
    }

    try {
      await new Promise<void>((resolve, reject) => {
        const xhr = new XMLHttpRequest()
        xhr.open('POST', `/api/transfer?expiry=${expiry}`)

        xhr.upload.addEventListener('progress', (e) => {
          if (e.lengthComputable) {
            setProgress(Math.round((e.loaded / e.total) * 100))
          }
        })

        xhr.addEventListener('load', () => {
          if (xhr.status === 201) {
            const data = JSON.parse(xhr.responseText) as TransferInfo
            onUploadSuccess(data)
            resolve()
          } else {
            try {
              const err = JSON.parse(xhr.responseText)
              reject(new Error(err.error || 'Upload failed'))
            } catch {
              reject(new Error('Upload failed'))
            }
          }
        })

        xhr.addEventListener('error', () => reject(new Error('Network error')))
        xhr.send(formData)
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Upload failed')
      setUploading(false)
      setProgress(0)
    }
  }

  const totalSize = files.reduce((acc, f) => acc + f.size, 0)
  const hasContent = files.length > 0 || text.trim().length > 0

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold text-white">Send Files</h1>
        <p className="text-white/50">Drop your files and share with a 4-digit PIN</p>
      </div>

      {/* Text input */}
      <div className="glass rounded-2xl p-4 space-y-3">
        <p className="text-sm font-medium text-white/60">Quick Text</p>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Paste or type text to share between devices..."
          disabled={uploading}
          className="w-full h-28 bg-white/5 border border-white/10 rounded-xl px-4 py-3
            text-sm text-white/90 placeholder-white/30
            focus:outline-none focus:border-indigo-500/50 focus:ring-1 focus:ring-indigo-500/30
            resize-y transition-all duration-200 disabled:opacity-50"
        />
      </div>

      {/* Drop zone */}
      <div
        onClick={() => !uploading && fileInputRef.current?.click()}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        className={`
          relative rounded-2xl border-2 border-dashed p-10 text-center cursor-pointer
          transition-all duration-200 select-none
          ${isDragOver
            ? 'border-indigo-500 bg-indigo-500/10 animate-pulse-glow'
            : 'border-white/20 hover:border-white/40 hover:bg-white/5'
          }
          ${uploading ? 'pointer-events-none opacity-70' : ''}
        `}
      >
        <input
          ref={fileInputRef}
          type="file"
          multiple
          className="hidden"
          onChange={handleFileInput}
          disabled={uploading}
        />

        <div className={`flex flex-col items-center gap-4 transition-all duration-200 ${isDragOver ? 'scale-105' : ''}`}>
          <div className={`p-4 rounded-2xl ${isDragOver ? 'bg-indigo-500/20 text-indigo-400' : 'bg-white/5 text-white/40'}`}>
            <UploadIcon />
          </div>
          {isDragOver ? (
            <div>
              <p className="text-lg font-medium text-indigo-400">Drop to add files</p>
            </div>
          ) : (
            <div>
              <p className="text-base font-medium text-white/80">
                Drag & drop files here, or <span className="text-indigo-400">browse</span>
              </p>
              <p className="text-sm text-white/30 mt-1">Any file type, any size</p>
            </div>
          )}
        </div>
      </div>

      {/* File list */}
      {files.length > 0 && (
        <div className="glass rounded-2xl divide-y divide-white/5 overflow-hidden animate-fade-in">
          <div className="px-4 py-3 flex items-center justify-between text-sm">
            <span className="text-white/60">
              {files.length} file{files.length !== 1 ? 's' : ''} &mdash; {formatBytes(totalSize)}
            </span>
            <button
              onClick={() => setFiles([])}
              className="text-white/40 hover:text-white/80 text-xs transition-colors"
              disabled={uploading}
            >
              Clear all
            </button>
          </div>
          {files.map((file, index) => (
            <div key={`${file.name}-${index}`} className="px-4 py-3 flex items-center gap-3 hover:bg-white/5 transition-colors">
              <FileIcon />
              <span className="flex-1 text-sm text-white/80 truncate">{file.name}</span>
              <span className="text-xs text-white/40 flex-shrink-0">{formatBytes(file.size)}</span>
              {!uploading && (
                <button
                  onClick={(e) => { e.stopPropagation(); removeFile(index) }}
                  className="p-1 rounded-md text-white/30 hover:text-white/80 hover:bg-white/10 transition-all flex-shrink-0"
                >
                  <XIcon />
                </button>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Expiry selector */}
      <div className="glass rounded-2xl p-4 space-y-3">
        <p className="text-sm font-medium text-white/60">Expires in</p>
        <div className="flex flex-wrap gap-2">
          {EXPIRY_OPTIONS.map(opt => (
            <button
              key={opt.value}
              onClick={() => setExpiry(opt.value)}
              disabled={uploading}
              className={`
                px-4 py-2 rounded-xl text-sm font-medium transition-all duration-200
                ${expiry === opt.value
                  ? 'bg-indigo-600 text-white shadow-lg shadow-indigo-500/20'
                  : 'bg-white/5 text-white/60 hover:bg-white/10 hover:text-white'
                }
                disabled:opacity-50 disabled:cursor-not-allowed
              `}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </div>

      {/* Error */}
      {error && (
        <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm animate-fade-in">
          {error}
        </div>
      )}

      {/* Upload progress */}
      {uploading && (
        <div className="space-y-2 animate-fade-in">
          <div className="flex items-center justify-between text-sm text-white/60">
            <span>Uploading...</span>
            <span>{progress}%</span>
          </div>
          <div className="h-2 bg-white/10 rounded-full overflow-hidden">
            <div
              className="h-full bg-gradient-to-r from-indigo-500 to-violet-500 rounded-full transition-all duration-300"
              style={{ width: `${progress}%` }}
            />
          </div>
        </div>
      )}

      {/* Upload button */}
      <button
        onClick={handleUpload}
        disabled={!hasContent || uploading}
        className={`
          w-full py-4 rounded-2xl font-semibold text-base transition-all duration-200
          ${hasContent && !uploading
            ? 'bg-gradient-to-r from-indigo-600 to-violet-600 hover:from-indigo-500 hover:to-violet-500 text-white shadow-xl shadow-indigo-500/20 hover:shadow-indigo-500/30 active:scale-[0.99]'
            : 'bg-white/10 text-white/30 cursor-not-allowed'
          }
        `}
      >
        {uploading ? (
          <span className="flex items-center justify-center gap-2">
            <svg className="w-4 h-4 animate-spin-slow" fill="none" viewBox="0 0 24 24">
              <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
              <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
            </svg>
            Uploading...
          </span>
        ) : (
          `Create Transfer${files.length > 0 ? ` (${files.length} file${files.length !== 1 ? 's' : ''})` : ''}`
        )}
      </button>
    </div>
  )
}
