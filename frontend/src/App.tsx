import { useState, useEffect } from 'react'
import DropZone from './components/DropZone'
import PinDisplay from './components/PinDisplay'
import PinEntry from './components/PinEntry'
import FileList from './components/FileList'

export type View = 'send' | 'receive'

export interface TransferInfo {
  pin: string
  expires_at: string
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
  const [view, setView] = useState<View>('send')
  const [transfer, setTransfer] = useState<TransferInfo | null>(null)
  const [receivedTransfer, setReceivedTransfer] = useState<TransferInfo | null>(null)

  // Check URL for ?pin= parameter on load
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const pin = params.get('pin')
    if (pin) {
      setView('receive')
    }
  }, [])

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

  const switchView = (v: View) => {
    setView(v)
    setTransfer(null)
    setReceivedTransfer(null)
    window.history.replaceState({}, '', window.location.pathname)
  }

  return (
    <div className="min-h-screen bg-[#0d0d0d] flex flex-col">
      {/* Header */}
      <header className="border-b border-white/10">
        <div className="max-w-4xl mx-auto px-4 py-4 flex items-center justify-between">
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
            <button
              onClick={() => switchView('send')}
              className={`px-4 py-1.5 rounded-lg text-sm font-medium transition-all duration-200 ${
                view === 'send'
                  ? 'bg-indigo-600 text-white shadow-lg shadow-indigo-500/20'
                  : 'text-white/60 hover:text-white'
              }`}
            >
              Send
            </button>
            <button
              onClick={() => switchView('receive')}
              className={`px-4 py-1.5 rounded-lg text-sm font-medium transition-all duration-200 ${
                view === 'receive'
                  ? 'bg-indigo-600 text-white shadow-lg shadow-indigo-500/20'
                  : 'text-white/60 hover:text-white'
              }`}
            >
              Receive
            </button>
          </nav>
        </div>
      </header>

      {/* Main content */}
      <main className="flex-1 flex items-start justify-center px-4 py-12">
        <div className="w-full max-w-2xl animate-slide-up">
          {view === 'send' && (
            <>
              {transfer ? (
                <PinDisplay transfer={transfer} onSendAnother={handleSendAnother} />
              ) : (
                <DropZone onUploadSuccess={handleUploadSuccess} />
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
          Files are automatically deleted after expiry. No account required.
        </div>
      </footer>
    </div>
  )
}
