export function observedRanks(values) {
  if (!values.length || values.some(v => !Number.isFinite(v) || v < 0)) throw Error('invalid sample');
  const sorted = [...values].sort((a,b) => a-b);
  return { p50: sorted[Math.ceil(sorted.length * .5) - 1], p95: sorted[Math.ceil(sorted.length * .95) - 1] };
}

export function calibrationResult(raw) {
  const rtt = observedRanks(raw.rtt_ms).p50;
  // Each exchange includes a request/response latency. Exclude the observed
  // same-origin small-request median once, from both directions equally.
  const rate = samples => samples.map(ms => ms > rtt ? raw.bytes * 8 / (ms-rtt) / 1000 : 0);
  const down = rate(raw.down_ms), up = rate(raw.up_ms);
  const downMedian = observedRanks(down).p50, upMedian = observedRanks(up).p50;
  return { ...raw, median_rtt_ms: rtt, download_mbps: down, upload_mbps: up,
    passed: rtt >= 56 && rtt <= 112 && downMedian >= 16 && downMedian <= 24 && upMedian >= 16 && upMedian <= 24 };
}

export function matchScriptTransfers(entries, headers, receipts) {
  const ids = new Set();
  const byID = new Map(receipts.map(r => [r.id,r]));
  const phases = {};
  for (const phase of ['startup','history']) {
    const groups = {}, scripts = [], wire = [];
    let valid = true;
    for (const e of entries[phase]) {
      const row = groups[e.kind] ||= { requests:0, encoded_body_bytes:0, decoded_body_bytes:0, transfer_bytes:0, cache_hits:0 };
      row.requests++;
      row.encoded_body_bytes += e.encoded;
      row.decoded_body_bytes += e.decoded;
      row.transfer_bytes += e.transfer;
      const hit = e.transfer === 0 && e.decoded > 0;
      if (hit) row.cache_hits++;
      if (e.kind !== 'js') continue;
      const h = headers.get(e.receipt) || (hit ? headers.get(e.name) : null);
      const actual = byID.get(e.receipt);
      scripts.push({file:e.name,receipt:e.receipt || null,cache_hit:hit,resource_transfer_bytes:e.transfer,
        encoded_body_bytes:e.encoded,decoded_body_bytes:e.decoded,status:h?.status??null,encoding:h?.encoding||'',
        vary_encoding:!!h?.vary,declared_bytes:h?.length??null});
      if (!h || h.file !== e.name || h.status !== 200 || h.encoding !== 'gzip' ||
          !h.vary || h.length !== e.encoded || e.encoded <= 0 || e.decoded <= 0) valid = false;
      if (hit) continue;
      if (!actual || ids.has(e.receipt)) {valid=false;continue;}
      ids.add(e.receipt);
      wire.push(actual);
      if (actual.file !== e.name || actual.invalid || !actual.gzip || actual.status !== 200 ||
          actual.declared !== e.encoded || actual.body_bytes !== e.encoded) valid=false;
    }
    const clientBytes = scripts.reduce((sum,r)=>sum+(r.cache_hit?0:r.encoded_body_bytes),0);
    const serverBytes = wire.reduce((sum,r)=>sum+r.body_bytes,0);
    phases[phase]={groups,scripts,server_js_responses:wire,wire_js_body_bytes:serverBytes,browser_js_body_bytes:clientBytes,
      valid:valid && clientBytes===serverBytes && (phase!=='startup'||scripts.length>0)};
  }
  const browserBytes = phases.startup.browser_js_body_bytes + phases.history.browser_js_body_bytes;
  const actualBytes = receipts.reduce((n,r)=>n+r.body_bytes,0);
  phases.total = { actual_js_body_bytes:actualBytes, browser_js_body_bytes:browserBytes,
    observed_requests:receipts.length, matched_requests:ids.size, unmatched_receipts:receipts.filter(r=>!ids.has(r.id)),
    valid:phases.startup.valid && phases.history.valid && ids.size===receipts.length && actualBytes===browserBytes };
  return phases;
}
