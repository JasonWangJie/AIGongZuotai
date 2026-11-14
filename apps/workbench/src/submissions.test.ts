import { describe, expect, it } from 'vitest';
import { buildTaskInput, MAX_PROMPT_UNITS } from './submissions';

describe('media submission contract', () => {
  it('preserves an image batch of four but submits one video when switching modes', () => {
    const previousImageCount = 4;
    expect(buildTaskInput('p1', 'image', 'studio-image', '中文画面', '16:9', previousImageCount).parameters.count).toBe(4);
    expect(buildTaskInput('p1', 'video', 'studio-video', '镜头描述', '16:9', previousImageCount).parameters.count).toBe(1);
  });
  it('never sends invalid image counts from stale local state', () => {
    for (const count of [0, -1, 8, 1.5, Number.NaN]) expect(buildTaskInput('p1', 'image', 'studio-image', '画面', '1:1', count).parameters.count).toBe(1);
  });
  it('keeps the longest supported Chinese prompt below the server byte cap', () => {
    const prompt = '画'.repeat(MAX_PROMPT_UNITS);
    expect(new TextEncoder().encode(prompt).byteLength).toBe(15000);
    expect(new TextEncoder().encode(prompt).byteLength).toBeLessThanOrEqual(16000);
  });
});
