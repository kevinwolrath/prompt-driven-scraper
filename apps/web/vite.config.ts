import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The UI talks to the Go API. In development Vite proxies /api to it; in
// production the API serves the built files itself.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: process.env.PDS_API ?? 'http://localhost:8080', changeOrigin: true },
    },
  },
});
