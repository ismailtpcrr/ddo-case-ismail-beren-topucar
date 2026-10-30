import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { AuthContext, type AuthState } from './useAuth'
import { login as apiLogin, logout as apiLogout, refreshSession, setSessionLostHandler, type User } from '../api/client'

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)
  const queryClient = useQueryClient()

  // Sayfa yenilenince access token kaybolur; HttpOnly çerezdeki refresh token ile oturum geri gelir.
  useEffect(() => {
    let alive = true
    refreshSession().then((s) => {
      if (!alive) return
      setUser(s?.user ?? null)
      setLoading(false)
    })
    return () => {
      alive = false
    }
  }, [])

  useEffect(() => {
    setSessionLostHandler(() => {
      queryClient.clear()
      setUser(null)
    })
  }, [queryClient])

  const signIn = useCallback(async (email: string, password: string) => {
    setUser(await apiLogin(email, password))
  }, [])

  const signOut = useCallback(async () => {
    await apiLogout()
    queryClient.clear()
    setUser(null)
  }, [queryClient])

  const value = useMemo<AuthState>(
    () => ({ user, loading, signIn, signOut, can: (p) => user?.permissions.includes(p) ?? false }),
    [user, loading, signIn, signOut],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
