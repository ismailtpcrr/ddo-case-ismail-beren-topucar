// API istemcisi. Access token yalnız bellekte tutulur; refresh token HttpOnly çerezdedir (JavaScript okuyamaz).
// 401 gelince oturum bir kez yenilenir ve istek tekrarlanır. Aynı anda yalnız tek bir yenileme isteği gönderilir:
// refresh token her yenilemede döner (rotasyon), eşzamanlı iki yenileme ikinciyi "çalınmış" sayar ve oturumu kapatır
// (DECISIONS.md K-07). Bu yüzden eşzamanlı 401'ler aynı yenileme sözünü bekler.

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export interface User {
  id: string
  email: string
  full_name: string
  role: string
  permissions: string[]
}

interface Session {
  access_token: string
  user: User
}

let accessToken: string | null = null
let refreshing: Promise<Session | null> | null = null
let onSessionLost: () => void = () => {}

export function setSessionLostHandler(fn: () => void) {
  onSessionLost = fn
}

async function parseError(res: Response): Promise<ApiError> {
  try {
    const body = await res.json()
    return new ApiError(res.status, body.error?.code ?? 'UNKNOWN', body.error?.message ?? 'Beklenmeyen bir hata oluştu.')
  } catch {
    return new ApiError(res.status, 'UNKNOWN', 'Sunucudan geçersiz yanıt geldi.')
  }
}

async function send(path: string, init: RequestInit & { token?: string | null }): Promise<Response> {
  const headers = new Headers(init.headers)
  if (init.body) headers.set('Content-Type', 'application/json')
  if (init.token) headers.set('Authorization', `Bearer ${init.token}`)
  try {
    return await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  } catch {
    throw new ApiError(0, 'NETWORK', 'Sunucuya ulaşılamadı. Bağlantınızı kontrol edip tekrar deneyin.')
  }
}

/** Çerezdeki refresh token ile oturumu yeniler; olmazsa null. Eşzamanlı çağrılar aynı sözü paylaşır. */
export function refreshSession(): Promise<Session | null> {
  refreshing ??= (async () => {
    try {
      const res = await send('/auth/refresh', { method: 'POST' })
      if (!res.ok) {
        accessToken = null
        return null
      }
      const s: Session = await res.json()
      accessToken = s.access_token
      return s
    } catch {
      return null
    } finally {
      refreshing = null
    }
  })()
  return refreshing
}

export async function login(email: string, password: string): Promise<User> {
  const res = await send('/auth/login', { method: 'POST', body: JSON.stringify({ email, password }) })
  if (!res.ok) throw await parseError(res)
  const s: Session = await res.json()
  accessToken = s.access_token
  return s.user
}

export async function logout(): Promise<void> {
  try {
    await send('/auth/logout', { method: 'POST' })
  } finally {
    accessToken = null
  }
}

export interface RequestOptions {
  method?: string
  body?: unknown
  headers?: Record<string, string>
}

/** Kimlik doğrulamalı API isteği; JSON yanıtı döndürür (204'te undefined). */
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const init = { method: opts.method ?? 'GET', headers: opts.headers, body: opts.body === undefined ? undefined : JSON.stringify(opts.body) }
  let res = await send(path, { ...init, token: accessToken })
  if (res.status === 401) {
    const s = await refreshSession()
    if (!s) {
      onSessionLost()
      throw new ApiError(401, 'SESSION_EXPIRED', 'Oturumunuzun süresi doldu. Lütfen yeniden giriş yapın.')
    }
    res = await send(path, { ...init, token: accessToken })
  }
  if (!res.ok) throw await parseError(res)
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T)
}

// --- Uç nokta tipleri ---

export interface Page<T> {
  items: T[]
  next_cursor: string | null
}

export interface Account {
  id: string
  customer_id: string | null
  kind: string
  currency: string
  name: string
  iban: string | null
  maturity_date: string | null
  balance_minor: number
  held_minor: number
  available_minor: number
}

export interface Posting {
  id: string
  entry_id: string
  kind: string
  description: string
  side: 'DEBIT' | 'CREDIT'
  amount_minor: number
  balance_effect_minor: number
  currency: string
  effective_at: string
}

export interface Transfer {
  id: string
  type: string
  state: string
  from_account_id: string
  to_iban: string
  amount_minor: number
  currency: string
  description: string
  failure_code: string | null
  created_at: string
}
