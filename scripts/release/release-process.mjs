import { createHash, randomBytes } from 'node:crypto';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { createInterface } from 'node:readline';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const run = promisify(execFile);
const supervisor = fileURLToPath(new URL('./release-child.py', import.meta.url));
const fixturePNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGMQWnIYiBggFAAmhgXlAUvQ9QAAAABJRU5ErkJggg==','base64');
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const key = () => randomBytes(24).toString('hex');
const trustedRoles = Object.freeze({previous:'baseline',candidate:'candidate'});
function check(value, message) { if (!value) throw new Error(message); }

export function checkReleaseFixture(databaseURL, drillId) {
  // Accept only the URI shape produced by createPitrDrill.withSource.
  check(/^pitr_[0-9a-f]{32}$/.test(drillId) &&
    /^postgresql:\/\/drill_[0-9a-f]{16}:[0-9a-f]{64}@127\.0\.0\.1:\d{1,5}\/studio\?sslmode=disable$/.test(databaseURL),
    'expected a dedicated PITR source callback');
  const port = Number(databaseURL.match(/127\.0\.0\.1:(\d+)\//)[1]);
  check(port >= 1 && port <= 65535, 'invalid source port');
}

export function assertPromotedReleaseBinding(source, next, drillId, nextID) {
  checkReleaseFixture(source,drillId);
  checkReleaseFixture(next,nextID);
  const original=new URL(source), promoted=new URL(next);
  check(nextID===drillId && promoted.port!==original.port && promoted.username===original.username &&
    promoted.password===original.password, 'promoted endpoint does not belong to the original fixture');
}

export function assertReleasePair(previous, candidate) {
  check(previous.identity.role === 'baseline' && candidate.identity.role === 'candidate' &&
    previous.artifactId !== candidate.artifactId && previous.binarySha256 !== candidate.binarySha256,
    'expected independently identified baseline and candidate');
  check(Number.isSafeInteger(previous.schemaVersion) && previous.schemaVersion > 0 &&
    previous.schemaVersion === candidate.schemaVersion && /^[0-9a-f]{64}$/.test(previous.schemaSha256) &&
    previous.schemaSha256 === candidate.schemaSha256, 'incompatible release migration profiles');
}

const osEnv = () => ({
  SystemRoot: process.env.SystemRoot, PATH:process.env.PATH, TEMP:process.env.TEMP, TMP:process.env.TMP,
});
export function releaseEnvironment({databaseURL,referenceDir,assetDir,mediaKey,webDir,port}) {
  return {...osEnv(), DATABASE_URL:databaseURL, REFERENCE_DIR:referenceDir, ASSET_DIR:assetDir,
    AUTH_MODE:'required', PROVIDER_MODE:'demo', BILLING_MODE:'disabled', MEDIA_DELIVERY_KEY:mediaKey,
    APP_WEB_DIR:webDir, APP_ADDR:'127.0.0.1:'+port};
}

async function unusedPort() {
  const reserve = net.createServer();
  reserve.listen({host:'127.0.0.1',port:0,exclusive:true});
  await once(reserve,'listening');
  const port = reserve.address().port;
  await new Promise((resolve,reject) => reserve.close(err => err ? reject(err) : resolve()));
  return port;
}

// The helper retains the original native process handle. Never select a process
// by port or PID to stop it. App credentials live only in the child environment.
export function launchReleaseChild({binary,digest,log,env}) {
  const helper = spawn('python',['-I','-B',supervisor,binary,digest,log],
    {windowsHide:true,env,stdio:['pipe','pipe','ignore']});
  const lines = createInterface({input:helper.stdout})[Symbol.asyncIterator]();
  const done = new Promise((resolve) => {
    helper.once('error',()=>resolve(-1));
    helper.once('exit',code=>resolve(code));
  });
  async function reply(timeout) {
    let timer;
    try {
      const next = await Promise.race([lines.next(), new Promise((_,reject) => {
        timer=setTimeout(()=>reject(new Error('private process supervisor timed out')), timeout);
      })]);
      check(!next.done,'private process supervisor closed');
      const result=JSON.parse(next.value);
      check(!result.error,'private process identity or graceful control failed');
      return result;
    } finally { clearTimeout(timer); }
  }
  helper.stdin.on('error',()=>{});
  return {
    async launched() { check((await reply(15000)).launched,'launch acknowledgement missing'); },
    async listening() { helper.stdin.write('listener\n'); return (await reply(12000)).listening === true; },
    async stop() {
      helper.stdin.end('stop\n');
      const result=await reply(50000);
      check(result.stopped && result.exitCode===0 && await done===0,'clean process exit not confirmed');
    },
    detach() { helper.stdin.end(); },
  };
}

async function request(origin, pathname, {method='GET',headers={},body}={}, wanted=200) {
  const url = new URL(pathname,origin);
  check(url.origin===origin && url.hostname==='127.0.0.1','probe must stay on its own loopback service');
  const response = await fetch(url,{method,headers,body,redirect:'error',signal:AbortSignal.timeout(15000)});
  const reader = response.body?.getReader();
  const chunks=[]; let size=0;
  if (reader) {
    for (;;) {
      const {done,value}=await reader.read();
      if (done) break;
      if ((size+=value.length) > 1024*1024) { await reader.cancel(); throw new Error('probe response exceeds limit'); }
      chunks.push(value);
    }
  }
  check((Array.isArray(wanted) ? wanted : [wanted]).includes(response.status),'unexpected private HTTP status '+response.status+' for '+pathname.split('?')[0]);
  return {body:Buffer.concat(chunks),headers:response.headers,status:response.status};
}

export { request as requestReleaseFixture };

function fixtureGraph(models) {
  const model = id => {
    const entry=models.find(m=>m.id===id && m.enabled);
    check(entry?.version,'required local catalog model is unavailable');
    return {model_id:id,model_version:entry.version};
  };
  return {name:'Release handoff fixture',template_id:'text-image-approval',template_version:'1',
    nodes:[{id:'script',kind:'text',title:'Scene',prompt:'Describe the scene',...model('studio-chat')},
      {id:'picture',kind:'image',title:'Image',prompt:'Illustrate',...model('studio-image'),parameters:{count:1,aspect_ratio:'1:1'}},
      {id:'pick',kind:'approval',title:'Approve'}],
    edges:[{from:'script',from_port:'text',to:'picture',to_port:'prompt'},
      {from:'picture',from_port:'images',to:'pick',to_port:'images'}]};
}

async function signIn(origin,credentials,register=false) {
  const response=await request(origin,'/api/v1/auth/'+(register?'register':'login'),{method:'POST',
    headers:{'Content-Type':'application/json'},body:JSON.stringify(register?{...credentials,name:'Local release fixture'}:credentials)},register?201:200);
  const cookie=response.headers.getSetCookie().find(x=>x.startsWith('studio_session='))?.split(';')[0];
  const identity=JSON.parse(response.body);
  check(cookie && identity.user?.id && identity.project?.id,'session identity was not returned');
  return {cookie,userId:identity.user.id,projectId:identity.project.id};
}

export async function createReleaseProcess({repositoryRoot,privateRoot,databaseURL,drillId,artifacts,synchronousStandby=false}) {
  check(process.platform==='win32','this process drill targets Windows');
  checkReleaseFixture(databaseURL,drillId);
  const root=await fs.realpath(privateRoot);
  check(path.resolve(privateRoot).toLowerCase()===root.toLowerCase() && /^studio-pitr-[A-Za-z0-9]+$/.test(path.basename(root)),
    'expected a private source fixture directory');
  const {verifyReleaseArtifact}=await import('./release-artifact.mjs');
  const pinned={};
  for (const role of synchronousStandby ? ['previous'] : Object.keys(trustedRoles)) {
    check(path.isAbsolute(artifacts[role]),'absolute release artifact directory required');
    pinned[role]=await verifyReleaseArtifact(artifacts[role],{role:trustedRoles[role]});
  }
  if (!synchronousStandby) assertReleasePair(pinned.previous,pinned.candidate);
  const work=await fs.mkdtemp(path.join(root,'release-handoff-'));
  const assetDir=path.join(work,'assets'), referenceDir=path.join(work,'references');
  await fs.mkdir(assetDir); await fs.mkdir(referenceDir);
  const lockProbe=path.join(work,'release-check.exe');
  const goEnv={...osEnv(),GOTOOLCHAIN:'local',GOENV:'off',GOWORK:'off',GOPROXY:'off',GOSUMDB:'off',CGO_ENABLED:'0'};
  for (const name of ['USERPROFILE','LOCALAPPDATA','APPDATA','GOCACHE','GOMODCACHE','GOPATH']) {
    if (process.env[name]) goEnv[name]=process.env[name];
  }
  try {
    await run('go',['test','-c','-tags=releasedrill','-o',lockProbe,'./internal/studio'],
      {cwd:repositoryRoot,windowsHide:true,timeout:150000,env:goEnv});
  } catch { throw new Error('isolated release verification binary could not be built'); }
  const mediaKey=randomBytes(32).toString('base64');
  const credentials={email:'release-'+key()+'@example.test',password:key()};
  let active,session,proof,databaseID='',hasStarted=false,clean=false,busy=false,closed=false,sequence=0;
  async function phase(fn) {
    check(!busy && !closed,'another phase is running or the runner closed');
    busy=true; try {return await fn();} finally {busy=false;}
  }
  let cluster = 'source';
  async function gate(name, targetURL = databaseURL, targetCluster = cluster) {
    let result;
    try {
      result=await run(lockProbe,['-test.run=^TestReleaseProcessOwnership$','-test.v','-test.timeout=25s'],
        {cwd:work,windowsHide:true,timeout:35000,env:{...osEnv(), DATABASE_URL:targetURL, RELEASE_CHECK_CLUSTER:targetCluster,
          RELEASE_DRILL_ID:drillId,RELEASE_FIXTURE:root,REFERENCE_DIR:referenceDir,ASSET_DIR:assetDir,
          RELEASE_CHECK_PHASE:name,RELEASE_STORAGE_ID:databaseID}});
    } catch { throw new Error('private release ownership check failed during '+name); }
    const facts=result.stdout.match(/RELEASE_LOCK_FACTS (\{[^\r\n]+\})/)?.[1];
    check(facts && /--- PASS: TestReleaseProcessOwnership\b/.test(result.stdout),'release ownership evidence missing');
    const parsed=JSON.parse(facts);
    check(parsed.drillId===drillId && parsed.schemaVersion===pinned.previous.schemaVersion,
      'lock observation from unexpected fixture or format');
    if (name!=='fresh') {
      check(parsed.schemaVersion===pinned.previous.schemaVersion && parsed.databaseID &&
        (!databaseID || parsed.databaseID===databaseID),'storage identity or schema mismatch');
      databaseID=parsed.databaseID;
    }
    return parsed;
  }
  async function start(role) {
    check(Object.hasOwn(pinned,role) && !active && (hasStarted?clean:role==='previous'),'previous clean handoff required');
    const artifact=await verifyReleaseArtifact(artifacts[role],{role:trustedRoles[role],sha256:pinned[role].manifestSha256});
    check(artifact.manifestSha256===pinned[role].manifestSha256,'release no longer matches its original pin');
    if (hasStarted) await gate('free'); else await gate('fresh');
    const port=await unusedPort(),origin='http://127.0.0.1:'+port;
    const child=launchReleaseChild({binary:artifact.binaryPath,digest:artifact.binarySha256,
      log:path.join(work,'server-'+(++sequence)+'.log'),
      env:releaseEnvironment({databaseURL,referenceDir,assetDir,mediaKey,webDir:artifact.distPath,port})});
    active={child,role,origin}; hasStarted=true; clean=false;
    await child.launched();
    const deadline=performance.now()+95000;
    while (!await child.listening()) {
      check(performance.now()<deadline,'owned loopback listener did not become ready');
      await delay(200);
    }
    const readiness=JSON.parse((await request(origin,'/readyz')).body);
    check(readiness.ready===true && readiness.state==='ready','owned process did not report ready');
    const identity=JSON.parse((await request(origin,'/release-identity.json')).body);
    check(Object.entries(artifact.identity).every(([key,val])=>identity[key]===val),'served identity differs from pinned manifest');
    const locks=await gate('held');
    return {role,origin,artifactID:artifact.artifactId,binarySHA256:artifact.binarySha256,locks};
  }
  async function stop() {
    check(active,'no owned process available to stop');
    await active.child.stop();
    active=null; clean=false;
    return {graceful:true,exitCode:0};
  }
  async function quiescent() {
    check(hasStarted && !active,'a successful owned process exit must precede quiescent verification');
    const facts=await gate('free'); clean=true; return facts;
  }
  function originalProbes() {
    const {imagePath,receiptPath,original,receiptKey}=proof;
    return {userId:session.userId,cookie:session.cookie,paths:[imagePath,receiptPath],
      headers:{[imagePath]:{'X-Project-ID':session.projectId},[receiptPath]:{'X-Project-ID':session.projectId,'Idempotency-Key':receiptKey}},
      expected:{
        [imagePath]:(status,bytes)=>check(status===200 && sha(bytes)===sha(fixturePNG),'original private image digest differs'),
        [receiptPath]:(status,bytes)=>{
          const receipt=JSON.parse(bytes);
          check(status===200 && receipt.id===original.id && receipt.version===original.version &&
            receipt.project_id===session.projectId && JSON.stringify(receipt.graph)===JSON.stringify(original.graph),
            'original definition receipt differs');
        },
      }};
  }
  async function readOriginals() {
    const p=originalProbes();
    for (const pathname of p.paths) {
      const r=await request(active.origin,pathname,{headers:{Cookie:p.cookie,...p.headers[pathname]}});
      p.expected[pathname](r.status,r.body);
    }
    return {originalUser:true,originalImageSHA256:sha(fixturePNG),originalReceipt:true,definitionVersion:proof.original.version};
  }
  return {
    start:role=>phase(()=>start(role)),stop:()=>phase(stop),assertQuiescent:()=>phase(quiescent),
    ...(synchronousStandby ? {
      bindPromotedFixture: ({databaseURL: next, drillId: nextID}) => phase(async () => {
        check(!active && hasStarted && clean && proof && cluster==='source' && nextID===drillId,
          'same fixture and verified source process exit required');
        assertPromotedReleaseBinding(databaseURL,next,drillId,nextID);
        await gate('free',next,'restored');
        databaseURL=next;
        cluster='restored';
      }),
    } : {}),
    seed:()=>phase(async()=>{
      check(!session && !proof && active?.role==='previous','seed once on the baseline');
      session=await signIn(active.origin,credentials,true);
      const headers={Cookie:session.cookie,'X-Project-ID':session.projectId};
      const models=JSON.parse((await request(active.origin,'/api/v1/bootstrap',{headers})).body).models;
      const form=new FormData(); form.append('file',new Blob([fixturePNG],{type:'image/png'}),'handoff.png');
      const image=JSON.parse((await request(active.origin,'/api/v1/references',{method:'POST',headers:{...headers,'Idempotency-Key':key()},body:form},201)).body);
      check(image.project_id===session.projectId && image.url==='/api/v1/references/'+image.id+'/content?project_id='+session.projectId,'unexpected original reference path');
      const definitions='/api/v1/projects/'+session.projectId+'/workflow-definitions',receiptKey=key();
      const original=JSON.parse((await request(active.origin,definitions,{method:'POST',headers:{...headers,'Content-Type':'application/json','Idempotency-Key':receiptKey},
        body:JSON.stringify({expected_version:0,graph:fixtureGraph(models)})},201)).body);
      check(original.id && original.version===1 && original.project_id===session.projectId,'original business resource missing');
      proof={original,receiptKey,imagePath:image.url,receiptPath:definitions+'/receipt'};
      return readOriginals();
    }),
    verify:()=>phase(async()=>{
      check(proof && active,'seed and an active owned process required');
      const current=await signIn(active.origin,credentials);
      check(current.userId===session.userId && current.projectId===session.projectId,'original credentials no longer resolve to original identity');
      session=current; return readOriginals();
    }),
    withProbe:fn=>phase(async()=>{check(proof,'seed required'); return fn(originalProbes());}),
    close:()=>phase(async()=>{
      if (active) { try {await stop();} catch(err) {active?.child.detach(); throw err;} }
      if (hasStarted && (!synchronousStandby || !clean)) await quiescent();
      closed=true;
    }),
  };
}
