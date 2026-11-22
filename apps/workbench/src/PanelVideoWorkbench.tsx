import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { formatCredits } from './billing';
import { uiText } from './locale';
import { statusName } from './types';
import type { Bootstrap } from './types';
import { timelineApi } from './videoTimeline';
import { PanelVideoReplacement } from './PanelVideoReplacement';
import { definitelyRejected, forgetTicket, matchesAttempt, matchesReplacement, panelVideoApi, readTicket,
  saveTicket, selectableVideoModels, ticketSlot, validPreview, videoPresets, videoSpec } from './panelVideos';
import type { Attempt, PanelScope, Preview, Ticket } from './panelVideos';
import './panelVideos.css';

type Props = { owner: string; scope: PanelScope; title: string; bootstrap: Bootstrap; onExpired: () => void };

export function PanelVideoWorkbench({ owner, scope, title, bootstrap, onExpired }: Props) {
  const models = selectableVideoModels(bootstrap.models);
  const [modelID, setModelID] = useState(''), [presetID, setPresetID] = useState('');
  const model = models.find(m => m.id === modelID) || models[0], presets = videoPresets(model);
  const preset = presets.find(p => p.id === presetID) || presets[0];
  const spec = model && preset ? videoSpec(scope, model, preset) : null;
  const slot = ticketSlot(owner, scope);
  const [preview, setPreview] = useState<{ data: Preview; key: string } | null>(null);
  const [pending, setPending] = useState<Ticket | null>(null), [blocked, setBlocked] = useState(false);
  const [items, setItems] = useState<Attempt[]>([]), [before, setBefore] = useState(0);
  const [selected, setSelected] = useState(''), [busy, setBusy] = useState(false);
  const [message, setMessage] = useState(''), [error, setError] = useState('');
  const mounted = useRef(false), working = useRef(false), denied = useRef(false);
  const readRevision = useRef(0);
  const chosen = items.find(a => a.task_id === selected);
  const valid = !!preview && !!spec && validPreview(preview.data, spec, bootstrap);
  const locked = blocked || busy || !!pending;

  function issue(e: unknown) {
    if (!mounted.current) return;
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
      setItems([]); setPreview(null); setSelected(''); setBlocked(true); denied.current = true;
      if (e.status === 401) onExpired();
    }
    setMessage('');
    setError(e instanceof Error ? e.message : uiText('请求失败，请核对原记录。'));
  }
  useLayoutEffect(() => {
    mounted.current = true;
    try { setPending(readTicket(slot)); } catch (e) { setBlocked(true); issue(e); }
    return () => { mounted.current = false; };
  }, [slot]);
  async function run(action: () => Promise<void>) {
    if (working.current || blocked || denied.current) return;
    working.current = true; setBusy(true); setError(''); setMessage('');
    try { await action(); }
    catch (e) { issue(e); }
    finally { working.current = false; if (mounted.current) setBusy(false); }
  }
  function install(a: Attempt) {
    setItems(old => [a, ...old.filter(item => item.task_id !== a.task_id)].sort((x,y) => y.attempt - x.attempt));
    setSelected(a.task_id);
  }
  async function load(more = false) {
    const page = await panelVideoApi.list(scope, more ? before : undefined);
    if (!mounted.current) return;
    if (!page.items.every(a => matchesAttempt(a, scope))) throw Error(uiText('视频尝试与当前镜头不一致。'));
    setItems(old => more ? [...new Map([...old, ...page.items].map(a => [a.task_id, a])).values()] : page.items);
    setBefore(page.next_before_attempt || 0);
    setSelected(old => more || page.items.some(a => a.task_id === old) ? old : page.items[0]?.task_id || '');
  }
  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    // Manual reads and writes supersede this initial read.
    const revision = readRevision.current;
    void panelVideoApi.list(scope, undefined, controller.signal).then(page => {
      if (!active || revision !== readRevision.current || !mounted.current || denied.current) return;
      if (!page.items.every(a => matchesAttempt(a, scope))) throw Error(uiText('视频尝试与当前镜头不一致。'));
      setItems(page.items); setBefore(page.next_before_attempt || 0); setSelected(page.items[0]?.task_id || '');
    }).catch(e => { if (active && revision === readRevision.current) issue(e); });
    return () => { active = false; controller.abort(); };
  }, [slot]);
  async function quote() {
    if (!spec || locked) return;
    readRevision.current++; setPreview(null);
    await run(async () => {
      const key = crypto.randomUUID(), data = await panelVideoApi.quote(scope, key, spec);
      if (!mounted.current) return;
      if (!validPreview(data, spec, bootstrap)) throw Error(uiText('报价已过期或与当前项目、模型及规格不一致。'));
      setPreview({ key, data });
    });
  }
  async function writeOrCheck(ticket: Ticket, check: boolean) {
    if (busy || blocked || (!check && pending)) return;
    readRevision.current++;
    await run(async () => {
      if (!check) {
        saveTicket(slot, ticket);
        setPending(ticket);
      }
      try {
        if (ticket.kind === 'generate') {
          const a = check ? await panelVideoApi.receipt(scope, ticket.key) : await panelVideoApi.generate(scope, ticket);
          if (!mounted.current) return;
          if (!matchesAttempt(a, scope, ticket)) throw Error(uiText('原生成回执与提交不一致，请继续核对。'));
          install(a); setPreview(null);
          setMessage(check ? uiText('已核对生成回执') : uiText('视频任务已受理'));
        } else {
          const v = check ? await timelineApi.receipt(scope.project, ticket.key) : await panelVideoApi.replace(scope, ticket);
          if (!mounted.current) return;
          if (!matchesReplacement(v, scope, ticket)) throw Error(uiText('原替换回执与目标镜头不一致，请继续核对。'));
          setMessage(check ? uiText('已核对替换回执 · v{0}', v.version) : uiText('已保存剪辑版本 v{0}', v.version));
        }
        forgetTicket(slot, ticket.key); setPending(null);
      } catch (e) {
        if (!mounted.current) return;
        if (!check && definitelyRejected(e)) {
          forgetTicket(slot, ticket.key); setPending(null);
          if (ticket.kind === 'generate') setPreview(null);
        }
        throw e;
      }
    });
  }
  function generate() {
    if (!spec || !preview || !validPreview(preview.data, spec, bootstrap) || locked) return;
    void writeOrCheck({ kind: 'generate', key: preview.key, spec, expected_attempt: preview.data.attempt, quote_id: preview.data.quote?.id }, false);
  }
  return <section className="pv-work" aria-label={uiText('单镜视频')}>
    <header><h2>{uiText('单镜视频')}</h2><span>v{scope.version} · {title}</span></header>
    <p>{uiText('用此版本的故事与镜头文字生成视频。项目配图不会发送给视频模型。')}</p>
    <div className="pv-controls">
      <label>{uiText('视频模型')}<select value={model?.id || ''} disabled={locked} onChange={e => {
        setModelID(e.target.value); setPresetID(''); setPreview(null);
      }}>{!models.length && <option value="">{uiText('暂无可用视频模型')}</option>}
        {models.map(m => <option key={m.id} value={m.id}>{m.name} · {m.version}</option>)}</select></label>
      <label>{uiText('视频规格')}<select value={preset?.id || ''} disabled={locked} onChange={e => {
        setPresetID(e.target.value); setPreview(null);
      }}>{presets.map(p => <option key={p.id} value={p.id}>{p.name} · {p.aspect_ratio} · {p.seconds}s {[p.size,p.resolution].filter(Boolean).join(' · ')}</option>)}</select></label>
    </div>
    <button disabled={locked || !spec} onClick={() => void quote()}>{uiText('预览提示词与报价')}</button>
    {preview && <div className="pv-preview">
      <h3>{uiText('固定镜头提示词')}</h3><pre>{preview.data.prompt}</pre>
      <p>{preview.data.quote ? uiText('本次冻结 {0} CREDIT，有效至 {1}。', formatCredits(preview.data.quote.reserve_micros), new Date(preview.data.quote.expires_at).toLocaleTimeString()) : uiText('当前未启用权益计价。')}</p>
      <button className="cb-primary" disabled={locked || !valid} onClick={generate}>{uiText('确认生成此镜视频')}</button>
    </div>}
    {pending && !denied.current && <div className="pv-pending">
      <p>{uiText('原操作等待核对。查询回执只读取原提交记录。')}</p>
      <button disabled={busy || blocked} onClick={() => void writeOrCheck(pending, true)}>
        {pending.kind === 'generate' ? uiText('按原键核对生成') : uiText('按原键核对替换')}</button>
    </div>}
    <div className="pv-history">
      <label>{uiText('视频尝试')}<select value={chosen?.task_id || ''} disabled={locked} onChange={e => setSelected(e.target.value)}>
        <option value="">{uiText('选择本镜头的尝试')}</option>{items.map(a => <option key={a.task_id} value={a.task_id}>
          {uiText('第 {0} 次', a.attempt)} · {statusName(a.status)}
        </option>)}</select></label>
      <button disabled={busy || blocked} onClick={() => { readRevision.current++; void run(() => load()); }}>{uiText('刷新视频尝试')}</button>
      {before > 0 && <button disabled={busy || blocked} onClick={() => { readRevision.current++; void run(() => load(true)); }}>{uiText('更早尝试')}</button>}
    </div>
    {chosen && ['SUCCEEDED','PARTIAL'].includes(chosen.status) &&
      <PanelVideoReplacement key={chosen.task_id} scope={scope} attempt={chosen} disabled={locked} issue={issue} onReplace={ticket => void writeOrCheck(ticket, false)}/>}
    {message && <p role="status">{message}</p>}{error && <p role="alert">{error}</p>}
  </section>;
}
