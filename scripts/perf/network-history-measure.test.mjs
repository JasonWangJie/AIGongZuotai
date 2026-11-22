import assert from 'node:assert/strict';
import test from 'node:test';
import { observedRanks, calibrationResult, matchScriptTransfers } from '../../apps/workbench/scripts/network-history-measure.mjs';

test('independent upload, download and latency samples reveal a missing throttle', () => {
  const raw = {bytes:4194304, rtt_ms:[78,80,82], down_ms:[1760,1760,1760], up_ms:[1760,1760,1760]};
  assert.equal(calibrationResult(raw).passed,true);
  assert.equal(calibrationResult({...raw,up_ms:[85,85,85]}).passed,false);
  assert.equal(calibrationResult({...raw,rtt_ms:[1,2,3]}).passed,false);
  assert.equal(observedRanks(Array.from({length:20},(_,i)=>i+1)).p95,19);
});

test('an overlapping startup request joins its exact receipt after the click', () => {
  const entry=(name,id,encoded,hit=false)=>({kind:'js',name,receipt:id,encoded,decoded:encoded*2,transfer:hit?0:encoded+300});
  const receipt=(file,id,body_bytes)=>({file,id,body_bytes,status:200,gzip:true,declared:body_bytes,invalid:false});
  const first=entry('index-fixture.js',101,165212), late=entry('accountGovernance-fixture.js',102,891);
  const next=entry('chat-fixture.js',103,540);
  const headers=new Map([first,late,next].map(e=>[e.receipt,{file:e.name,status:200,encoding:'gzip',vary:true,length:e.encoded}]));
  // The oldest requested script completed last; array order cannot determine its phase.
  const wire=[next,late,first].map(e=>receipt(e.name,e.receipt,e.encoded));
  const match=(startup,history,receipts=wire)=>matchScriptTransfers({startup,history},headers,receipts);
  const trip=match([first,late],[next]);
  assert.equal(trip.total.valid,true);
  assert.equal(trip.startup.wire_js_body_bytes,166103);
  assert.equal(trip.history.wire_js_body_bytes,540);
  assert.equal(trip.total.actual_js_body_bytes,166643);
  assert.equal(match([first,late],[next],wire.slice(1)).total.valid,false);
  assert.equal(match([first],[next]).total.valid,false);
  assert.equal(match([first,late],[{...next,receipt:102}]).total.valid,false);
  assert.equal(match([first,late],[next],wire.map(r=>r.id===101?{...r,body_bytes:0}:r)).total.valid,false);
  const hot=match([{...first,transfer:0},{...late,transfer:0}],[{...next,transfer:0}],[]);
  assert.ok(hot.total.valid);
  assert.equal(hot.total.actual_js_body_bytes,0);
  assert.equal(hot.startup.scripts.filter(s=>s.cache_hit).length,2);
});
