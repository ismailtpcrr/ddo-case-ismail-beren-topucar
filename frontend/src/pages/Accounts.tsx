import { Badge, Button, Card, Group, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { Link } from 'react-router-dom'
import { useAccounts } from '../api/hooks'
import { Empty, ErrorState, Loading } from '../components/States'
import { formatIban, formatMoney } from '../format'

const kindLabels: Record<string, string> = {
  CHECKING: 'Vadesiz', TIME_DEPOSIT: 'Vadeli', NOSTRO: 'Nostro', EXTERNAL_CLEARING: 'Dış takas', CARD_SETTLEMENT: 'Kart takas',
  INTEREST_EXPENSE: 'Faiz gideri', WITHHOLDING_TAX: 'Stopaj', FX_POSITION: 'Döviz pozisyonu', SUSPENSE: 'Askı', OPENING_EQUITY: 'Açılış özkaynağı',
}

export default function Accounts() {
  const q = useAccounts()
  const accounts = q.data?.pages.flatMap((p) => p.items) ?? []

  return (
    <Stack>
      <Title order={2}>Hesaplar</Title>
      {q.isPending && <Loading label="Hesaplar yükleniyor…" />}
      {q.isError && <ErrorState error={q.error} onRetry={() => void q.refetch()} />}
      {q.isSuccess && accounts.length === 0 && <Empty title="Henüz hesap yok">Hesabınız açıldığında burada görünür.</Empty>}
      <SimpleGrid cols={{ base: 1, sm: 2, xl: 3 }}>
        {accounts.map((a) => (
          <Card key={a.id} withBorder padding="md" component={Link} to={`/accounts/${a.id}`} style={{ textDecoration: 'none', color: 'inherit' }}>
            <Group justify="space-between" wrap="nowrap" align="flex-start">
              <Text fw={600}>{a.name}</Text>
              <Badge variant="light">{kindLabels[a.kind] ?? a.kind}</Badge>
            </Group>
            <Text size="xl" fw={700} mt="xs">
              {formatMoney(a.balance_minor, a.currency)}
            </Text>
            <Text size="sm" c="dimmed">
              Kullanılabilir: {formatMoney(a.available_minor, a.currency)}
              {a.held_minor > 0 && ` · Bloke: ${formatMoney(a.held_minor, a.currency)}`}
            </Text>
            <Text size="xs" c="dimmed" mt="xs" style={{ overflowWrap: 'anywhere' }}>
              {formatIban(a.iban)}
              {a.maturity_date && ` · Vade: ${a.maturity_date}`}
            </Text>
          </Card>
        ))}
      </SimpleGrid>
      {q.hasNextPage && (
        <Button variant="light" loading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          Daha fazla göster
        </Button>
      )}
    </Stack>
  )
}
