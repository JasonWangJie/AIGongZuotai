import { createHash, randomBytes } from 'node:crypto';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { createPitrDrill } from './pg-pitr-drill.mjs';
import { createReleaseProcess, requestReleaseFixture } from '../release/release-process.mjs';

const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url));
const sha256 = (data) => createHash('sha256').update(data).digest('hex');
const key = () => randomBytes(24).toString('hex');
const confirmedDrafts = 4;

function check(ok, message) {
  if (!ok) throw new Error(message);
}

async function createOtherSession(origin) {
  const result = await requestReleaseFixture(
    origin,
    '/api/v1/auth/register',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        email: 'standby-' + key() + '@example.test',
        password: key(),
        name: 'Standby fixture',
      }),
    },
    201,
  );
  const cookie = result.headers
    .getSetCookie()
    .find((value) => value.startsWith('studio_session='))
    ?.split(';')[0];
  check(cookie, 'second account could not establish a session');
  return cookie;
}

async function confirmedOnSource(origin, probe) {
  const [image, definition] = probe.paths;
  const project = probe.headers[image]['X-Project-ID'];
  check(/^prj_[0-9a-f]{32}$/.test(project), 'unexpected original project');
  const original = await requestReleaseFixture(origin, definition, {
    headers: { Cookie: probe.cookie, ...probe.headers[definition] },
  });
  probe.expected[definition](original.status, original.body);
  const accepted = [{ path: definition, headers: probe.headers[definition], digest: sha256(original.body) }];
  const draft = '/api/v1/collaboration/projects/' + project + '/draft';
  for (let revision = 1; revision <= confirmedDrafts; revision++) {
    const headers = { 'Idempotency-Key': key() };
    const nodes = [{ id: 'scene', title: 'Local continuity', note: key() }];
    const write = await requestReleaseFixture(origin, draft, {
      method: 'PUT',
      headers: { Cookie: probe.cookie, ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify({ expected_revision: revision - 1, nodes }),
    });
    const document = JSON.parse(write.body);
    check(
      document.project_id === project &&
        document.revision === revision &&
        JSON.stringify(document.nodes) === JSON.stringify(nodes),
      'source did not confirm the intended revision',
    );
    const read = await requestReleaseFixture(origin, draft + '/receipt', {
      headers: { Cookie: probe.cookie, ...headers },
    });
    check(sha256(read.body) === sha256(write.body), 'original save and read-only receipt differ');
    accepted.push({ path: draft + '/receipt', headers, digest: sha256(read.body) });
  }
  return accepted;
}

// Only a 404 for an independently authorized original operation counts as
// missing. An incorrect body is counted separately, and other errors fail.
export function countOriginalReceipts(confirmed, results) {
  check(confirmed.length > 0 && confirmed.length === results.length, 'incomplete set of original responses');
  let missing = 0,
    mismatched = 0;
  for (let i = 0; i < confirmed.length; i++) {
    const response = results[i];
    if (response.status === 404) missing++;
    else {
      check(response.status === 200, 'original account could not read its receipts');
      if (sha256(response.body) !== confirmed[i].digest) mismatched++;
    }
  }
  return {
    confirmed: confirmed.length,
    recovered: confirmed.length - missing - mismatched,
    missing,
    mismatched,
  };
}

async function verifyAfterPromotion(origin, probe, accepted, foreignCookie) {
  const image = probe.paths[0];
  const auth = { Cookie: probe.cookie };
  const bytes = await requestReleaseFixture(origin, image, { headers: { ...auth, ...probe.headers[image] } });
  probe.expected[image](bytes.status, bytes.body);
  await requestReleaseFixture(origin, image, {}, 401);
  await requestReleaseFixture(
    origin,
    image,
    { headers: { Cookie: foreignCookie, ...probe.headers[image] } },
    404,
  );
  const responses = [];
  for (const record of accepted) {
    responses.push(
      await requestReleaseFixture(
        origin,
        record.path,
        { headers: { ...auth, ...record.headers } },
        [200, 404],
      ),
    );
  }
  return countOriginalReceipts(accepted, responses);
}

export async function runSyncStandbyDrill(artifact, progress = () => {}) {
  check(typeof artifact === 'string' && artifact.length > 0, 'a local baseline artifact is required');
  const { verifyReleaseArtifact } = await import('../release/release-artifact.mjs');
  const pinned = await verifyReleaseArtifact(path.resolve(artifact), { role: 'baseline' });
  const pg = await createPitrDrill({ synchronousStandby: true });
  let app;
  try {
    progress('new source and verified physical backup');
    await pg.startSource();
    await pg.takeBaseBackup();
    progress('synchronous physical replication');
    await pg.startSynchronousStandby();
    let accepted, foreignCookie, artifactDigest;
    progress('seed ordinary authorized business');
    await pg.withSource(async ({ databaseURL, drillId }) => {
      app = await createReleaseProcess({
        repositoryRoot,
        privateRoot: pg.privateRoot,
        databaseURL,
        drillId,
        artifacts: { previous: path.resolve(artifact) },
        synchronousStandby: true,
      });
      const source = await app.start('previous');
      check(
        source.binarySHA256 === pinned.binarySha256 && source.artifactID === pinned.artifactId,
        'local source artifact changed after verification',
      );
      artifactDigest = source.binarySHA256;
      const seeded = await app.seed();
      check(seeded.originalReceipt && seeded.originalUser, 'original business proof missing');
      foreignCookie = await createOtherSession(source.origin);
      accepted = await app.withProbe((probe) => confirmedOnSource(source.origin, probe));
    });
    // Measure loss of service from the start of planned application shutdown
    // through the first verified successful use by its original account.
    const start = performance.now();
    let oldAppOff;
    progress('stop application, verify free locks, fence original PostgreSQL, promote');
    await pg.fenceAndPromote(async () => {
      const exit = await app.stop();
      const locks = await app.assertQuiescent();
      oldAppOff = Math.round(performance.now() - start);
      return { exit, locks };
    });
    const promotedAt = Math.round(performance.now() - start);
    progress('same binary and original business on promoted timeline');
    const checked = await pg.withRestored(async (binding) => {
      await app.bindPromotedFixture(binding);
      const restarted = await app.start('previous');
      check(restarted.binarySHA256 === artifactDigest, 'reopened artifact differs');
      const readyAt = Math.round(performance.now() - start);
      const receipts = await app.withProbe((probe) =>
        verifyAfterPromotion(restarted.origin, probe, accepted, foreignCookie),
      );
      if (receipts.missing || receipts.mismatched) {
        const error = new Error('confirmed business continuity check failed');
        error.receiptCounts = receipts;
        throw error;
      }
      const owner = await app.verify(); // same credentials, newly authenticated session
      check(owner.originalUser && owner.originalReceipt, 'original account identity did not recover');
      return { readyAt, receipts, recoveredAt: Math.round(performance.now() - start) };
    });
    const evidence = pg.evidence();
    progress('stop reopened application and original fixture members');
    await app.stop();
    await app.assertQuiescent();
    return {
      scope: 'Windows loopback PostgreSQL synchronous physical replication and planned single-writer cutover',
      postgres: evidence.server_version,
      artifact_sha256: artifactDigest,
      base_manifest_sha256: evidence.base_manifest_sha256,
      replication: {
        before_business: evidence.sync_start_coverage,
        after_ack_and_quiescence: evidence.sync_final_coverage,
        timelines: evidence.sync_promotion,
      },
      old_source_detached: evidence.sync_source_fenced,
      confirmed_receipts: checked.receipts,
      original_cookie: true,
      original_credentials: true,
      private_original_sha256_verified: true,
      anonymous_denied: true,
      other_account_denied: true,
      shared_private_directories: true,
      cutover_ms: {
        original_service_exit_and_three_free_locks: oldAppOff,
        old_primary_fenced_and_new_promoted: promotedAt,
        same_binary_ready_and_locks_held: checked.readyAt,
        original_account_and_business_verified: checked.recoveredAt,
      },
    };
  } finally {
    try {
      if (app) await app.close();
    } finally {
      await pg.close();
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  let phase = 'arguments';
  try {
    const { values } = parseArgs({ options: { artifact: { type: 'string' } } });
    const report = await runSyncStandbyDrill(values.artifact, (value) => {
      phase = value;
    });
    console.log(JSON.stringify(report, null, 2));
  } catch (error) {
    const counts = error.receiptCounts ? ' ' + JSON.stringify(error.receiptCounts) : '';
    console.error('Local standby drill failed at ' + phase + '.' + counts);
    process.exitCode = 1;
  }
}
