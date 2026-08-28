import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react-swc';
import * as path from 'node:path';

// https://vite.dev/config/
// Relative base in builds: the server injects <base href> with the real mount
// path, unknown at build time. Dev keeps the absolute path the router expects.
export default defineConfig(({ command }) => ({
  plugins: [react()],
  base: command === 'build' ? './' : '/dashboard',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
}));
