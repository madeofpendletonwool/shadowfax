export interface ManagedFile {
  id: number
  name: string
  size: number
  disk_size?: number
  content_type: string
  encrypted: boolean
  download_count: number
  added_at?: string
  download_url: string
  owner_url: string
}

export interface TransferEvent {
  at: string
  kind: string
  detail?: string
  ip?: string
  user_agent?: string
}

export interface ManagedTransfer {
  pin: string
  label: string
  created_at: string
  expires_at: string
  max_expires_at: string
  share_url: string
  zip_url?: string
  owner_zip_url?: string
  file_count: number
  total_size: number
  disk_size?: number
  has_text: boolean
  text?: string
  encrypted: boolean
  files: ManagedFile[]
  upload: {
    method: string
    auth: string
    ip: string
    user_agent: string
    requested_expiry_secs: number
  }
  extend_count: number
  download_count: number
  last_download_at?: string
  events?: TransferEvent[]
}

export interface ManageList {
  transfers: ManagedTransfer[]
  stats: { transfers: number; files: number; bytes: number; downloads: number; next_expiry?: string }
  server_time: string
  max_expiry_secs: number
}

export class UnauthorizedError extends Error {
  constructor() {
    super('Your session expired. Please sign in again.')
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api/manage${path}`, init)
  if (res.status === 401) throw new UnauthorizedError()
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `Request failed (${res.status})`)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export const manageApi = {
  list: () => request<ManageList>('/transfers'),
  get: (pin: string) => request<ManagedTransfer>(`/transfers/${pin}`),
  update: (pin: string, body: Record<string, unknown>) =>
    request<ManagedTransfer>(`/transfers/${pin}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  remove: (pin: string) => request<void>(`/transfers/${pin}`, { method: 'DELETE' }),
  rotatePin: (pin: string) => request<ManagedTransfer>(`/transfers/${pin}/rotate-pin`, { method: 'POST' }),
  removeFile: (pin: string, id: number) =>
    request<ManagedTransfer | { transfer_deleted: true }>(`/transfers/${pin}/files/${id}`, { method: 'DELETE' }),

  // XHR rather than fetch, for upload progress.
  addFiles(pin: string, files: File[], onProgress: (pct: number) => void): Promise<ManagedTransfer> {
    return new Promise((resolve, reject) => {
      const form = new FormData()
      for (const f of files) form.append('files', f, f.name)
      const xhr = new XMLHttpRequest()
      xhr.open('POST', `/api/manage/transfers/${pin}/files`)
      xhr.upload.addEventListener('progress', e => {
        if (e.lengthComputable) onProgress(Math.round((e.loaded / e.total) * 100))
      })
      xhr.addEventListener('load', () => {
        if (xhr.status === 201) return resolve(JSON.parse(xhr.responseText))
        if (xhr.status === 401) return reject(new UnauthorizedError())
        try {
          reject(new Error(JSON.parse(xhr.responseText).error || 'Upload failed'))
        } catch {
          reject(new Error('Upload failed'))
        }
      })
      xhr.addEventListener('error', () => reject(new Error('Network error')))
      xhr.send(form)
    })
  },
}

// Starts a browser download without navigating away.
export function triggerDownload(url: string, filename?: string) {
  const a = document.createElement('a')
  a.href = url
  if (filename) a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

export function transferTitle(t: ManagedTransfer): string {
  if (t.label) return t.label
  if (t.files.length === 1) return t.files[0].name
  if (t.files.length > 1) return `${t.files[0].name} + ${t.files.length - 1} more`
  return 'Text note'
}
