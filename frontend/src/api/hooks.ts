import { useInfiniteQuery } from '@tanstack/react-query'
import { api, type Account, type Page } from './client'

// Hesap listesi (müşteride kendi hesapları, yetkili rollerde tümü); keyset sayfalı.
export function useAccounts() {
  return useInfiniteQuery({
    queryKey: ['accounts'],
    queryFn: ({ pageParam }) => api<Page<Account>>(`/accounts?limit=50${pageParam ? `&cursor=${pageParam}` : ''}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
}
