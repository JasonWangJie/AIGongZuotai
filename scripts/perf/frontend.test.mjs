import test from 'node:test';
import assert from 'node:assert/strict';
import { distribution, serviceEnv } from './frontend.mjs';

test('P50/P95 use the nearest observed rank without reordering raw samples', () => {
  const values = Array.from({ length: 20 }, (_, i) => 20 - i);
  assert.deepEqual(distribution(values), { p50: 10, p95: 19 });
  assert.equal(values[0], 20);
  assert.deepEqual(distribution([1.5]), { p50: 1.5, p95: 1.5 });
  for (const bad of [[], [NaN], [-1], [30001]]) {
    assert.throws(() => distribution(bad));
  }
});

test('the owned demo server uses an allowlist for inherited environment', () => {
  const env = serviceEnv('127.0.0.1:10000', 'dist', {
    assetDirectory: 'assets',
    directory: 'references',
  });
  assert.equal(env.DATABASE_URL, '');
  assert.equal(env.PROVIDER_MODE, 'demo');
  assert.equal(env.ASSET_STORAGE_BACKEND, 'local');
  for (const name of [
    'NEWAPI_BASE_URL',
    'AUTH_MAIL_HOST',
    'NEWAPI_API_KEY',
    'S3_ENDPOINT',
    'MODERATION_ENDPOINT',
    'MEDIA_MODERATION_ENDPOINT',
    'ORDER_CATALOG_FILE',
  ]) {
    assert.equal(Object.hasOwn(env, name), false);
  }
});
