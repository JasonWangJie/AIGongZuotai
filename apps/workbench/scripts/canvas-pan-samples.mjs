export function frameStats(intervals) {
  if (!Array.isArray(intervals) || intervals.length < 2 ||
      intervals.some(value => !Number.isFinite(value) || value <= 0)) {
    throw Error('incomplete animation frame distribution');
  }
  const sorted = [...intervals].sort((a, b) => a - b);
  const percentile = q => sorted[Math.ceil(sorted.length * q) - 1];
  return {
    frames: intervals.length, total_ms: intervals.reduce((sum, value) => sum + value, 0),
    p50_ms: percentile(.5), p95_ms: percentile(.95), p99_ms: percentile(.99), max_ms: sorted.at(-1),
  };
}

export function classifyRenderer(info) {
  if (/swiftshader|llvmpipe|softpipe|software rasterizer|microsoft basic render|\bwarp\b/i.test(info.renderer) ||
      info.compositing === 'disabled_software' || info.compositing === 'unavailable_software') return 'software';
  if (/^(enabled|enabled_on)$/.test(info.compositing) &&
      /intel|nvidia|amd|radeon|apple m[1-9]|qualcomm|adreno/i.test(info.renderer)) return 'hardware';
  return 'unknown';
}

