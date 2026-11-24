import { copyFile, mkdir } from 'node:fs/promises';
import { build } from 'esbuild';

await mkdir('../dist', { recursive: true });
await build({
  entryPoints: ['app.js', 'manager.js'],
  bundle: true,
  format: 'esm',
  outdir: '../dist',
});
await Promise.all(['index.html', 'viewer.html', 'style.css', 'manager.css'].map(
  file => copyFile(file, `../dist/${file}`),
));
