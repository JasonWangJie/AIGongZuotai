import { ChildProcess, execFileSync, spawn } from 'node:child_process';
import { open, readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { performance } from 'node:perf_hooks';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const intervalMs = 100;
const ended = child => child.exitCode !== null || child.signalCode !== null;
const round = n => Math.round(n * 100) / 100;

async function within(promise, ms) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('owned process observation timed out')), ms);
    })]);
  } finally { clearTimeout(timer); }
}

async function windowsReader(child) {
  const program = join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
  const script = fileURLToPath(new URL('./owned-resource-sampler.ps1', import.meta.url));
  const helper = spawn(program, ['-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script], {
    windowsHide: true, stdio: ['pipe', 'pipe', 'ignore'],
  });
  const closed = new Promise(resolve => helper.once('close', resolve));
  // The stream may close independently when the target exits.
  helper.stdin.on('error', () => {});
  const lines = createInterface({ input: helper.stdout, crlfDelay: Infinity });
  const next = lines[Symbol.asyncIterator]();
  helper.once('error', () => {}); // spawn failure also closes stdout.
  const read = async timeout => {
    const line = await within(next.next(), timeout);
    return line.done ? 'unavailable' : line.value;
  };
  async function close() {
    helper.stdin.end();
    try { await within(closed, 1500); }
    catch { helper.kill(); await within(closed, 3000); }
    finally { lines.close(); }
  }
  try {
    helper.stdin.write(String(child.pid) + '\n');
    if (await read(8000) !== 'ready') throw new Error('owned process reader unavailable');
    return {
      source: 'windows_process', close,
      async sample() {
        helper.stdin.write('sample\n');
        const value = await read(1500);
        if (value === 'exited') return null;
        const fields = value.split(' ').map(Number);
        if (fields.length !== 4 || fields.some(n => !Number.isFinite(n) || n < 0)) throw new Error('invalid process counters');
        return { tick: fields[0], cpu: fields[1], rss: fields[2], handles: fields[3], fds: null };
      },
    };
  } catch (e) { await close(); throw e; }
}

async function linuxReader(child) {
  // A handle on this procfs inode continues to denote only this child, even if
  // the numeric PID is reused. stat's comm can contain spaces and parentheses.
  const proc = await open(`/proc/${child.pid}`, 'r');
  const path = '/proc/self/fd/' + proc.fd;
  const stat = async () => {
    const data = await readFile(path + '/stat', 'utf8');
    const fields = data.slice(data.lastIndexOf(')') + 2).split(' ');
    if (Number(fields[1]) !== process.pid) throw new Error('not an owned child');
    return fields;
  };
  try {
    const hz = Number(execFileSync('getconf', ['CLK_TCK'], { encoding: 'utf8', timeout: 2000, stdio: ['ignore', 'pipe', 'ignore'] }).trim());
    if (!Number.isFinite(hz) || hz <= 0) throw new Error('unknown CPU clock');
    await stat();
    return {
      source: 'linux_procfs', close: () => proc.close(),
      async sample() {
        const fields = await stat();
        if (fields[0] === 'Z' || fields[0] === 'X') return null;
        const [status, fds] = await Promise.all([
          readFile(path + '/status', 'utf8'),
          readdir(path + '/fd').then(entries => entries.length, () => null),
        ]);
        const rss = /^VmRSS:\s+(\d+)\s+kB$/m.exec(status);
        return { tick: performance.now(), cpu: (Number(fields[11]) + Number(fields[12])) * 1000 / hz,
          rss: rss ? Number(rss[1]) * 1024 : null, handles: null, fds };
      },
    };
  } catch (e) { await proc.close(); throw e; }
}

// This API accepts the ChildProcess returned by spawn, never a PID, hostname
// or endpoint. One reader stays attached across all phases of a single run.
export async function startOwnedResourceSampler(child) {
  if (!(child instanceof ChildProcess)) throw new TypeError('a directly spawned child process is required');
  const supported = process.platform === 'win32' || process.platform === 'linux';
  let reader, closed = false, active = false;
  let failure = supported ? (!child.pid || ended(child) ? 'process_exited' : null) : 'unsupported_platform';
  if (supported && child.pid && !ended(child)) {
    try { reader = await (process.platform === 'win32' ? windowsReader(child) : linuxReader(child)); }
    catch { failure = ended(child) ? 'process_exited' : 'reader_unavailable'; }
  }
  return {
    source: reader?.source ?? null, interval_ms: intervalMs,
    async phase(operation) {
      if (closed || active || typeof operation !== 'function') throw new Error('invalid owned sampler phase');
      active = true;
      let first, last, peakCpu = null, rss = null, handles = null, fds = null, samples = 0, missing = 0, missedTicks = 0;
      const counts = { cpu: 0, rss: 0, windows_handles: 0, linux_fds: 0 };
      const observe = async () => {
        if (ended(child)) failure = 'process_exited';
        if (!reader || failure) return;
        let point;
        try { point = await reader.sample(); }
        catch { failure = ended(child) ? 'process_exited' : 'reader_unavailable'; return; }
        if (!point) { failure = 'process_exited'; return; }
        if (!Number.isFinite(point.cpu) || !Number.isFinite(point.tick) || (last && (point.cpu < last.cpu || point.tick < last.tick))) {
          failure = 'invalid_counters'; return;
        }
        if (last && point.tick > last.tick) peakCpu = Math.max(peakCpu ?? 0, (point.cpu - last.cpu) * 100 / (point.tick - last.tick));
        first ??= point;
        last = point;
        samples++; counts.cpu++;
        if (point.rss !== null) { rss = Math.max(rss ?? 0, point.rss); counts.rss++; }
        if (point.handles !== null) { handles = Math.max(handles ?? 0, point.handles); counts.windows_handles++; }
        if (point.fds !== null) { fds = Math.max(fds ?? 0, point.fds); counts.linux_fds++; }
        if (point.rss === null || (reader.source === 'windows_process' ? point.handles === null : point.fds === null)) missing++;
      };
      let timer, pending;
      try {
        const start = new Date().toISOString();
        await observe();
        timer = setInterval(() => {
          if (pending) { missedTicks++; return; }
          pending = observe().finally(() => { pending = null; });
        }, intervalMs);
        const result = await operation();
        clearInterval(timer);
        await pending;
        await observe();
        const end = new Date().toISOString();
        const span = samples > 1 ? last.tick - first.tick : null;
        const cpu = span !== null ? last.cpu - first.cpu : null;
        return { result, resources: {
          status: failure ?? (samples < 2 ? 'insufficient_samples' : missing ? 'partial' : 'observed'),
          started_at: start, ended_at: end, samples, metric_samples: counts, busy_ticks_skipped: missedTicks,
          sample_span_ms: span === null ? null : round(span),
          cpu_time_delta_ms: cpu === null ? null : round(cpu),
          cpu_pct_one_core_mean: span > 0 ? round(cpu * 100 / span) : null,
          cpu_pct_one_core_peak_interval: peakCpu === null ? null : round(peakCpu),
          peak_rss_bytes_observed: rss, peak_windows_handles_observed: handles, peak_linux_fds_observed: fds,
        } };
      } finally {
        clearInterval(timer); await pending; active = false;
      }
    },
    async close() {
      if (active) throw new Error('cannot close an active sampler phase');
      if (!closed) { closed = true; await reader?.close(); }
    },
  };
}
