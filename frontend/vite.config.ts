import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Geliştirme sunucusu (npm run dev) /api isteklerini yerelde çalışan API'ye iletir.
// Üretimde aynı işi nginx yapar (nginx.conf); iki durumda da tarayıcı tek kökene konuşur, CORS gerekmez.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
