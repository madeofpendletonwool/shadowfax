import { useState, useRef, useEffect, KeyboardEvent, ClipboardEvent } from 'react'
import type { TransferInfo } from '../App'

interface Props {
  onSuccess: (info: TransferInfo) => void
}

export default function PinEntry({ onSuccess }: Props) {
  const [digits, setDigits] = useState(['', '', '', ''])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRefs = useRef<(HTMLInputElement | null)[]>([])

  // Auto-fill from URL
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const pin = params.get('pin')
    if (pin && /^\d{4}$/.test(pin)) {
      setDigits(pin.split(''))
      // Auto-submit after brief delay
      setTimeout(() => {
        submitPin(pin)
      }, 400)
    } else {
      inputRefs.current[0]?.focus()
    }
  }, [])

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
      if (!res.ok) {
        const data = await res.json()
        setError(data.error || 'Something went wrong')
        setLoading(false)
        return
      }
      const data = await res.json() as TransferInfo
      onSuccess(data)
    } catch {
      setError('Network error. Please try again.')
      setLoading(false)
    }
  }

  const handleKeyDown = (index: number, e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Backspace') {
      if (digits[index] !== '') {
        const newDigits = [...digits]
        newDigits[index] = ''
        setDigits(newDigits)
      } else if (index > 0) {
        inputRefs.current[index - 1]?.focus()
        const newDigits = [...digits]
        newDigits[index - 1] = ''
        setDigits(newDigits)
      }
      return
    }

    if (e.key === 'ArrowLeft' && index > 0) {
      inputRefs.current[index - 1]?.focus()
      return
    }
    if (e.key === 'ArrowRight' && index < 3) {
      inputRefs.current[index + 1]?.focus()
      return
    }

    if (e.key === 'Enter') {
      const pin = digits.join('')
      if (pin.length === 4) submitPin(pin)
      return
    }

    if (!/^\d$/.test(e.key)) {
      e.preventDefault()
      return
    }

    // Digit pressed
    e.preventDefault()
    const newDigits = [...digits]
    newDigits[index] = e.key
    setDigits(newDigits)
    setError(null)

    if (index < 3) {
      inputRefs.current[index + 1]?.focus()
    } else {
      // Last digit - auto submit
      const pin = newDigits.join('')
      if (pin.length === 4) {
        setTimeout(() => submitPin(pin), 100)
      }
    }
  }

  const handlePaste = (e: ClipboardEvent<HTMLInputElement>) => {
    e.preventDefault()
    const pasted = e.clipboardData.getData('text').replace(/\D/g, '').slice(0, 4)
    if (pasted.length === 0) return

    const newDigits = [...digits]
    for (let i = 0; i < 4; i++) {
      newDigits[i] = pasted[i] || ''
    }
    setDigits(newDigits)
    setError(null)

    if (pasted.length === 4) {
      inputRefs.current[3]?.focus()
      setTimeout(() => submitPin(pasted), 100)
    } else {
      inputRefs.current[pasted.length]?.focus()
    }
  }

  const handleClick = (index: number) => {
    inputRefs.current[index]?.focus()
  }

  const pin = digits.join('')
  const isComplete = pin.length === 4

  return (
    <div className="space-y-8">
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold text-white">Receive Files</h1>
        <p className="text-white/50">Enter the 4-digit PIN to access the files</p>
      </div>

      <div className="glass rounded-2xl p-8 space-y-6">
        {/* PIN inputs */}
        <div className="flex items-center justify-center gap-4">
          {digits.map((digit, i) => (
            <input
              key={i}
              ref={(el) => { inputRefs.current[i] = el }}
              type="text"
              inputMode="numeric"
              pattern="\d*"
              maxLength={1}
              value={digit}
              onChange={() => {}} // handled by keyDown
              onKeyDown={(e) => handleKeyDown(i, e)}
              onPaste={handlePaste}
              onClick={() => handleClick(i)}
              disabled={loading}
              className="pin-input"
              autoComplete="off"
            />
          ))}
        </div>

        {/* Error */}
        {error && (
          <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm text-center animate-fade-in">
            {error}
          </div>
        )}

        {/* Submit button */}
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
        Tip: You can paste a 4-digit PIN directly into any box
      </p>
    </div>
  )
}
