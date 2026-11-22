import * as fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { parseLSN } from './pg-pitr-drill.mjs';

const replica = 'studio_drill_standby';
const synchronousNames = 'FIRST 1 (' + replica + ')';

function requireFact(value, message) {
  if (!value) throw new Error(message);
}

export function standbyCovers(source, standby, barrier) {
  if (
    source.replicationCount !== 1 ||
    source.localRecovery !== false ||
    source.commit !== 'remote_apply' ||
    source.names !== synchronousNames ||
    source.fsync !== true ||
    source.fullPages !== true ||
    source.scram !== true ||
    standby.fsync !== true ||
    source.state !== 'streaming' ||
    source.sync !== 'sync' ||
    source.name !== replica ||
    source.localClient !== true ||
    standby.recovery !== true ||
    standby.receiver !== 'streaming' ||
    standby.fromSource !== true ||
    source.system !== standby.system ||
    !/^[0-9]+$/.test(source.system)
  ) {
    return false;
  }
  const target = parseLSN(barrier);
  return [source.flush, source.replay, standby.flush, standby.replay].every(
    (value) => typeof value === 'string' && parseLSN(value) >= target,
  );
}

export function verifyCutoverFence(exit, locks) {
  requireFact(
    exit?.graceful === true &&
      exit.exitCode === 0 &&
      locks?.postgresAdvisory === true &&
      locks.referenceOS === true &&
      locks.assetOS === true,
    'original service exit and all three free locks are required',
  );
}

async function provePortFree(port) {
  const listener = net.createServer();
  await new Promise((resolve, reject) => {
    listener.once('error', reject);
    listener.listen({ host: '127.0.0.1', port, exclusive: true }, resolve);
  });
  await new Promise((resolve, reject) => listener.close((err) => (err ? reject(err) : resolve())));
}

export function createStandbyBranch(fixture) {
  const { step, sql, start, halt, dirs, facts, until, configValue } = fixture;
  let sourceTimeline;
  async function observation() {
    const primary = JSON.parse(
      await sql(
        'source',
        `
      SELECT json_build_object(
        'localRecovery',pg_is_in_recovery(),'system',(SELECT system_identifier::text FROM pg_control_system()),
        'commit',current_setting('synchronous_commit'),'names',current_setting('synchronous_standby_names'),
        'fsync',current_setting('fsync')='on','fullPages',current_setting('full_page_writes')='on',
        'scram',(SELECT bool_and(auth_method='scram-sha-256') FROM pg_hba_file_rules
          WHERE type='host' AND 'replication'=ANY(database)),
        'replicationCount',(SELECT count(*) FROM pg_stat_replication),'name',s.application_name,
        'state',s.state,'sync',s.sync_state,'localClient',s.client_addr='127.0.0.1'::inet,
        'flush',s.flush_lsn::text,'replay',s.replay_lsn::text)
      FROM (SELECT 1) seed LEFT JOIN pg_stat_replication s ON s.application_name='${replica}'`,
      ),
    );
    const standby = JSON.parse(
      await sql(
        'restored',
        `
      SELECT json_build_object('recovery',pg_is_in_recovery(),'fsync',current_setting('fsync')='on',
        'system',(SELECT system_identifier::text FROM pg_control_system()),
        'receiver',w.status,'fromSource',w.sender_host='127.0.0.1' AND w.sender_port=${fixture.sourcePort()},
        'flush',w.flushed_lsn::text,'replay',pg_last_wal_replay_lsn()::text)
      FROM (SELECT 1) seed LEFT JOIN pg_stat_wal_receiver w ON true`,
      ),
    );
    return { primary, standby };
  }
  async function checkCoverage(marker) {
    const wanted = await sql('source', 'SELECT pg_current_wal_flush_lsn()::text');
    // A WAL switch can flush zero padding up to a segment boundary. A later
    // committed record gives physical replay a real record beyond that mark.
    await sql('source', "INSERT INTO pitr_drill_marker(phase) VALUES ('" + marker + "')");
    let result;
    await until(async () => {
      result = await observation();
      return (
        result.primary.system === facts.system_identifier &&
        standbyCovers(result.primary, result.standby, wanted)
      );
    }, 'sync replica flush and replay coverage');
    return {
      barrier: wanted,
      commit: result.primary.commit,
      senderState: result.primary.state,
      syncState: result.primary.sync,
      receiverState: result.standby.receiver,
      senders: result.primary.replicationCount,
      primaryFsync: result.primary.fsync,
      standbyFsync: result.standby.fsync,
      fullPageWrites: result.primary.fullPages,
      replicationHbaScram: result.primary.scram,
      senderFlush: result.primary.flush,
      senderReplay: result.primary.replay,
      receiverFlush: result.standby.flush,
      receiverReplay: result.standby.replay,
    };
  }
  async function fenceOldPrimary() {
    const pid = fixture.sourcePID();
    requireFact(Number.isSafeInteger(pid) && pid > 0, 'owned source pid is required');
    const source = await fs.realpath(dirs.source);
    requireFact(
      source.toLowerCase() === dirs.source.toLowerCase() &&
        path.dirname(dirs.source) === path.dirname(dirs.detached),
      'source directory escaped the private fixture',
    );
    try {
      await fs.lstat(dirs.detached);
      throw new Error('fence destination is occupied');
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    await halt('source');
    await until(
      () => {
        try {
          process.kill(pid, 0);
          return false;
        } catch (error) {
          if (error.code === 'ESRCH') return true;
          throw error;
        }
      },
      'confirmed source postmaster exit',
      10000,
    );
    await provePortFree(fixture.sourcePort());
    await fs.rename(dirs.source, dirs.detached);
    facts.sync_source_fenced = true;
  }
  return {
    startSynchronousStandby: () =>
      step('base_ready', 'sync_ready', async () => {
        requireFact(
          (await fixture.sha256(path.join(dirs.base, 'backup_manifest'))) === facts.base_manifest_sha256,
          'base backup identity changed',
        );
        await fixture.tool('pg_verifybackup', [dirs.base], 'reverify independent standby backup', 180000);
        await fixture.assertNoLinks(dirs.base);
        await fs.cp(dirs.base, dirs.restored, { recursive: true, force: false, errorOnExist: true });
        await fixture.tool('pg_verifybackup', [dirs.restored], 'verify actual standby copy', 180000);
        const targetPort = await fixture.allocateRestoredPort();
        requireFact(targetPort !== fixture.sourcePort(), 'standby needs its own port');
        const passfile = fixture.passfile.replaceAll('\\', '/');
        requireFact(!/['\r\n]/.test(passfile), 'unexpected private password file path');
        const conninfo = `host=127.0.0.1 port=${fixture.sourcePort()} user=${fixture.user} application_name=${replica} passfile='${passfile}' sslmode=disable connect_timeout=5`;
        const standbyConfig = [
          "listen_addresses='127.0.0.1'",
          'port=' + targetPort,
          'hot_standby=on',
          'archive_mode=off',
          "archive_library=''",
          "synchronous_standby_names=''",
          'synchronous_commit=on',
          "recovery_target_timeline='latest'",
          'primary_conninfo=' + configValue(conninfo),
        ];
        await fs.appendFile(
          path.join(dirs.restored, 'postgresql.conf'),
          '\n' + standbyConfig.join('\n') + '\n',
        );
        await fs.writeFile(path.join(dirs.restored, 'standby.signal'), '', { flag: 'wx', mode: 0o600 });
        await start('restored', targetPort);
        await fixture.assertIdentity('restored');
        await fs.appendFile(
          path.join(dirs.source, 'postgresql.conf'),
          '\nsynchronous_commit=remote_apply\nsynchronous_standby_names=' +
            configValue(synchronousNames) +
            '\n',
        );
        requireFact(
          (await sql('source', 'SELECT pg_reload_conf()')) === 't',
          'could not enable synchronous replication',
        );
        facts.sync_start_coverage = await checkCoverage('sync_ready');
        sourceTimeline = await sql('source', 'SELECT left(pg_walfile_name(pg_current_wal_lsn()),8)');
        requireFact(/^[A-F0-9]{8}$/.test(sourceTimeline), 'invalid source timeline');
      }),
    fenceAndPromote: (releaseSource) =>
      step('sync_ready', 'sync_promoted', async () => {
        requireFact(typeof releaseSource === 'function', 'a verified service exit is required');
        const proof = await releaseSource();
        verifyCutoverFence(proof?.exit, proof?.locks);
        const free = JSON.parse(
          await sql(
            'source',
            `
          SELECT json_build_object('writerFree',pg_try_advisory_lock(${0x53545544494fn}::bigint),
            'storage',storage_id::text) FROM studio_storage_identity WHERE singleton`,
            'studio',
          ),
        );
        requireFact(
          free.writerFree === true &&
            free.storage === proof.locks.databaseID &&
            proof.locks.drillId === facts.drill_id,
          'source database did not corroborate the released application',
        );
        // No workbench writer remains. This flush position follows all observed
        // client commits and the original application's graceful shutdown.
        facts.sync_final_coverage = await checkCoverage('sync_final');
        const barrier = facts.sync_final_coverage.barrier;
        await fenceOldPrimary();
        const before = JSON.parse(
          await sql(
            'restored',
            `SELECT json_build_object(
        'recovery',pg_is_in_recovery(),'system',(SELECT system_identifier::text FROM pg_control_system()),
        'replayed',pg_last_wal_replay_lsn()::text)`,
          ),
        );
        requireFact(
          before.recovery === true &&
            before.system === facts.system_identifier &&
            parseLSN(before.replayed) >= parseLSN(barrier),
          'fenced standby does not cover acknowledged writes',
        );
        requireFact(
          (await sql('restored', 'SELECT pg_promote(true,30)')) === 't',
          'standby promotion was not confirmed',
        );
        const promoted = JSON.parse(
          await sql(
            'restored',
            `SELECT json_build_object(
        'primary',NOT pg_is_in_recovery(),'system',(SELECT system_identifier::text FROM pg_control_system()),
        'timeline',left(pg_walfile_name(pg_current_wal_lsn()),8),
        'writable',current_setting('transaction_read_only')='off')`,
          ),
        );
        requireFact(
          promoted.primary === true &&
            promoted.writable === true &&
            promoted.system === facts.system_identifier &&
            /^[A-F0-9]{8}$/.test(promoted.timeline) &&
            parseInt(promoted.timeline, 16) > parseInt(sourceTimeline, 16),
          'new writable timeline was not proven',
        );
        await fixture.assertIdentity('restored');
        facts.sync_promotion = {
          oldTimeline: sourceTimeline,
          newTimeline: promoted.timeline,
          replayedBeforePromotion: before.replayed,
          sameSystem: true,
          oldPIDExited: true,
          oldPortFree: true,
        };
      }),
  };
}
