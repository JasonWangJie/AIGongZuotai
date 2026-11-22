import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { createPitrDrill } from '../recovery/pg-pitr-drill.mjs';
import { createLocalIngressTLS, requestLocalIngress, startLocalIngress } from './local-ingress.mjs';
import { createReleaseProcess } from './release-process.mjs';
import { verifyReleaseArtifact } from './release-artifact.mjs';

const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url));
const identityPath = '/release-identity.json';
const missingPath = '/api/v1/__missing_release_probe__';
let phase = 'arguments';

class EvidenceError extends Error {}
function requireEvidence(condition, message) {
  if (!condition) throw new EvidenceError(message);
}

async function exercise(services, packages) {
  const tls = await createLocalIngressTLS();
  const previous = await services.start('previous');
  const gateway = await startLocalIngress({ ...tls, initialTarget: previous.origin });
  const get = (pathname, headers = {}) => requestLocalIngress(gateway.origin, {
    cert: tls.cert, path: pathname, headers,
  });
  const attempts = [];
  try {
    await services.seed();
    async function inspect(release, target, rejectCandidate = false) {
      return services.withProbe(async ({ userId, cookie, paths, expected, headers }) => {
        requireEvidence(paths.length >= 2, 'An authenticated asset and a business receipt are required.');
        const pathsToCheck = ['/readyz', identityPath, ...paths];
        async function verifyPaths() {
          const identity = await get(identityPath, { cookie });
          requireEvidence(identity.status === 200, 'The release identity is unavailable through TLS.');
          const actual = JSON.parse(identity.body.toString('utf8'));
          const planned = packages[release].identity;
          requireEvidence(Object.keys(planned).length === Object.keys(actual).length &&
            Object.keys(planned).every(k => actual[k] === planned[k]),
            'Ingress returned a different release identity.');
          requireEvidence((await get('/readyz', { cookie })).status === 200, 'The writer is not ready.');
          for (const p of paths) {
            const answer = await get(p, { ...headers[p], cookie });
            expected[p](answer.status, answer.body);
          }
        }
        if (target) {
          gateway.candidate(target, { userId, paths: rejectCandidate ? [...pathsToCheck, missingPath] : pathsToCheck });
          requireEvidence((await get(identityPath)).status === 403, 'Unauthenticated candidate request was admitted.');
          requireEvidence((await get('/api/v1/bootstrap', { cookie })).status === 503,
            'Candidate allowed a request outside its inspection list.');
        }
        await verifyPaths();
        if (rejectCandidate) {
          requireEvidence((await get(missingPath, { cookie })).status !== 200, 'Failure probe unexpectedly succeeded.');
          let refused = false;
          try { gateway.open(); } catch { refused = true; }
          requireEvidence(refused, 'Candidate was opened with an incomplete probe.');
          requireEvidence((await get(identityPath)).status === 403, 'Failed candidate became generally accessible.');
        } else if (target) {
          gateway.open();
          requireEvidence((await get(identityPath)).status === 200, 'Approved release did not open.');
          await verifyPaths();
        }
        return paths.length;
      });
    }
    async function transition(release, rejectCandidate = false) {
      const start = performance.now();
      phase = 'maintenance and writer handoff';
      await gateway.maintain();
      requireEvidence((await get(identityPath)).status === 503, 'Maintenance admitted a new request.');
      await services.stop();
      const locks = await services.assertQuiescent();
      requireEvidence(locks.postgresAdvisory && locks.referenceOS && locks.assetOS,
        'The database and both private directory locks must be free together.');
      const next = await services.start(release);
      requireEvidence(next.artifactID === packages[release].identity.artifact_id,
        'The selected writer does not match the pinned artifact.');
      phase = 'candidate inspection';
      const confirmed = await inspect(release, next.origin, rejectCandidate);
      if (!rejectCandidate) await services.verify();
      attempts.push({ release, admitted: !rejectCandidate, handoff_ms: Math.round(performance.now() - start),
        database_lock_released: true, private_locks_released: 2, verified_private_reads: confirmed });
    }
    await inspect('previous');
    await transition('candidate', true);
    await transition('previous');
    await transition('candidate');
    phase = 'final application read and shutdown';
    await gateway.maintain();
    await services.stop();
    await services.assertQuiescent();
    return { scope: 'isolated loopback TLS, one PostgreSQL writer', transitions: attempts,
      baseline: packages.previous.identity.artifact_id, candidate: packages.candidate.identity.artifact_id,
      original_private_asset_and_receipt: true };
  } finally {
    await gateway.close();
  }
}

async function main() {
  const { values } = parseArgs({ options: { previous: { type: 'string' }, candidate: { type: 'string' } } });
  requireEvidence(values.previous && values.candidate, 'Specify both --previous and --candidate artifact directories.');
  const artifacts = { previous: path.resolve(values.previous), candidate: path.resolve(values.candidate) };
  const packages = {
    previous: await verifyReleaseArtifact(artifacts.previous, { role: 'baseline' }),
    candidate: await verifyReleaseArtifact(artifacts.candidate, { role: 'candidate' }),
  };
  requireEvidence(packages.previous.artifactId !== packages.candidate.artifactId, 'Two distinct build identities are required.');
  const oldFormat = packages.previous.identity;
  const newFormat = packages.candidate.identity;
  requireEvidence(Number.isSafeInteger(oldFormat.schema_version) && oldFormat.schema_version > 0 &&
    /^[a-f0-9]{64}$/.test(oldFormat.schema_sha256) &&
    oldFormat.schema_version === newFormat.schema_version && oldFormat.schema_sha256 === newFormat.schema_sha256,
    'In-place handoff requires identical database migrations.');
  phase = 'isolated PostgreSQL startup';
  const pg = await createPitrDrill();
  try {
    await pg.startSource();
    const report = await pg.withSource(async ({ databaseURL, drillId }) => {
      const services = await createReleaseProcess({ repositoryRoot, privateRoot: pg.privateRoot, databaseURL, drillId, artifacts });
      try { return await exercise(services, packages); }
      finally { await services.close(); }
    });
    process.stdout.write(JSON.stringify(report, null, 2) + '\n');
  } finally { await pg.close(); }
}

main().catch(error => {
  const message = error instanceof EvidenceError ? error.message : 'Validation or process operation failed.';
  console.error(`Local release drill stopped at ${phase}. ${message}`);
  process.exitCode = 1;
});
