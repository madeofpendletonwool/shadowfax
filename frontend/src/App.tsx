import { useState, useEffect } from 'react'
import DropZone from './components/DropZone'
import PinDisplay from './components/PinDisplay'
import PinEntry from './components/PinEntry'
import FileList from './components/FileList'
import Login from './components/Login'
import Manage from './components/Manage'
import { useSession } from './config'

export type View = 'send' | 'receive' | 'uploads'

// The uploads view lives in the URL fragment (#uploads, #uploads/<pin>) so a
// refresh keeps your place, and a PIN never reaches the server's access log.
function readHash(): { uploads: boolean; pin: string | null } {
  const m = window.location.hash.match(/^#uploads(?:\/(\d+))?$/)
  return m ? { uploads: true, pin: m[1] ?? null } : { uploads: false, pin: null }
}

export interface TransferInfo {
  pin: string
  expires_at: string | null // null when it never expires
  never_expires?: boolean
  files: { name: string; size: number }[]
  text?: string
}

function ZapIcon() {
  return (
    <svg className="w-6 h-6" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M13 10V3L4 14h7v7l9-11h-7z" />
    </svg>
  )
}

export default function App() {
  const [view, setView] = useState<View>(() => (readHash().uploads ? 'uploads' : 'send'))
  const [managedPin, setManagedPin] = useState<string | null>(() => readHash().pin)
  const [transfer, setTransfer] = useState<TransferInfo | null>(null)
  const [receivedTransfer, setReceivedTransfer] = useState<TransferInfo | null>(null)
  const [session, refreshSession] = useSession()

  // Check URL for ?pin= parameter on load
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const pin = params.get('pin')
    if (pin) {
      setView('receive')
    }
  }, [])

  // Back/forward between the list and a transfer.
  useEffect(() => {
    const onHash = () => {
      const h = readHash()
      if (h.uploads) {
        setView('uploads')
        setManagedPin(h.pin)
      }
    }
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  const openManaged = (pin: string | null, replace = false) => {
    setManagedPin(pin)
    const url = `${window.location.pathname}#uploads${pin ? `/${pin}` : ''}`
    if (replace) window.history.replaceState({}, '', url)
    else window.history.pushState({}, '', url)
  }

  const canManage = session.managementEnabled && session.authenticated

  const handleUploadSuccess = (info: TransferInfo) => {
    setTransfer(info)
  }

  const handleSendAnother = () => {
    setTransfer(null)
  }

  const handlePinSuccess = (info: TransferInfo) => {
    setReceivedTransfer(info)
  }

  const handleTransferAnother = () => {
    setReceivedTransfer(null)
    // Clear pin from URL
    window.history.replaceState({}, '', window.location.pathname)
  }

  const handleLogout = async () => {
    await fetch('/api/logout', { method: 'POST' }).catch(() => {})
    setTransfer(null)
    refreshSession()
  }

  const switchView = (v: View) => {
    setView(v)
    setTransfer(null)
    setReceivedTransfer(null)
    setManagedPin(null)
    window.history.replaceState({}, '', window.location.pathname + (v === 'uploads' ? '#uploads' : ''))
  }

  const tabClass = (v: View) =>
    `px-4 py-1.5 rounded-lg text-sm font-medium transition-all duration-200 ${
      view === v ? 'bg-indigo-600 text-white shadow-lg shadow-indigo-500/20' : 'text-white/60 hover:text-white'
    }`

  return (
    <div className="min-h-screen bg-[#0d0d0d] flex flex-col">
      {/* Header */}
      <header className="border-b border-white/10">
        <div className="max-w-5xl mx-auto px-4 py-4 flex flex-wrap gap-3 items-center justify-between">
          <div className="flex items-center gap-2">
            <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-indigo-500 to-violet-600 flex items-center justify-center text-white">
              <ZapIcon />
            </div>
            <span className="text-xl font-bold bg-gradient-to-r from-indigo-400 to-violet-400 bg-clip-text text-transparent">
              Shadowfax
            </span>
          </div>

          {/* Nav tabs */}
          <nav className="flex items-center gap-1 glass rounded-xl p-1">
            <button onClick={() => switchView('send')} className={tabClass('send')}>
              Send
            </button>
            <button onClick={() => switchView('receive')} className={tabClass('receive')}>
              Receive
            </button>
            {canManage && (
              <button onClick={() => switchView('uploads')} className={tabClass('uploads')}>
                Uploads
              </button>
            )}
            {session.loginRequired && session.authenticated && (
              <button
                onClick={handleLogout}
                className="px-3 py-1.5 rounded-lg text-sm font-medium text-white/40 hover:text-white transition-all duration-200"
              >
                Sign out
              </button>
            )}
          </nav>
        </div>
      </header>

      {/* Main content */}
      <main className="flex-1 flex items-start justify-center px-4 py-12">
        <div className={`w-full ${view === 'uploads' ? 'max-w-5xl' : 'max-w-2xl'} animate-slide-up`}>
          {view === 'uploads' && (
            <>
              {!session.checked ? null : canManage ? (
                <Manage
                  selectedPin={managedPin}
                  onSelect={openManaged}
                  onUnauthorized={refreshSession}
                  onNewUpload={() => switchView('send')}
                />
              ) : session.managementEnabled ? (
                <Login onSuccess={refreshSession} />
              ) : (
                <div className="glass rounded-2xl p-8 text-center text-white/60 text-sm">
                  Upload management needs a login. Set <code className="text-white/80">UPLOAD_PASSWORD</code> or{' '}
                  <code className="text-white/80">API_KEY</code> on the server to enable it.
                </div>
              )}
            </>
          )}
          {view === 'send' && (
            <>
              {transfer ? (
                <PinDisplay
                  transfer={transfer}
                  onSendAnother={handleSendAnother}
                  onManage={
                    canManage
                      ? () => {
                          setView('uploads')
                          setTransfer(null)
                          openManaged(transfer.pin)
                        }
                      : undefined
                  }
                />
              ) : !session.checked ? null : session.authenticated ? (
                <DropZone onUploadSuccess={handleUploadSuccess} onUnauthorized={refreshSession} />
              ) : (
                <Login onSuccess={refreshSession} />
              )}
            </>
          )}
          {view === 'receive' && (
            <>
              {receivedTransfer ? (
                <FileList transfer={receivedTransfer} onTransferAnother={handleTransferAnother} />
              ) : (
                <PinEntry onSuccess={handlePinSuccess} />
              )}
            </>
          )}
        </div>
      </main>

      {/* Footer */}
      <footer className="border-t border-white/5 py-6">
        <div className="max-w-4xl mx-auto px-4 text-center text-white/30 text-sm">
          Encrypted at rest with AES-256-GCM &middot; automatically deleted after expiry &middot; no account needed to receive.
        </div>
      </footer>
    </div>
  )
}
