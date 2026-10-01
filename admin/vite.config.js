import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产物用相对路径(base './'), 放到 web 任意子目录都能跑
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2048,
  },
})
