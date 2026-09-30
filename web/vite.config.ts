import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// API проксируется на тот же origin: в разработке — Vite, в Docker — nginx
// (ADR 015). Так не нужен CORS, а ссылки на билеты и возврат после оплаты
// ведут на один адрес.
const api = process.env.API_URL ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: { port: 5173, proxy: { '/v1': { target: api, changeOrigin: true } } },
  build: { target: 'es2022', cssCodeSplit: true },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
