import { Badge } from '@mantine/core'

const states: Record<string, { label: string; color: string }> = {
  CREATED: { label: 'Oluşturuldu', color: 'gray' },
  UNDER_REVIEW: { label: 'İncelemede', color: 'yellow' },
  PENDING_APPROVAL: { label: 'Onay bekliyor', color: 'yellow' },
  RESERVED: { label: 'Ayrıldı', color: 'blue' },
  SENT: { label: 'Gönderildi', color: 'blue' },
  UNKNOWN: { label: 'Belirsiz', color: 'orange' },
  COMPLETED: { label: 'Tamamlandı', color: 'green' },
  FAILED: { label: 'Başarısız', color: 'red' },
  REVERSED: { label: 'İade edildi', color: 'gray' },
  REJECTED: { label: 'Reddedildi', color: 'red' },
}

export default function StateBadge({ state }: { state: string }) {
  const s = states[state] ?? { label: state, color: 'gray' }
  return (
    <Badge color={s.color} variant="light">
      {s.label}
    </Badge>
  )
}
