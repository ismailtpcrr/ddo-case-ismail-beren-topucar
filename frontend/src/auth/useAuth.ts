import { createContext, useContext } from 'react'
import type { User } from '../api/client'

export interface AuthState {
  user: User | null
  loading: boolean // açılışta çerezden oturum geri yükleniyor
  signIn: (email: string, password: string) => Promise<void>
  signOut: () => Promise<void>
  can: (permission: string) => boolean
}

export const AuthContext = createContext<AuthState | null>(null)

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
