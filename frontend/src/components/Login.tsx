import { useState, FormEvent } from 'react'

interface Props {
  onSuccess: () => void
}

export default function Login({ onSuccess }: Props) {
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!password || loading) return
    setLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      })
      if (res.ok) {
        setPassword('')
        onSuccess()
        return
      }
      if (res.status === 429) {
        const retry = res.headers.get('Retry-After')
        setError(
          retry
            ? `Too many attempts. Try again in about ${Math.ceil(Number(retry) / 60)} minute(s).`
            : 'Too many attempts. Please wait a moment and try again.'
        )
      } else if (res.status === 401) {
        setError('Wrong password.')
      } else {
        setError('Could not sign in. Please try again.')
      }
    } catch {
      setError('Network error. Please try again.')
    }
    setLoading(false)
  }

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold text-white">Sign in to send</h1>
        <p className="text-white/50">
          Uploading needs a login. Got a PIN? Use <span className="text-white/70">Receive</span>, no login needed.
        </p>
      </div>

      <form onSubmit={submit} className="glass rounded-2xl p-8 space-y-6">
        <input
          type="password"
          value={password}
          onChange={e => setPassword(e.target.value)}
          placeholder="Password"
          autoComplete="current-password"
          autoFocus
          disabled={loading}
          className="w-full bg-white/5 border border-white/10 rounded-xl px-4 py-3 text-white placeholder-white/30 focus:outline-none focus:border-indigo-500"
        />

        {error && (
          <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 text-red-400 text-sm text-center animate-fade-in">
            {error}
          </div>
        )}

        <button
          type="submit"
          disabled={!password || loading}
          className={`
            w-full py-4 rounded-2xl font-semibold text-base transition-all duration-200
            ${password && !loading
              ? 'bg-gradient-to-r from-indigo-600 to-violet-600 hover:from-indigo-500 hover:to-violet-500 text-white shadow-xl shadow-indigo-500/20 active:scale-[0.99]'
              : 'bg-white/10 text-white/30 cursor-not-allowed'
            }
          `}
        >
          {loading ? 'Signing in...' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
