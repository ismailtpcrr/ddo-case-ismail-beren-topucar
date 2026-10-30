import { AppShell, Badge, Burger, Button, Group, NavLink, Text, Title } from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import { NavLink as RouterLink, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/useAuth'

const roleLabels: Record<string, string> = {
  CUSTOMER: 'Müşteri',
  OPERATIONS: 'Operasyon uzmanı',
  RISK_ANALYST: 'Risk analisti',
  COMPLIANCE: 'Uyum uzmanı',
  AUDITOR: 'Denetçi',
  SYSTEM: 'Sistem',
}

// Menü role göre süzülür; asıl koruma sunucudadır (her uç kendi iznini ister).
const menu = [
  { to: '/accounts', label: 'Hesaplar', anyOf: ['own_banking', 'ledger:read'] },
  { to: '/transfers', label: 'Transferler', anyOf: ['own_banking', 'ledger:read'] },
]

export default function Layout() {
  const { user, signOut, can } = useAuth()
  const [opened, { toggle, close }] = useDisclosure()
  const location = useLocation()
  const items = menu.filter((m) => m.anyOf.some(can))

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 220, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="md"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="Menüyü aç/kapat" />
            <Title order={4}>ParaKöprü</Title>
          </Group>
          <Group gap="sm" wrap="nowrap">
            <Badge variant="light" visibleFrom="xs">
              {roleLabels[user?.role ?? ''] ?? user?.role}
            </Badge>
            <Button variant="subtle" size="xs" onClick={() => void signOut()}>
              Çıkış
            </Button>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="xs">
        {items.map((m) => (
          <NavLink
            key={m.to}
            component={RouterLink}
            to={m.to}
            label={m.label}
            active={location.pathname.startsWith(m.to)}
            onClick={close}
          />
        ))}
        {items.length === 0 && (
          <Text size="sm" c="dimmed" p="sm">
            Bu rol için henüz ekran yok.
          </Text>
        )}
        <Text size="xs" c="dimmed" p="sm" mt="auto" style={{ overflowWrap: 'anywhere' }}>
          {user?.email}
        </Text>
      </AppShell.Navbar>
      <AppShell.Main>
        <Outlet />
      </AppShell.Main>
    </AppShell>
  )
}
