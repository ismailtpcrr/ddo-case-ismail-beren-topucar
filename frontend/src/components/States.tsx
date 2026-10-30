// Her ekranda aynı yükleniyor / boş / hata görünümü.
import { Alert, Button, Center, Loader, Stack, Text } from '@mantine/core'
import type { ReactNode } from 'react'
import { ApiError } from '../api/client'

export function Loading({ label = 'Yükleniyor…' }: { label?: string }) {
  return (
    <Center py="xl" role="status" aria-live="polite">
      <Stack align="center" gap="xs">
        <Loader />
        <Text c="dimmed" size="sm">
          {label}
        </Text>
      </Stack>
    </Center>
  )
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <Center py="xl">
      <Stack align="center" gap={4}>
        <Text fw={600}>{title}</Text>
        {children && (
          <Text c="dimmed" size="sm" ta="center">
            {children}
          </Text>
        )}
      </Stack>
    </Center>
  )
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof ApiError ? error.message : 'Beklenmeyen bir hata oluştu.'
  return (
    <Alert color="red" title="İşlem tamamlanamadı" role="alert" my="md">
      <Stack gap="xs" align="flex-start">
        <Text size="sm">{message}</Text>
        {onRetry && (
          <Button variant="light" color="red" size="xs" onClick={onRetry}>
            Tekrar dene
          </Button>
        )}
      </Stack>
    </Alert>
  )
}
