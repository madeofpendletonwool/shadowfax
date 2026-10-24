import { useEffect, useState } from 'react'

export interface ServerConfig {
  pin_length: number
  max_upload_bytes: number
  max_expiry_secs: number
  api_key_required: boolean
  login_required: boolean
  encrypted: boolean
}

// Used until /api/config answers, and if it never does. PIN_LENGTH is a server
// setting, so the UI must not assume a fixed number of boxes.
const DEFAULTS: ServerConfig = {
  pin_length: 6,
  max_upload_bytes: 0,
  max_expiry_secs: 604800,
  api_key_required: false,
  login_required: false,
  encrypted: true,
}

let pending: Promise<ServerConfig> | null = null

export function fetchConfig(): Promise<ServerConfig> {
  if (!pending) {
    pending = fetch('/api/config')
      .then(res => (res.ok ? res.json() : {}))
      .then((cfg: Partial<ServerConfig>) => ({ ...DEFAULTS, ...cfg }))
      .catch(() => DEFAULTS)
  }
  return pending
}

export function useConfig(): ServerConfig {
  const [config, setConfig] = useState<ServerConfig>(DEFAULTS)
  useEffect(() => {
    let active = true
    fetchConfig().then(cfg => {
      if (active) setConfig(cfg)
    })
    return () => {
      active = false
    }
  }, [])
  return config
}

export interface Session {
  checked: boolean
  loginRequired: boolean
  authenticated: boolean
}

// Whether this browser may upload. Re-run refresh() after login, logout, or a 401.
export function useSession(): [Session, () => void] {
  const [session, setSession] = useState<Session>({
    checked: false,
    loginRequired: false,
    authenticated: false,
  })
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let active = true
    fetch('/api/session')
      .then(res => (res.ok ? res.json() : { login_required: false, authenticated: true }))
      .catch(() => ({ login_required: false, authenticated: true }))
      .then((s: { login_required: boolean; authenticated: boolean }) => {
        if (active) {
          setSession({ checked: true, loginRequired: s.login_required, authenticated: s.authenticated })
        }
      })
    return () => {
      active = false
    }
  }, [tick])
  return [session, () => setTick(t => t + 1)]
}
