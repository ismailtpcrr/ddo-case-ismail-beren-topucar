import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from './auth/useAuth'
import Layout from './components/Layout'
import { Loading } from './components/States'
import AccountDetail from './pages/AccountDetail'
import Accounts from './pages/Accounts'
import Login from './pages/Login'
import Transfers from './pages/Transfers'

export default function App() {
  const { user, loading } = useAuth()
  if (loading) return <Loading label="Oturum kontrol ediliyor…" />
  if (!user) return <Login />
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route path="/accounts" element={<Accounts />} />
        <Route path="/accounts/:id" element={<AccountDetail />} />
        <Route path="/transfers" element={<Transfers />} />
        <Route path="*" element={<Navigate to="/accounts" replace />} />
      </Route>
    </Routes>
  )
}
