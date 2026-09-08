import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    // The dev server has no relay of its own; forward /api to the Go server
    // started by mise run serve, WebSockets included. The Host header is left
    // alone so it keeps matching Origin, which the relay checks on upgrade.
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', ws: true },
    },
  },
});
