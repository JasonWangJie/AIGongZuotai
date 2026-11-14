import { spawn, spawnSync } from 'node:child_process';
import { existsSync, readFileSync, mkdirSync, mkdtempSync, realpathSync, rmSync } from 'node:fs';
import { dirname, resolve, relative, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseEnv } from 'node:util';

export const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
export const web = resolve(root, 'apps/workbench');
export const binary = resolve(root, '.runtime', process.platform === 'win32' ? 'server.exe' : 'server');

// Isolated smoke/performance processes cannot inherit a developer's mail channel.
// Explicit numeric defaults also remain valid under strict environment parsing.
export const isolatedMailEnvironment = Object.freeze({
  AUTH_MAIL_ENABLED: 'false', AUTH_MAIL_HOST: '', AUTH_MAIL_PORT: '465',
  AUTH_MAIL_TLS_MODE: 'implicit_tls', AUTH_MAIL_USERNAME: '', AUTH_MAIL_PASSWORD: '',
  AUTH_MAIL_FROM: '', AUTH_MAIL_PUBLIC_ORIGIN: '', AUTH_MAIL_OUTBOX_KEY: '',
  AUTH_MAIL_TIMEOUT_SECONDS: '20', AUTH_MAIL_WORKERS: '1',
});

export const isolatedBillingEnvironment = Object.freeze({ BILLING_MODE: "disabled", BILLING_PRICE_FILE: "" });
export const isolatedOperationsEnvironment = Object.freeze({ OPERATIONS_ENABLED: "false" });
export const isolatedModerationEnvironment = Object.freeze({ MODERATION_MODE: 'disabled', MODERATION_POLICY_FILE: '', MODERATION_ENDPOINT: '', MODERATION_API_KEY: '', MODERATION_CREDENTIAL_ID: '' });

// Every isolated service gets a private reference tree. Cleanup only owns the
// exact newly created child, never the developer's REFERENCE_DIR or assets.
export function isolatedReferenceDirectory() {
  const runtime = resolve(root, '.runtime');
  mkdirSync(runtime, { recursive: true });
  const ownedDirectory = mkdtempSync(resolve(runtime, 'reference-check-'));
  const directory = resolve(ownedDirectory, 'references');
  const assetDirectory = resolve(ownedDirectory, 'assets');
  return { directory, assetDirectory, cleanup() {
    if (!existsSync(ownedDirectory)) return;
    const resolved = realpathSync(ownedDirectory);
    const inside = relative(realpathSync(runtime), resolved);
    if (!inside || inside.startsWith('..') || isAbsolute(inside) || dirname(resolve(ownedDirectory)) !== runtime) throw new Error('Refusing private test cleanup outside its owned runtime child');
    rmSync(ownedDirectory, { recursive: true, force: true });
  } };
}

export function loadLocalEnv(relative = '.env') {
  const path = resolve(root, relative);
  if (!existsSync(path)) return;
  const values = parseEnv(readFileSync(path, 'utf8'));
  for (const [key, value] of Object.entries(values)) {
    if (process.env[key] === undefined) process.env[key] = value;
  }
}

export function npmCommand(args) {
  const candidates = [
    process.env.npm_execpath,
    resolve(dirname(process.execPath), 'node_modules/npm/bin/npm-cli.js'),
    resolve(dirname(process.execPath), '../lib/node_modules/npm/bin/npm-cli.js'),
  ].filter(Boolean);
  const cli = candidates.find(path => existsSync(path));
  if (!cli) throw new Error('npm CLI not found. Run this script through npm, for example: npm run dev.');
  return [process.execPath, [cli, ...args]];
}

export function start(command, args, options = {}) {
  return spawn(command, args, { cwd: root, env: process.env, stdio: 'inherit', windowsHide: true, ...options });
}

export function run(command, args, options) {
  return new Promise((resolvePromise, reject) => {
    const child = start(command, args, options);
    child.once('error', reject);
    child.once('exit', (code, signal) => code === 0
      ? resolvePromise()
      : reject(new Error(`${command} exited with ${signal ?? code}`)));
  });
}

export function runNpm(args, options) {
  const [command, commandArgs] = npmCommand(args);
  return run(command, commandArgs, { cwd: web, ...options });
}

export function stopOwnedProcess(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  if (process.platform === 'win32') {
    spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore', windowsHide: true });
  } else {
    child.kill('SIGTERM');
  }
}
