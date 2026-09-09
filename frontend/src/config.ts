import { useEffect, useState } from 'react'

export interface ServerConfig {
  pin_length: number
  max_upload_bytes: number
  max_expiry_secs: number
  api_key_required: boolean
  encrypted: boolean
}

// Used until /api/config answers, and if it never does. PIN_LENGTH is a server
// setting, so the UI must not assume a fixed number of boxes.
const DEFAULTS: ServerConfig = {
  pin_length: 6,
  max_upload_bytes: 0,
  max_expiry_secs: 604800,
  api_key_required: false,
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
