import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 构建产物直接输出到 web/static，供 Go 端 go:embed 打包。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../static',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
});
