import { mkdirSync, unlinkSync } from 'node:fs';
import { resolve } from 'node:path';
import net from 'node:net';
import { root, web, loadLocalEnv, npmCommand, run, start, stopOwnedProcess } from './lib/process.mjs';

loadLocalEnv();
if (process.argv.includes('--database')) {
  loadLocalEnv('.runtime/postgres.env');
  if (!process.env.DATABASE_URL) throw new Error('DATABASE_URL is required. Run npm run db:setup on Windows, or configure a PostgreSQL URL in .env.');
  process.env.AUTH_MODE = 'required';
}
const children = [];
const devBinary = resolve(root, '.runtime', `dev-server-${process.pid}${process.platform === 'win32' ? '.exe' : ''}`);
let stopping = false;
function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const child of children) stopOwnedProcess(child);
  process.exitCode = code;
}
process.on('SIGINT', () => stop());
process.on('SIGTERM', () => stop());
process.on('exit', () => {
  for (const child of children) stopOwnedProcess(child);
  try { unlinkSync(devBinary); } catch { /* A locked binary can be removed after the owned process exits. */ }
});

function assertFree(host, port) {
  return new Promise((resolvePromise, reject) => {
    const server = net.createServer();
    server.once('error', () => reject(new Error(`${host}:${port} is occupied. Stop the existing local server first.`)));
    server.listen(port, host, () => server.close(resolvePromise));
  });
}
try {
  // The Vite proxy uses this fixed local development endpoint.
  if (process.env.APP_ADDR && process.env.APP_ADDR !== '127.0.0.1:8080') {
    throw new Error('npm run dev uses APP_ADDR=127.0.0.1:8080. Run the built server separately for a custom address.');
  }
  await assertFree('127.0.0.1', 8080);
  await assertFree('127.0.0.1', 5173);
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  await run('go', ['build', '-o', devBinary, './cmd/server']);
  const backend = start(devBinary, [], { env: { ...process.env, APP_ADDR: '127.0.0.1:8080' } });
  children.push(backend);
  backend.once('error', error => { console.error(error.message); stop(1); });
  backend.once('exit', code => { if (!stopping) { console.error(`Go server exited (${code}).`); stop(code || 1); } });
  for (let attempt = 0; attempt < 60; attempt++) {
    if (stopping) throw new Error('Backend failed to start.');
    try {
      const response = await fetch('http://127.0.0.1:8080/readyz', { signal: AbortSignal.timeout(800) });
      if (response.ok) break;
    } catch { /* bounded startup retry */ }
    if (attempt === 59) throw new Error('Backend did not become ready within 15 seconds.');
    await new Promise(resolvePromise => setTimeout(resolvePromise, 250));
  }
  const [command, args] = npmCommand(['run', 'dev']);
  const frontend = start(command, args, { cwd: web });
  children.push(frontend);
  frontend.once('error', error => { console.error(error.message); stop(1); });
  frontend.once('exit', code => { if (!stopping) stop(code || 1); });
  console.log('\nWorkbench: http://127.0.0.1:5173\nAPI: http://127.0.0.1:8080\nCtrl+C stops both owned processes.\n');
} catch (error) {
  console.error(error.message);
  stop(1);
}
