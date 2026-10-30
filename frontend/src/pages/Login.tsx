import { Alert, Button, Card, Center, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core'
import { useState, type FormEvent } from 'react'
import { ApiError } from '../api/client'
import { useAuth } from '../auth/useAuth'

export default function Login() {
  const { signIn } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await signIn(email.trim(), password)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Giriş yapılamadı.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Center mih="100vh" p="md">
      <Card withBorder shadow="sm" padding="lg" w="100%" maw={400}>
        <form onSubmit={(e) => void submit(e)}>
          <Stack>
            <div>
              <Title order={2}>ParaKöprü</Title>
              <Text c="dimmed" size="sm">
                Dijital banka çekirdeği — giriş yapın
              </Text>
            </div>
            {error && (
              <Alert color="red" role="alert">
                {error}
              </Alert>
            )}
            <TextInput
              label="E-posta"
              type="email"
              autoComplete="username"
              required
              value={email}
              onChange={(e) => setEmail(e.currentTarget.value)}
              placeholder="musteri@parakopru.test"
            />
            <PasswordInput
              label="Parola"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.currentTarget.value)}
            />
            <Button type="submit" loading={busy}>
              Giriş yap
            </Button>
            <Text size="xs" c="dimmed">
              Demo hesaplar README'de listelidir (ör. musteri@parakopru.test).
            </Text>
          </Stack>
        </form>
      </Card>
    </Center>
  )
}
