import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { root, binary, run, runNpm, loadLocalEnv } from './lib/process.mjs';

async function build() {
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  await runNpm(['run', 'build']);
  await run('go', ['build', '-trimpath', '-o', binary, './cmd/server']);
}
async function test() {
  await run(process.execPath, ['--test', 'scripts/tasks.test.mjs']);
  await run('go', ['test', './...']);
  await runNpm(['run', 'test']);
}
try {
  switch (process.argv[2]) {
    case 'setup': await runNpm(['ci']); break;
    case 'build': await build(); break;
    case 'test': await test(); break;
    case 'test:db':
      loadLocalEnv();
      loadLocalEnv('.runtime/postgres.env');
      if (!process.env.TEST_DATABASE_URL) throw new Error('TEST_DATABASE_URL is required. Configure a disposable test database or run npm run db:setup on Windows.');
      await run('go', ['test', '-count=1', './...']);
      break;
    case 'check':
      await run(process.execPath, ['scripts/tasks.mjs', '--check']);
      await run('go', ['vet', './...']);
      await test();
      await build();
      await run(process.execPath, ['scripts/check-docs.mjs']);
      break;
    default: throw new Error('Usage: npm run setup | dev | build | test | check');
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
