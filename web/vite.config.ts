import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    host: '127.0.0.1', port: 5173, strictPort: true,
    headers: { 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'X-Frame-Options': 'DENY', 'Referrer-Policy': 'same-origin' },
    proxy: { '/api': 'http://127.0.0.1:18080' },
  },
  preview: { headers: { 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'X-Frame-Options': 'DENY', 'Referrer-Policy': 'same-origin' } },
  test: { environment: 'jsdom', setupFiles: ['./src/test/setup.ts'], restoreMocks: true },
  build: { rolldownOptions: { output: { codeSplitting: {
    groups: [{ name: 'ant-design', test: /node_modules\/ant-design-vue/ }, { name: 'vue', test: /node_modules\/(?:@vue|vue)\// }],
  } } } },
})
