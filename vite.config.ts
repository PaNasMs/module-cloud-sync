import { defineConfig } from 'vite'
import { resolve } from 'node:path'
const id = 'cloud-sync'
const shared: Record<string, string> = {
  react: 'PaNasMsSDK.react',
  'react/jsx-runtime': 'PaNasMsSDK.jsx',
  '@tanstack/react-query': 'PaNasMsSDK.query',
  '@radix-ui/react-dialog': 'PaNasMsSDK.dialog',
  'react-router-dom': 'PaNasMsSDK.router',
}
for (const name of [
  'ui',
  'operations',
  'removable',
  'runtime',
  'completion',
  'layout',
  'client',
  'i18n',
  'navigation',
])
  shared['@panasms/' + name] = 'PaNasMsSDK.' + name
export default defineConfig({
  resolve: {
    alias: {
      '@mdi/js': resolve('node_modules/@mdi/js/mdi.js'),
      '@xterm/xterm': resolve('node_modules/@xterm/xterm'),
      '@xterm/addon-fit': resolve('node_modules/@xterm/addon-fit'),
    },
  },
  build: {
    outDir: resolve('dist/ui'),
    emptyOutDir: true,
    lib: {
      entry: resolve('frontend/' + id + '.tsx'),
      name: 'PaNasMsModule_' + id.replaceAll('-', '_'),
      formats: ['iife'],
      fileName: () => 'index.js',
      cssFileName: 'index',
    },
    rollupOptions: { external: Object.keys(shared), output: { globals: shared } },
  },
})
