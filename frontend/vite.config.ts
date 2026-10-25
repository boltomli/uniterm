import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

const version = process.env.VITE_VERSION || 'dev'

export default defineConfig({
  plugins: [
    vue(),
    // Element Plus on demand: every <el-*> used in a template gets its own
    // import (and CSS) injected instead of bundling the whole library
    // (~740 kB of JS + the full 360 kB stylesheet were on the critical path).
    // dirs: [] — local components are always imported explicitly, there is
    // nothing to scan; dts off so no components.d.ts is generated.
    Components({
      dirs: [],
      dts: false,
      resolvers: [ElementPlusResolver({ importStyle: 'css' })],
    }),
  ],
  define: {
    'import.meta.env.VITE_VERSION': JSON.stringify(version)
  },
  // esnext everywhere so the dev server's esbuild transform also accepts
  // top-level await (used by @novnc/novnc). `build.target` alone only affects
  // the production bundle; without this the `wails3 dev` Vite server crashes.
  build: {
    target: 'esnext'
  },
  esbuild: {
    target: 'esnext'
  },
  // NOTE: no optimizeDeps target needed on Vite 8 (rolldown pre-bundling does
  // no syntax downleveling, so TLA in deps passes through untouched; the old
  // optimizeDeps.esbuildOptions.target is deprecated and has no rolldown
  // equivalent).
  server: {
    host: '127.0.0.1',
    port: 34115,
    strictPort: true
  }
})
