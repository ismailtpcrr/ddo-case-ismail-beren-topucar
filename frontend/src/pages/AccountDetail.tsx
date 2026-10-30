import { Anchor, Button, Card, Group, ScrollArea, Stack, Table, Text, Title } from '@mantine/core'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { api, type Account, type Page, type Posting } from '../api/client'
import { Empty, ErrorState, Loading } from '../components/States'
import { formatDate, formatIban, formatMoney } from '../format'

export default function AccountDetail() {
  const { id = '' } = useParams()
  const account = useQuery({ queryKey: ['accounts', id], queryFn: () => api<Account>(`/accounts/${id}`) })
  const entries = useInfiniteQuery({
    queryKey: ['entries', id],
    queryFn: ({ pageParam }) => api<Page<Posting>>(`/accounts/${id}/entries?limit=25${pageParam ? `&cursor=${pageParam}` : ''}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
  const rows = entries.data?.pages.flatMap((p) => p.items) ?? []
  const a = account.data

  return (
    <Stack>
      <Anchor component={Link} to="/accounts" size="sm">
        ← Hesaplar
      </Anchor>
      {account.isPending && <Loading />}
      {account.isError && <ErrorState error={account.error} onRetry={() => void account.refetch()} />}
      {a && (
        <Card withBorder>
          <Title order={3}>{a.name}</Title>
          <Text size="xl" fw={700}>
            {formatMoney(a.balance_minor, a.currency)}
          </Text>
          <Group gap="lg" mt="xs">
            <Text size="sm" c="dimmed">
              Kullanılabilir: {formatMoney(a.available_minor, a.currency)}
            </Text>
            <Text size="sm" c="dimmed">
              Bloke: {formatMoney(a.held_minor, a.currency)}
            </Text>
          </Group>
          <Text size="xs" c="dimmed" mt="xs" style={{ overflowWrap: 'anywhere' }}>
            {formatIban(a.iban)}
          </Text>
        </Card>
      )}

      <Title order={4}>Hesap hareketleri</Title>
      {entries.isPending && <Loading label="Hareketler yükleniyor…" />}
      {entries.isError && <ErrorState error={entries.error} onRetry={() => void entries.refetch()} />}
      {entries.isSuccess && rows.length === 0 && <Empty title="Henüz hareket yok">Bu hesapta işlem yapıldığında burada görünür.</Empty>}
      {rows.length > 0 && (
        <ScrollArea>
          <Table striped highlightOnHover miw={420}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Tarih</Table.Th>
                <Table.Th>Açıklama</Table.Th>
                <Table.Th ta="right">Tutar</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((r) => (
                <Table.Tr key={r.id}>
                  <Table.Td>{formatDate(r.effective_at)}</Table.Td>
                  <Table.Td>{r.description}</Table.Td>
                  <Table.Td ta="right" c={r.balance_effect_minor < 0 ? 'red' : 'green'} fw={600}>
                    {r.balance_effect_minor > 0 && '+'}
                    {formatMoney(r.balance_effect_minor, r.currency)}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </ScrollArea>
      )}
      {entries.hasNextPage && (
        <Button variant="light" loading={entries.isFetchingNextPage} onClick={() => void entries.fetchNextPage()}>
          Daha eski hareketler
        </Button>
      )}
    </Stack>
  )
}
