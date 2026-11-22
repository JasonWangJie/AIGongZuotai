import test from 'node:test';
import assert from 'node:assert/strict';
import { randomBytes, createHash } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { assertReleasePair, checkReleaseFixture, launchReleaseChild, releaseEnvironment } from './release-process.mjs';

const authority='drill_'+ '2'.repeat(16)+':'+ '3'.repeat(64);
const url='postgresql://'+authority+'@127.0.0.1:39912/studio?sslmode=disable';
const id='pitr_'+'1'.repeat(32);
test('release scope accepts only the isolated PITR callback URI',()=>{
  checkReleaseFixture(url,id);
  for (const invalid of [url.replace('127.0.0.1','localhost'),url+'&options=unsafe',url.replace('/studio','/postgres'),
    url.replace(':39912',':0'),url.replace('drill_','user_')]) assert.throws(()=>checkReleaseFixture(invalid,id));
  assert.throws(()=>checkReleaseFixture(url,'other-drill'));
  const env=releaseEnvironment({databaseURL:url,port:8990});
  assert.equal(env.APP_ADDR,'127.0.0.1:8990');
  assert.deepEqual(Object.keys(env).sort(),['SystemRoot','PATH','TEMP','TMP','DATABASE_URL','REFERENCE_DIR','ASSET_DIR','AUTH_MODE',
    'PROVIDER_MODE','BILLING_MODE','MEDIA_DELIVERY_KEY','APP_WEB_DIR','APP_ADDR'].sort());
});

test('incompatible migrations or aliased builds cannot enter same-format handoff',()=>{
  const p={identity:{role:'baseline'},artifactId:'one',binarySha256:'a',schemaVersion:29,schemaSha256:'b'.repeat(64)};
  const c={...p,identity:{role:'candidate'},artifactId:'two',binarySha256:'c'};
  assertReleasePair(p,c);
  for (const change of [{schemaVersion:28},{schemaSha256:'d'.repeat(64)},{artifactId:'one'},{binarySha256:'a'}])
    assert.throws(()=>assertReleasePair(p,{...c,...change}));
});

test('Windows supervisor confirms its own listener and graceful exit, leaving another listener open',
  {skip:process.platform!=='win32',timeout:100000},async t=>{
    const root=await fs.mkdtemp(path.join(os.tmpdir(),'release-child-check-'));
    t.after(()=>fs.rm(root,{recursive:true,force:true}));
    const source=path.join(root,'main.go'),binary=path.join(root,'isolated.exe'),sentinel=randomBytes(12).toString('hex');
    await fs.writeFile(source,'package main\nimport("os";"net";"os/signal")\nfunc main(){c:=make(chan os.Signal,1);signal.Notify(c,os.Interrupt);l,e:=net.Listen("tcp",os.Getenv("APP_ADDR"));if e!=nil{os.Exit(4)};<-c;l.Close();if os.WriteFile("drained",[]byte("'+sentinel+'"),0600)!=nil{os.Exit(5)}}\n',{flag:'wx'});
    await promisify(execFile)('go',['build','-o',binary,source],{cwd:root,windowsHide:true,timeout:60000});
    const digest=createHash('sha256').update(await fs.readFile(binary)).digest('hex');
    const peer=net.createServer(s=>s.end('unrelated'));
    peer.listen(0,'127.0.0.1'); await once(peer,'listening');
    t.after(()=>new Promise(resolve=>peer.close(resolve)));
    const probe=net.createServer();probe.listen(0,'127.0.0.1');await once(probe,'listening');
    const port=probe.address().port;
    await new Promise(resolve=>probe.close(resolve));
    const child=launchReleaseChild({binary,digest,log:path.join(root,'local.log'),env:releaseEnvironment({port})});
    let stopped=false;
    try {
      await child.launched();
      const deadline=performance.now()+15000;
      while (!await child.listening()) {assert.ok(performance.now()<deadline); await delay(100);}
      await child.stop(); stopped=true;
      assert.equal(await fs.readFile(path.join(root,'drained'),'utf8'),sentinel);
      const socket=net.connect(peer.address().port,'127.0.0.1');
      assert.equal((await once(socket,'data'))[0].toString(),'unrelated');socket.destroy();
    } finally {if(!stopped) child.detach();}
  });
