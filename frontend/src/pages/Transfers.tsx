import { Alert, Autocomplete, Button, Card, ScrollArea, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState, type FormEvent } from 'react'
import { ApiError, api, type Page, type Transfer } from '../api/client'
import { useAccounts } from '../api/hooks'
import { useAuth } from '../auth/useAuth'
import { Empty, ErrorState, Loading } from '../components/States'
import StateBadge from '../components/StateBadge'
import { formatDate, formatIban, formatMoney, parseMinor } from '../format'

function TransferForm() {
  const accounts = useAccounts()
  const queryClient = useQueryClient()
  const list = (accounts.data?.pages.flatMap((p) => p.items) ?? []).filter((a) => a.customer_id)
  const [from, setFrom] = useState<string | null>(null)
  const [iban, setIban] = useState('')
  const [amount, setAmount] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // Idempotency-Key: aynı niyetin tekrarında (ağ hatası, zaman aşımı) aynı anahtar gönderilir, böylece çift işlem olmaz;
  // kesin bir cevap (başarı ya da iş kuralı reddi) gelince yeni bir niyet için yenisi üretilir.
  const key = useRef(crypto.randomUUID())

  const source = list.find((a) => a.id === from)
  const ownIbans = list.filter((a) => a.id !== from && a.iban).map((a) => ({ value: a.iban!, label: `${a.name} · ${formatIban(a.iban)}` }))

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    const minor = parseMinor(amount)
    const cleanIban = iban.replace(/\s/g, '').toUpperCase()
    if (!source) return setError('Gönderen hesabı seçin.')
    if (cleanIban.length !== 26) return setError('Alıcı IBAN\'ı 26 karakter olmalı (TR ile başlar).')
    if (minor === null || minor <= 0) return setError('Tutarı geçerli girin (ör. 1.250,50).')
    setBusy(true)
    try {
      const tr = await api<Transfer>('/transfers', {
        method: 'POST',
        headers: { 'Idempotency-Key': key.current },
        body: { from_account_id: source.id, to_iban: cleanIban, amount_minor: minor, currency: source.currency, description: description.trim() },
      })
      // Banka içi transfer hemen tamamlanır; dış banka transferi kabul edilir ve arka planda gönderilir (RESERVED).
      notifications.show(
        tr.state === 'COMPLETED'
          ? { color: 'green', title: 'Transfer tamamlandı', message: `${formatMoney(minor, source.currency)} gönderildi.` }
          : { color: 'blue', title: 'Transfer alındı', message: 'Dış bankaya gönderiliyor; durumu geçmişten izleyebilirsiniz.' },
      )
      key.current = crypto.randomUUID()
      setAmount('')
      setDescription('')
      await queryClient.invalidateQueries()
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message)
        if (err.status >= 400 && err.status < 500 && err.status !== 401) key.current = crypto.randomUUID() // kesin ret
      } else setError('Transfer gönderilemedi.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card withBorder>
      <form onSubmit={(e) => void submit(e)}>
        <Stack>
          <Title order={4}>Yeni transfer</Title>
          {accounts.isError && <ErrorState error={accounts.error} onRetry={() => void accounts.refetch()} />}
          {error && (
            <Alert color="red" role="alert">
              {error}
            </Alert>
          )}
          <Select
            label="Gönderen hesap"
            placeholder={accounts.isPending ? 'Hesaplar yükleniyor…' : 'Hesap seçin'}
            data={list.map((a) => ({ value: a.id, label: `${a.name} — ${formatMoney(a.available_minor, a.currency)}` }))}
            value={from}
            onChange={setFrom}
            searchable
            required
          />
          <Autocomplete
            label="Alıcı IBAN"
            placeholder="TR00 0000 0000 0000 0000 0000 00"
            data={ownIbans}
            value={iban}
            onChange={setIban}
            filter={({ options }) => options}
            required
          />
          <TextInput
            label={`Tutar${source ? ` (${source.currency})` : ''}`}
            inputMode="decimal"
            placeholder="1.250,50"
            value={amount}
            onChange={(e) => setAmount(e.currentTarget.value)}
            required
          />
          <TextInput label="Açıklama (isteğe bağlı)" maxLength={200} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
          <Button type="submit" loading={busy}>
            Gönder
          </Button>
        </Stack>
      </form>
    </Card>
  )
}

export default function Transfers() {
  const { can } = useAuth()
  const q = useInfiniteQuery({
    queryKey: ['transfers'],
    queryFn: ({ pageParam }) => api<Page<Transfer>>(`/transfers?limit=25${pageParam ? `&cursor=${pageParam}` : ''}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    // Sonuçlanmamış dış transfer varken liste birkaç saniyede bir yenilenir.
    refetchInterval: (query) =>
      query.state.data?.pages.some((p) => p.items.some((t) => ['RESERVED', 'SENT', 'UNKNOWN'].includes(t.state))) ? 3000 : false,
  })
  const rows = q.data?.pages.flatMap((p) => p.items) ?? []

  return (
    <Stack maw={900}>
      <Title order={2}>Transferler</Title>
      {can('transfers:create') && <TransferForm />}
      <Title order={4}>Geçmiş</Title>
      {q.isPending && <Loading label="Transferler yükleniyor…" />}
      {q.isError && <ErrorState error={q.error} onRetry={() => void q.refetch()} />}
      {q.isSuccess && rows.length === 0 && <Empty title="Henüz transfer yok">Yaptığınız transferler burada listelenir.</Empty>}
      {rows.length > 0 && (
        <ScrollArea>
          <Table striped miw={520}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Tarih</Table.Th>
                <Table.Th>Alıcı</Table.Th>
                <Table.Th ta="right">Tutar</Table.Th>
                <Table.Th>Durum</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((t) => (
                <Table.Tr key={t.id}>
                  <Table.Td>{formatDate(t.created_at)}</Table.Td>
                  <Table.Td>
                    <Text size="sm" style={{ overflowWrap: 'anywhere' }}>{formatIban(t.to_iban)}</Text>
                    {t.description && <Text size="xs" c="dimmed">{t.description}</Text>}
                  </Table.Td>
                  <Table.Td ta="right">{formatMoney(t.amount_minor, t.currency)}</Table.Td>
                  <Table.Td>
                    <StateBadge state={t.state} />
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </ScrollArea>
      )}
      {q.hasNextPage && (
        <Button variant="light" loading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          Daha eski transferler
        </Button>
      )}
    </Stack>
  )
}
