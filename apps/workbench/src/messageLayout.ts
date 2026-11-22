// Estimated row heights are replaced with observed border-box heights. The
// prefix tree avoids traversing the loaded history on scroll or on each delta.
export class MessageLayout {
  readonly positions = new Map<string, number>();
  private readonly sums: Float64Array;
  private readonly heights: Float64Array;

  constructor(readonly ids: readonly string[], known: ReadonlyMap<string, number>, estimate = 128) {
    this.sums = new Float64Array(ids.length + 1);
    this.heights = new Float64Array(ids.length);
    ids.forEach((id, index) => {
      this.positions.set(id, index);
      const n = index + 1;
      this.heights[index] = known.get(id) ?? estimate;
      this.sums[n] += this.heights[index];
      const parent = n + (n & -n);
      if (parent < this.sums.length) this.sums[parent] += this.sums[n];
    });
  }

  measure(index: number, height: number) {
    if (index < 0 || index >= this.ids.length || !Number.isFinite(height) || height <= 0) return;
    const delta = height - this.heights[index];
    if (!delta) return;
    this.heights[index] = height;
    for (let n = index + 1; n < this.sums.length; n += n & -n) this.sums[n] += delta;
  }

  top(index: number) {
    let sum = 0;
    for (let n = Math.min(this.ids.length, index); n > 0; n -= n & -n) sum += this.sums[n];
    return sum;
  }

  indexAt(offset: number) {
    let index = 0;
    let sum = 0;
    for (let bit = 2 ** Math.floor(Math.log2(Math.max(1, this.ids.length))); bit >= 1; bit /= 2) {
      const next = index + bit;
      if (next < this.sums.length && sum + this.sums[next] <= offset) {
        index = next;
        sum += this.sums[next];
      }
    }
    return Math.min(index, this.ids.length - 1);
  }

  range(top: number, viewport: number): [number, number] {
    if (!this.ids.length) return [0, 0];
    return [this.indexAt(Math.max(0, top - viewport)), this.indexAt(top + viewport * 2) + 1];
  }
}

