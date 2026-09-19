/**
 * Feature: F-004 (TypeScript SDK)
 * Story: US-004-03 (SDK Rate Limiting, Docs & CI)
 * Purpose: Copy ESM build output as .mjs files for dual CJS/ESM package exports
 */

const fs = require('node:fs');
const path = require('node:path');

const distDir = path.resolve(__dirname, '..', 'dist');
const esmDir = path.resolve(distDir, 'esm');

const esmFiles = fs.readdirSync(esmDir).filter((f) => f.endsWith('.js'));

for (const file of esmFiles) {
  const src = path.join(esmDir, file);
  const dest = path.join(distDir, file.replace(/\.js$/, '.mjs'));
  fs.copyFileSync(src, dest);
}

const count = esmFiles.length;
console.log(`build-dual: copied ${count} ESM files as .mjs to dist/`);
