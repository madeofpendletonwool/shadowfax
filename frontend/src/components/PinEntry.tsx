import { useState, useRef, useEffect, KeyboardEvent, ClipboardEvent } from 'react'
import type { TransferInfo } from '../App'
import { useConfig } from '../config'

interface Props {
  onSuccess: (info: TransferInfo) => void
}

export default function PinEntry({ onSuccess }: Props) {
  const { pin_length: pinLength } = useConfig()
  const [digits, setDigits] = useState<string[]>(() => Array(pinLength).fill(''))
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRefs = useRef<(HTMLInputElement | null)[]>([])
  const submitted = useRef(false)

  // The PIN length comes from the server, so resize once it is known.
  useEffect(() => {
    setDigits(prev => {
      if (prev.length === pinLength) return prev
      const next = Array(pinLength).fill('')
      prev.slice(0, pinLength).forEach((d, i) => { next[i] = d })
      return next
    })
  }, [pinLength])

  // Auto-fill from ?pin= in the share link.
  useEffect(() => {
    const fromUrl = new URLSearchParams(window.location.search).get('pin')
    if (fromUrl && new RegExp(`^\\d{${pinLength}}$`).test(fromUrl)) {
      if (submitted.current) return
      submitted.current = true
      setDigits(fromUrl.split(''))
      const timer = setTimeout(() => submitPin(fromUrl), 400)
      return () => clearTimeout(timer)
    }
    inputRefs.current[0]?.focus()
  }, [pinLength])

  const submitPin = async (pin: string) => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/transfer/${pin}`)
      if (res.status === 404) {
        setError('No transfer found with this PIN. It may have expired.')
        setLoading(false)
        return
      }
      if (res.status === 429) {
        const retry = res.headers.get('Retry-After')
        setError(
          retry
            ? `Too many attempts. Try again in about ${Math.ceil(Number(retry) / 60)} minute(s).`
            : 'Too many attempts. Please wait a moment and try again.'
        )
        setLoading(false)
        return
      }
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error || 'Something went wrong')
        setLoading(false)
        return
      }
      onSuccess((await res.json()) as TransferInfo)
    } catch {
      setError('Network error. Please try again.')
      setLoading(false)
    }
  }

  const maybeAutoSubmit = (next: string[]) => {
    const pin = next.join('')
    if (pin.length === pinLength && !next.includes('')) {
      setTimeout(() => submitPin(pin), 100)
    }
  }

  const handleKeyDown = (index: number, e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Backspace') {
      const next = [...digits]
      if (next[index] !== '') {
        next[index] = ''
      } else if (index > 0) {
        inputRefs.current[index - 1]?.focus()
        next[index - 1] = ''
      }
      setDigits(next)
      return
    }

    if (e.key === 'ArrowLeft' && index > 0) {
      inputRefs.current[index - 1]?.focus()
      return
    }
    if (e.key === 'ArrowRight' && index < pinLength - 1) {
      inputRefs.current[index + 1]?.focus()
      return
    }
    if (e.key === 'Enter') {
      const pin = digits.join('')
      if (pin.length === pinLength && !digits.includes('')) submitPin(pin)
      return
    }
    if (!/^\d$/.test(e.key)) {
      if (e.key.length === 1 && !e.metaKey && !e.ctrlKey) e.preventDefault()
      return
    }

    e.preventDefault()
    const next = [...digits]
    next[index] = e.key
    setDigits(next)
    setError(null)

    if (index < pinLength - 1) {
      inputRefs.current[index + 1]?.focus()
    } else {
      maybeAutoSubmit(next)
    }
  }

  const handlePaste = (e: ClipboardEvent<HTMLInputElement>) => {
    e.preventDefault()
    const pasted = e.clipboardData.getData('text').replace(/\D/g, '').slice(0, pinLength)
    if (!pasted) return

    const next = Array(pinLength).fill('')
    for (let i = 0; i < pinLength; i++) next[i] = pasted[i] || ''
    setDigits(next)
    setError(null)

    if (pasted.length === pinLength) {
      inputRefs.current[pinLength - 1]?.focus()
      maybeAutoSubmit(next)
    } else {
      inputRefs.current[pasted.length]?.focus()
    }
  }

  const pin = digits.join('')
  const isComplete = pin.length === pinLength && !digits.includes('')

  // Keep long PINs on one line on a phone.
  const boxSize = pinLength > 6 ? 'w-10 h-14 text-2xl' : pinLength > 4 ? 'w-12 h-16 text-3xl' : 'w-16 h-20 text-4xl'

  return (
    <div className="space-y-8">
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold text-white">Receive Files</h1>
        <p className="text-white/50">Enter the {pinLength}-digit PIN to access the files</p>
      </div>

      <div className="glass rounded-2xl p-8 space-y-6">
        <div className="flex items-center justify-center gap-2 sm:gap-3">
          {digits.map((digit, i) => (
            <input
              key={i}
              ref={el => { inputRefs.current[i] = el }}
              type="text"
              inputMode="numeric"
              pattern="\d*"
              maxLength={1}
              value={digit}
              onChange={() => {}} // handled by keyDown
              onKeyDown={e => handleKeyDown(i, e)}
              onPaste={handlePaste}
              onClick={() => inputRefs.current[i]?.focus()}
              disabled={loading}
              className={`pin-input ${boxSize}`}
              autoComplete="off"
            />
          ))}
        </div>

        {error && (
          <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm text-center animate-fade-in">
            {error}
          </div>
        )}

        <button
          onClick={() => isComplete && !loading && submitPin(pin)}
          disabled={!isComplete || loading}
          className={`
            w-full py-4 rounded-2xl font-semibold text-base transition-all duration-200
            ${isComplete && !loading
              ? 'bg-gradient-to-r from-indigo-600 to-violet-600 hover:from-indigo-500 hover:to-violet-500 text-white shadow-xl shadow-indigo-500/20 active:scale-[0.99]'
              : 'bg-white/10 text-white/30 cursor-not-allowed'
            }
          `}
        >
          {loading ? (
            <span className="flex items-center justify-center gap-2">
              <svg className="w-4 h-4 animate-spin-slow" fill="none" viewBox="0 0 24 24">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
              </svg>
              Looking up transfer...
            </span>
          ) : (
            'Access Files'
          )}
        </button>
      </div>

      <p className="text-center text-sm text-white/30">
        Tip: You can paste a {pinLength}-digit PIN directly into any box
      </p>
    </div>
  )
}
