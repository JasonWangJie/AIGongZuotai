import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { basename, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';
import { build } from 'vite';

// Run after npm run build. A second in-memory build follows the existing
// SBOM path, which must see the same complete set of public representations.
const root = fileURLToPath(new URL('../', import.meta.url));
const dist = join(root, 'dist');
const result = await build({ root, logLevel: 'silent', build: { write: false } });
const emitted = new Map();
for (const out of (Array.isArray(result) ? result : [result]).flatMap((r) => r.output)) {
  emitted.set(out.fileName, Buffer.from(out.type === 'chunk' ? out.code : out.source));
}
const assets = await readdir(join(dist, 'assets'));
const chunks = assets.filter((name) => name.endsWith('.js')).sort();
assert.ok(chunks.length > 0, 'production build contains no JS');
assert.deepEqual(assets.filter((name) => name.endsWith('.gz')).sort(), chunks.map((name) => name + '.gz').sort());
for (const name of chunks) {
  const path = 'assets/' + name;
  const original = await readFile(join(dist, path));
  const compressed = await readFile(join(dist, path + '.gz'));
  assert.ok(original.equals(gunzipSync(compressed)), 'decoded sidecar differs from final chunk: ' + name);
  assert.ok(original.equals(emitted.get(path)), 'memory build differs from final chunk: ' + name);
  assert.ok(compressed.equals(emitted.get(path + '.gz')), 'memory build differs from sidecar: ' + name);
}
const html = await readFile(join(dist, 'index.html'), 'utf8');
const entry = /<script\b[^>]*\btype="module"[^>]*\bsrc="\/(assets\/[^"]+\.js)"/.exec(html)?.[1];
assert.ok(entry && chunks.includes(basename(entry)), 'entry script is missing');
console.log(JSON.stringify({
  verified_chunks: chunks.length,
  entry_bytes: emitted.get(entry).length,
  entry_gzip_bytes: emitted.get(entry + '.gz').length,
}));
