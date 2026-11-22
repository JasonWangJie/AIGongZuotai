import assert from 'node:assert/strict';
import test from 'node:test';
import { classifyRenderer, frameStats } from '../../apps/workbench/scripts/canvas-pan-samples.mjs';

test('nearest ranks retain every interval including large stalls', () => {
  const raw = [...Array(18).fill(16), 70, 340];
  assert.deepEqual(frameStats(raw), { frames: 20, total_ms: 698, p50_ms: 16, p95_ms: 70, p99_ms: 340, max_ms: 340 });
  assert.throws(() => frameStats([16, 0, 17]));
});

test('hardware requires positive renderer and compositing evidence', () => {
  assert.equal(classifyRenderer({ renderer: 'ANGLE (Intel UHD Graphics)', compositing: 'enabled' }), 'hardware');
  assert.equal(classifyRenderer({ renderer: 'ANGLE (SwiftShader Device)', compositing: 'enabled' }), 'software');
  assert.equal(classifyRenderer({ renderer: '', compositing: 'disabled_software' }), 'software');
  assert.equal(classifyRenderer({ renderer: 'unidentified', compositing: 'enabled' }), 'unknown');
});

