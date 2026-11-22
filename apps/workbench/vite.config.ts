import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { gzipSync } from 'node:zlib';

export default defineConfig({
  plugins: [
    react(),
    {
      name: 'workbench-precompressed-javascript',
      apply: 'build',
      enforce: 'post',
      generateBundle: {
        order: 'post',
        handler(_options, bundle) {
          for (const item of Object.values(bundle)) {
            if (item.type !== 'chunk' || !/^assets\/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.js$/.test(item.fileName)) continue;
            this.emitFile({
              type: 'asset',
              fileName: item.fileName + '.gz',
              source: gzipSync(Buffer.from(item.code), { level: 6 }),
            });
          }
        },
      },
    },
  ],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/healthz': { target: 'http://127.0.0.1:8080' },
      '/demo-assets': { target: 'http://127.0.0.1:8080' },
    },
  },
  build: { target: 'es2022', sourcemap: false },
});
