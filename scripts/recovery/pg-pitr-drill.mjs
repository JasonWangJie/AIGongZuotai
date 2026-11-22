import { execFile, spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { once } from 'node:events';
import * as fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';

const runFile = promisify(execFile);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const segPattern = /^[0-9A-F]{24}$/;
const lsnPattern = /^[0-9A-F]{1,8}\/[0-9A-F]{1,8}$/;
const markerPattern = /^v18a_[0-9a-f]{24}$/;

export function parseLSN(value) {
  if (!lsnPattern.test(value)) throw new Error('invalid PostgreSQL LSN');
  const [hi, lo] = value.split('/');
  return (BigInt('0x' + hi) << 32n) + BigInt('0x' + lo);
}

export function assertRecoveryMarkers({ before, after, paused, target, expected, replay, point, postCommit }) {
  if (!markerPattern.test(expected) || target !== expected || paused !== 'paused' || before !== 1 || after !== 0 ||
      parseLSN(replay) < parseLSN(point) || parseLSN(replay) >= parseLSN(postCommit)) {
    throw new Error('named recovery target or committed marker mismatch');
  }
}

export function windowsRestoreCommand(archive) {
  if (!path.win32.isAbsolute(archive) || /["%&|<>^!\r\n]/.test(archive)) {
    throw new Error('invalid private archive directory');
  }
  return `copy /B /Y "${archive}\\%f" "%p" >NUL`;
}

function ownedDir(root, leaf) {
  if (!/^[a-z][a-z0-9-]{0,60}$/.test(leaf)) throw new Error('invalid private fixture path');
  const target = path.resolve(root, leaf);
  if (path.dirname(target) !== root) throw new Error('fixture path outside owned root');
  return target;
}
function configValue(value) {
  return "'" + value.replaceAll('\\', '\\\\').replaceAll("'", "''") + "'";
}
async function privateACL(folder) {
  const powershell = path.join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
  const script = `
$ErrorActionPreference='Stop'
$sid=[System.Security.Principal.WindowsIdentity]::GetCurrent().User
$acl=New-Object System.Security.AccessControl.DirectorySecurity
$acl.SetOwner($sid)
$acl.SetAccessRuleProtection($true,$false)
foreach($who in @($sid,(New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')))){
  $rule=New-Object System.Security.AccessControl.FileSystemAccessRule($who,'FullControl','ContainerInherit,ObjectInherit','None','Allow')
  $acl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $env:STUDIO_PITR_ACL_DIR -AclObject $acl
if(!(Get-Acl -LiteralPath $env:STUDIO_PITR_ACL_DIR).AreAccessRulesProtected){throw 'private ACL not installed'}
`;
  await runFile(powershell, ['-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64')], {
    windowsHide: true, timeout: 15000, env: { SystemRoot: process.env.SystemRoot, STUDIO_PITR_ACL_DIR: folder }
  });
}
async function sha256(file) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest('hex');
}
async function until(test, label, timeout = 60000) {
  const deadline = performance.now() + timeout;
  do {
    if (await test()) return;
    await delay(150);
  } while (performance.now() < deadline);
  throw new Error(label + ' timed out');
}
async function ephemeralPort(avoid) {
  const listener = net.createServer();
  await new Promise((resolve, reject) => {
    listener.on('error', reject);
    listener.listen({ port: 0, host: '127.0.0.1', exclusive: true }, resolve);
  });
  const { port } = listener.address();
  await new Promise((resolve, reject) => listener.close(err => err ? reject(err) : resolve()));
  return port === avoid ? ephemeralPort(avoid) : port;
}
async function assertNoLinks(dir) {
  for (const item of await fs.readdir(dir, { withFileTypes: true })) {
    if (item.isSymbolicLink()) throw new Error('link in private backup');
    if (item.isDirectory()) await assertNoLinks(path.join(dir, item.name));
    else if (!item.isFile()) throw new Error('unexpected file type in private backup');
  }
}
async function pidRecord(dir, port) {
  const [pid, data, , bound] = (await fs.readFile(path.join(dir, 'postmaster.pid'), 'utf8')).trim().split(/\r?\n/);
  if (!/^[1-9]\d*$/.test(pid) || path.resolve(data).toLowerCase() !== dir.toLowerCase() || Number(bound) !== port) {
    throw new Error('private PostgreSQL process identity mismatch');
  }
  return Number(pid);
}
function childEnv(bin, root, passfile) {
  const sys = process.env.SystemRoot;
  return { SystemRoot: sys, WINDIR: sys, ComSpec: path.join(sys, 'System32','cmd.exe'),
    PATH: [bin,path.join(sys,'System32'),sys].join(path.delimiter), USERPROFILE: os.homedir(),
    TEMP: root, TMP: root, LC_ALL:'C', PGCLIENTENCODING:'UTF8',
    PGPASSFILE: passfile, PGCONNECT_TIMEOUT:'5', PGSSLMODE:'disable', PGAPPNAME:'studio_pitr_fixture' };
}

export async function createPitrDrill({ binDir = 'C:\\Program Files\\PostgreSQL\\16\\bin', stageParent = os.tmpdir(), synchronousStandby = false } = {}) {
  if (process.platform !== 'win32') throw new Error('this drill targets Windows and PostgreSQL 16');
  const bin = await fs.realpath(binDir);
  const parent = await fs.realpath(stageParent);
  if (!/^[A-Za-z]:[A-Za-z0-9_ .\\-]+$/.test(parent)) throw new Error('choose an existing local ASCII staging parent');
  for (const name of ['postgres', 'pg_ctl', 'initdb', 'psql', 'pg_basebackup', 'pg_verifybackup', 'pg_waldump']) {
    if (!(await fs.stat(path.join(bin, name+'.exe'))).isFile()) throw new Error('missing PostgreSQL tool: '+name);
  }
  await fs.access(path.join(bin,'..','lib','basic_archive.dll'));
  const root = await fs.mkdtemp(path.join(parent, 'studio-pitr-'));
  await privateACL(root);
  const name = 'v18a_'+randomBytes(12).toString('hex');
  const drillId = 'pitr_'+randomBytes(16).toString('hex');
  const user = 'drill_'+randomBytes(8).toString('hex');
  const secret = randomBytes(32).toString('hex');
  const dirs = { source:ownedDir(root,'source'), detached:ownedDir(root,'offline-source'), liveWal:ownedDir(root,'live-wal'), detachedWal:ownedDir(root,'offline-live-wal'),
    base:ownedDir(root,'base'), archive:ownedDir(root,'archive'), restored:ownedDir(root,'restored') };
  const passfile = path.join(root,'pgpass.conf');
  const initfile = path.join(root,'initial-password');
  const env = childEnv(bin,root,passfile);
  await fs.writeFile(passfile,`127.0.0.1:*:*:${user}:${secret}\n`,{flag:'wx',mode:0o600});
  const owned = {source:null,restored:null};
  let state = 'new', busy = false, sourcePort, restoredPort;
  const facts = { server_major:16, drill_id:drillId, recovery_target:name, database:'studio', archive_method:'basic_archive',
    base_method:'pg_basebackup/plain/stream', evidence_scope:'local Windows, isolated loopback cluster',
    durations_ms:{} };
  async function step(expect, next, fn) {
    if (busy || state!==expect) throw new Error(`expected idle phase ${expect}; current phase ${state}`);
    busy=true;
    const start=performance.now();
    try { await fn(); state=next; facts.durations_ms[next]=Math.round(performance.now()-start); }
    finally {busy=false;}
  }
  async function tool(exe,args, label,timeout=90000) {
    try {
      const {stdout}=await runFile(path.join(bin,exe+'.exe'),args,{cwd:root,env,windowsHide:true,timeout,maxBuffer:1024*1024});
      return stdout.trim();
    } catch(error) {
      // Error objects can include command arguments and connection details.
      const exit=Number.isInteger(error.code)?error.code:'unavailable';
      const diagnostic = String(error.stderr || error.stdout || '').slice(0, 8192).replaceAll(secret,'[redacted]');
      await fs.appendFile(path.join(root,'tools.log'),`[${label}] ${diagnostic}\n`,{mode:0o600});
      throw new Error(`${label} failed (exit ${exit}); inspect the private fixture logs`);
    }
  }
  async function sql(which, query, database='postgres') {
    const port = which==='source'?sourcePort:restoredPort;
    if (!owned[which] || await pidRecord(dirs[which],port)!==owned[which]) throw new Error('owned listener is unavailable');
    return tool('psql',['-X','-w','-qAt','-v','ON_ERROR_STOP=1','-h','127.0.0.1','-p',String(port),'-U',user,'-d',database,'-c',query],which+' SQL',20000);
  }
  async function assertIdentity(which) {
    const id = await sql(which,'SELECT id FROM public.pitr_drill_identity ORDER BY id','studio');
    if(id!==drillId)throw new Error('platform database identity differs from this drill');
  }
  async function start(which, port) {
    const dir=dirs[which];
    const log=path.join(root,which+'.log');
    // Windows pg_ctl launches postgres through a persistent cmd.exe which
    // inherits stdio. An execFile callback may wait for that shell's pipes
    // even after pg_ctl exits successfully. Send diagnostics to a real file.
    const controlLog=await fs.open(path.join(root,which+'-pg-ctl.log'),'wx',0o600);
    try {
      const child=spawn(path.join(bin,'pg_ctl.exe'),['start','-D',dir,'-l',log,'-w','-t','60'],{
        cwd:root,env,windowsHide:true,timeout:70000,stdio:['ignore',controlLog.fd,controlLog.fd]
      });
      const [code]=await once(child,'exit');
      if(code!==0)throw new Error(which+' start failed; inspect the private pg_ctl log');
    } finally {
      await controlLog.close();
      try {owned[which]=await pidRecord(dir,port);} catch { /* startup may have failed before creating a PID */ }
    }
    if (!owned[which]) throw new Error(which+' startup did not establish fixture ownership');
  }
  async function halt(which) {
    if (!owned[which]) return;
    const dir=dirs[which],port=which==='source'?sourcePort:restoredPort;
    let observed;
    try { observed=await pidRecord(dir,port); }
    catch (error) {
      if (error.code!=='ENOENT')throw error;
      // On a failed recovery, pg_ctl may have already returned from standby
      // startup before postgres terminates and removes its own PID file.
      await until(() => {
        try { process.kill(owned[which],0); return false; }
        catch (e) { if(e.code==='ESRCH')return true; throw e; }
      },'confirm exit after PID file disappeared',3000);
      owned[which]=null;
      return;
    }
    if (observed!==owned[which]) throw new Error('refusing to stop an unowned process');
    await tool('pg_ctl',['stop','-D',dir,'-m','fast','-w','-t','60'],which+' stop',70000);
    try {await fs.access(path.join(dir,'postmaster.pid'));throw new Error('fixture process still owns data directory');}
    catch(e){if(e.code!=='ENOENT')throw e;}
    owned[which]=null;
  }
  async function clusterAccess(which, fn) {
    const port=which==='source'?sourcePort:restoredPort;
    if(await pidRecord(dirs[which],port)!==owned[which])throw new Error('owned listener changed');
    await assertIdentity(which);
    return fn({ databaseURL:`postgresql://${user}:${secret}@127.0.0.1:${port}/studio?sslmode=disable`, drillId });
  }
  async function withCluster(which, fn) {
    if(typeof fn!=='function' || busy) throw new Error('provide a callback between drill phases');
    const valid=which==='source'?['source_ready','base_ready','point_archived','sync_ready']:['restored_ready','sync_promoted'];
    if(!valid.includes(state))throw new Error(which+' fixture unavailable in current phase');
    busy=true;
    try{return await clusterAccess(which,fn);}
    finally{busy=false;}
  }

  // The streaming-replica branch reuses this newly owned pair of directories,
  // process handles, ports, ACL, credentials and backup. It cannot accept a DSN.
  let standby = {};
  if (synchronousStandby) {
    const { createStandbyBranch } = await import('./pg-sync-standby.mjs');
    standby = createStandbyBranch({
      step, sql, tool, start, halt, assertIdentity, assertNoLinks, configValue, sha256, until, dirs, facts, user, passfile,
      sourcePort: () => sourcePort,
      sourcePID: () => owned.source,
      allocateRestoredPort: async () => (restoredPort = await ephemeralPort(sourcePort)),
    });
  }
  return {
    ...standby,
    withSource:fn=>withCluster('source',fn), withRestored:fn=>withCluster('restored',fn),
    evidence:()=>structuredClone({...facts,phase:state}),
    // This is a private OS staging path for the caller's companion backup.
    // Public reports should use evidence(), which contains no paths or credentials.
    privateRoot:root,
    startSource:()=>step('new','source_ready',async()=>{
      facts.server_version=await tool('postgres',['--version'],'verify server version');
      if(!/^postgres \(PostgreSQL\) 16\./.test(facts.server_version))throw new Error('PostgreSQL 16 required');
      await fs.mkdir(dirs.liveWal);
      await fs.writeFile(initfile,secret+'\n',{flag:'wx',mode:0o600});
      try{await tool('initdb',['-D',dirs.source,'-U',user,'--encoding=UTF8','--locale=C','--data-checksums','--auth=scram-sha-256','--pwfile='+initfile],'initdb');}
      finally{await fs.unlink(initfile);}
      if((await fs.readFile(path.join(dirs.source,'PG_VERSION'),'utf8')).trim()!=='16')throw new Error('unexpected fixture format');
      sourcePort=await ephemeralPort();
      await fs.appendFile(path.join(dirs.source,'postgresql.conf'),`\nlisten_addresses='127.0.0.1'\nport=${sourcePort}\nwal_level=replica\nmax_wal_senders=4\narchive_mode=on\narchive_library='basic_archive'\nbasic_archive.archive_directory=${configValue(dirs.liveWal)}\nlogging_collector=off\n`);
      await start('source',sourcePort);
      facts.system_identifier=await sql('source','SELECT system_identifier::text FROM pg_control_system()');
      await sql('source','CREATE DATABASE studio');
      await sql('source',`CREATE TABLE public.pitr_drill_identity(id text PRIMARY KEY); INSERT INTO public.pitr_drill_identity VALUES('${drillId}')`,'studio');
      await assertIdentity('source');
      await sql('source',"CREATE TABLE pitr_drill_marker(phase text PRIMARY KEY); INSERT INTO pitr_drill_marker VALUES('baseline')");
    }),
    takeBaseBackup:()=>step('source_ready','base_ready',async()=>{
      if(await sql('source',"SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')")!=='0')throw new Error('fixture does not support external tablespaces');
      await tool('pg_basebackup',['-D',dirs.base,'-h','127.0.0.1','-p',String(sourcePort),'-U',user,'-w','--format=plain','--wal-method=stream','--checkpoint=fast','--manifest-checksums=SHA256'],'physical base backup',300000);
      await tool('pg_verifybackup',[dirs.base],'verify base backup and included WAL',180000);
      await assertNoLinks(dirs.base);
      facts.base_manifest_sha256=await sha256(path.join(dirs.base,'backup_manifest'));
      await sql('source',"INSERT INTO pitr_drill_marker VALUES('after_base')");
      await sql('source','SELECT pg_switch_wal()');
    }),
    createRestorePoint:({afterPoint}={})=>step('base_ready','point_archived',async()=>{
      if(afterPoint!==undefined && typeof afterPoint!=='function')throw new Error('afterPoint must be a callback');
      // Separate client commands commit the two transactions on different sides
      // of the named WAL record, including the deliberately excluded transaction.
      await sql('source',"BEGIN; INSERT INTO pitr_drill_marker VALUES('before'); COMMIT");
      // The observations are sampled on the source database clock. The
      // deliberately skipped interval is not a continuous-availability RPO.
      const point = JSON.parse(await sql('source',`WITH mark AS MATERIALIZED (SELECT pg_create_restore_point('${name}')::text AS lsn)
        SELECT json_build_object('lsn',lsn,'at',clock_timestamp()) FROM mark`));
      facts.point_lsn=point.lsn;
      facts.point_observed_at=point.at;
      parseLSN(facts.point_lsn);
      if(afterPoint)await clusterAccess('source',afterPoint);
      await sql('source',"BEGIN; INSERT INTO pitr_drill_marker VALUES('after'); COMMIT");
      const post = JSON.parse(await sql('source',"SELECT json_build_object('lsn',pg_current_wal_flush_lsn()::text,'at',clock_timestamp())"));
      facts.post_commit_lsn=post.lsn;
      facts.post_commit_observed_at=post.at;
      if(parseLSN(facts.post_commit_lsn)<=parseLSN(facts.point_lsn))throw new Error('post-target commit did not advance WAL');
      facts.point_to_post_flush_wal_bytes=(parseLSN(post.lsn)-parseLSN(point.lsn)).toString();
      const segments=[];
      for(const lsn of [facts.point_lsn,facts.post_commit_lsn]){
        const seg=await sql('source',`SELECT pg_walfile_name('${lsn}'::pg_lsn)`);
        if(!segPattern.test(seg))throw new Error('invalid WAL segment from source');
        if(!segments.includes(seg))segments.push(seg);
      }
      const baseWal = await fs.readdir(path.join(dirs.base,'pg_wal'));
      if(baseWal.includes(segments[0]))throw new Error('target WAL already present in base backup');
      facts.target_segment_absent_from_base=true;
      await sql('source','SELECT pg_switch_wal()');
      await until(async()=>{for(const seg of segments){try{await fs.access(path.join(dirs.liveWal,seg));}catch(e){if(e.code==='ENOENT')return false;throw e;}}return true;},'archive complete target and post-target WAL');
      await fs.mkdir(dirs.archive);
      facts.archive_segments=[];
      for(const entry of (await fs.readdir(dirs.liveWal)).sort()){
        if(!segPattern.test(entry) && !/^[0-9A-F]{8}\.history$/.test(entry)) continue;
        const original=path.join(dirs.liveWal,entry),copy=path.join(dirs.archive,entry);
        await fs.copyFile(original,copy,fs.constants.COPYFILE_EXCL);
        const digest=await sha256(copy);
        if(digest!==await sha256(original))throw new Error('archived WAL copy mismatch');
        facts.archive_segments.push({file:entry,bytes:(await fs.stat(copy)).size,sha256:digest});
      }
      facts.target_and_post_segments=segments;
      facts.source_before_and_after=await sql('source',"SELECT string_agg(phase,',' ORDER BY phase) FROM pitr_drill_marker");
      if(facts.source_before_and_after!=='after,after_base,baseline,before')throw new Error('source markers incomplete');
    }),
    stopSource:()=>step('point_archived','source_detached',async()=>{
      await halt('source');
      for(const [original,destination] of [[dirs.source,dirs.detached],[dirs.liveWal,dirs.detachedWal]]){
        if(path.resolve(await fs.realpath(original)).toLowerCase()!==original.toLowerCase() || path.dirname(original)!==root || path.dirname(destination)!==root)throw new Error('source not contained by private fixture');
        await fs.rename(original,destination);
      }
      facts.source_detached=true;
      facts.source_archive_detached=true;
    }),
    restore:()=>step('source_detached','restored_ready',async()=>{
      if(await sha256(path.join(dirs.base,'backup_manifest'))!==facts.base_manifest_sha256)throw new Error('base manifest changed');
      for(const row of facts.archive_segments)if(await sha256(path.join(dirs.archive,row.file))!==row.sha256)throw new Error('archived WAL changed');
      await tool('pg_verifybackup',[dirs.base],'verify independent base before restore',180000);
      await assertNoLinks(dirs.base);
      await fs.cp(dirs.base,dirs.restored,{recursive:true,force:false,errorOnExist:true});
      restoredPort=await ephemeralPort(sourcePort);
      const restoreCommand=windowsRestoreCommand(dirs.archive);
      await fs.appendFile(path.join(dirs.restored,'postgresql.conf'),`\nlisten_addresses='127.0.0.1'\nport=${restoredPort}\narchive_mode=off\narchive_library=''\nprimary_conninfo=''\nprimary_slot_name=''\nrestore_command=${configValue(restoreCommand)}\nrecovery_target_name=${configValue(name)}\nrecovery_target_action=pause\nhot_standby=on\n`);
      await fs.writeFile(path.join(dirs.restored,'recovery.signal'),'',{flag:'wx'});
      await start('restored',restoredPort);
      await until(async()=>(await sql('restored','SELECT pg_get_wal_replay_pause_state()'))==='paused','reach named recovery point');
      const proof=JSON.parse(await sql('restored',`SELECT json_build_object('before',count(*) FILTER(WHERE phase='before'),'after',count(*) FILTER(WHERE phase='after'),
        'paused',pg_get_wal_replay_pause_state(),'target',current_setting('recovery_target_name'),'replay',pg_last_wal_replay_lsn()::text,
        'identifier',(SELECT system_identifier::text FROM pg_control_system()),'in_recovery',pg_is_in_recovery()) FROM pitr_drill_marker`));
      assertRecoveryMarkers({...proof,expected:name,point:facts.point_lsn,postCommit:facts.post_commit_lsn});
      if(!proof.in_recovery||proof.identifier!==facts.system_identifier)throw new Error('physical recovery identity mismatch');
      await assertIdentity('restored');
      facts.paused_replay_lsn=proof.replay;
      facts.restored_before=proof.before;
      facts.restored_after=proof.after;
      await sql('restored','SELECT pg_wal_replay_resume()');
      await until(async()=>await sql('restored','SELECT NOT pg_is_in_recovery()')==='t','complete named-target recovery');
      if(await sql('restored',"SELECT string_agg(phase,',' ORDER BY phase) FROM pitr_drill_marker")!=='after_base,baseline,before')throw new Error('promotion changed recovered marker set');
      facts.promoted=true;
    }),
    stopRestored:()=>step('restored_ready','stopped',async()=>{await halt('restored');await fs.unlink(passfile);}),
    async close(){
      if(busy)throw new Error('wait for active drill phase before shutdown');
      busy=true;
      try{
        const errors=[];
        for(const which of synchronousStandby ? ['source','restored'] : ['restored','source']){
          try { await halt(which); } catch(e) { errors.push(e); }
        }
        try{await fs.unlink(passfile);}catch(e){if(e.code!=='ENOENT')errors.push(e);}
        if(errors.length)throw new AggregateError(errors,'private drill cleanup did not complete');
        if(state!=='stopped')state='closed';
      }finally{busy=false;}
    }
  };
}

if(process.argv[1] && import.meta.url===pathToFileURL(path.resolve(process.argv[1])).href){
  if(process.argv[2]!=='--run' || process.argv.length!==3){
    console.error('Use --run for a fresh local PostgreSQL 16 drill, or import createPitrDrill for staged integration.');process.exitCode=2;
  }else{
    let drill;
    try{
      drill=await createPitrDrill();
      await drill.startSource();await drill.takeBaseBackup();await drill.createRestorePoint();await drill.stopSource();
      await drill.restore();await drill.stopRestored();
      console.log(JSON.stringify(drill.evidence(),null,2));
    }catch(error){console.error(error.message);process.exitCode=1;}
    finally{if(drill)await drill.close();}
  }
}
