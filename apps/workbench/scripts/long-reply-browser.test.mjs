// @vitest-environment node
import assert from 'node:assert/strict';
import { test } from 'vitest';
import { verifyPrefix } from './long-reply-browser.mjs';

const pieces = ['甲', '乙', '丙', '丁', '戊', '己', '庚'];
const full = pieces.join('');
const ends = new Map(pieces.map((_, i) => [i + 1, i + 1]));

function probe(rendered, received, sent, previous) {
  const view = { active: true, valid: true, elapsed: 3100, text: full.slice(0, rendered), received };
  return verifyPrefix(view, sent, full, ends, previous, previous ? 2 : 1);
}

test('visible prefix advances through whole segments and stays within two dispatched segments', () => {
  const start = probe(1, 2, 3);
  const next = probe(3, 4, 5, start);
  assert.equal(next.visible_utf8_bytes, 9);
  assert.equal(next.rendered_segments, 3);
  assert.equal(next.lag_segments, 2);
  assert.throws(() => probe(1, 2, 3, start), /failed to advance/);
  assert.throws(() => probe(2, 5, 5, start), /lag exceeded/);
  assert.throws(() => probe(4, 3, 4, start), /lag exceeded/);
});

test('a partial event, changed prefix or terminal snapshot cannot substitute for a live checkpoint', () => {
  const view = { active: true, valid: true, elapsed: 1, text: '甲', received: 1 };
  assert.throws(() => verifyPrefix(view, 1, full, new Map([[2, 1]]), undefined, 1));
  assert.throws(() => verifyPrefix({ ...view, text: '错' }, 1, full, ends, undefined, 1));
  assert.throws(() => verifyPrefix({ ...view, active: false }, 1, full, ends, undefined, 1));
  assert.throws(() => verifyPrefix({ ...view, valid: false }, 1, full, ends, undefined, 1));
});
