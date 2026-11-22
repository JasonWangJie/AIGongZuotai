import { uiText } from './locale';
import { useState } from 'react';
import { api } from './api';
import { Icon } from './components';
import { modelCapability } from './modelCatalog';
import type { Bootstrap, CloudDraft, ParameterPreset } from './types';
import { workflowParameters, inputEdge, workflowKind, workflowModels } from './workflows';
import type { WorkflowGraph, WorkflowNode } from './workflows';

function DraftMapping({ project, nodes, map, report }: { project: string; nodes: WorkflowNode[]; map: (id: string, prompt: string) => void; report: (error: unknown) => void }) {
  const [draft, setDraft] = useState<CloudDraft | null>(null), [version, setVersion] = useState(''), [source, setSource] = useState(''), [target, setTarget] = useState('');
  const [busy, setBusy] = useState(false);
  async function load() {
    setBusy(true); setDraft(null); setSource('');
    try {
      const result = version ? await api.draftRevision(project, Number(version)) : await api.draft(project);
      if (result.project_id === project) { setDraft(result); setSource(result.nodes[0]?.id || ''); }
    } catch (error) { report(error); } finally { setBusy(false); }
  }
  const card = draft?.nodes.find(n => n.id === source);
  return <details className="wf-mapping"><summary>{uiText("从已保存的草稿映射文字")}</summary>
    <div className="wf-form-grid"><label>{uiText("草稿版本")}<input type="number" min="1" step="1" placeholder={uiText("最新版本")} value={version} onChange={e => { setVersion(e.target.value); setDraft(null); }}/></label>
    <button type="button" className="button secondary" disabled={busy || (!!version && (!Number.isSafeInteger(Number(version)) || Number(version) < 1))} onClick={load}>{uiText("读取草稿")}</button></div>
    {draft && (draft.nodes.length ? <><div className="wf-form-grid"><label>{uiText("草稿 v")}{draft.revision} {uiText("的卡片")}<select value={source} onChange={e => setSource(e.target.value)}>{draft.nodes.map(n => <option key={n.id} value={n.id}>{n.title || n.id}</option>)}</select></label>
    <label>{uiText("替换哪个节点的提示词")}<select value={target} onChange={e => setTarget(e.target.value)}><option value="">{uiText("选择节点")}</option>{nodes.filter(n => n.kind !== 'approval').map(n => <option value={n.id} key={n.id}>{n.title}</option>)}</select></label></div>
    <blockquote>{card?.note || uiText('这张卡片没有正文。')}</blockquote><button type="button" className="button secondary" disabled={!target || !card?.note} onClick={() => card && map(target, card.note)}>{uiText("将卡片正文写入节点")}</button></> : <p>{uiText("该版本没有文字卡片。")}</p>)}
  </details>;
}
export function WorkflowEditor({ b, graph, update, disabled, report }: { b: Bootstrap; graph: WorkflowGraph; update: (g: WorkflowGraph) => void; disabled: boolean; report: (error: unknown) => void }) {
  const [focus, setFocus] = useState<WorkflowNode['kind']>('text');
  const order = ['text', 'image', 'approval', 'video'];
  const ordered = [...graph.nodes].sort((a, b) => order.indexOf(a.kind) - order.indexOf(b.kind));
  const selected = graph.nodes.find(n => n.kind === focus)!;
  function patch(id: string, values: Partial<WorkflowNode>) { update({ ...graph, nodes: graph.nodes.map(n => n.id === id ? { ...n, ...values } : n) }); }
  const model = b.models.find(m => m.id === selected.model_id);
  const caps = model && modelCapability(b, model), presets: ParameterPreset[] = caps?.parameter_presets || [];
  const preset = presets.find(p => p.aspect_ratio === selected.parameters?.aspect_ratio && p.size === selected.parameters?.size && p.resolution === selected.parameters?.resolution && p.seconds === selected.parameters?.seconds);
  return <fieldset className="wf-editor" disabled={disabled}><legend className="wf-sr">{uiText("编辑工作流节点和连接")}</legend>
    <label>{uiText("流程名称")}<input value={graph.name} maxLength={80} onChange={e => update({ ...graph, name: e.target.value })}/></label>
    <div className="wf-track" aria-label={uiText("节点图")}>{ordered.map((n, i) => <button type="button" data-connected={graph.edges.some(e => e.from === n.id && e.to === ordered[i + 1]?.id)} aria-pressed={n.id === selected.id} key={n.id} onClick={() => setFocus(n.kind)}><Icon name={n.kind === 'approval' ? 'check' : n.kind === 'text' ? 'chat' : n.kind === 'video' ? 'video' : 'image'}/><span><small>{workflowKind[n.kind]}</small><strong>{n.title || uiText('未命名节点')}</strong></span></button>)}</div>
    <div className="wf-inspector"><label>{uiText("节点名称")}<input maxLength={80} value={selected.title} onChange={e => patch(selected.id, {title: e.target.value})}/></label>
    {selected.kind !== 'approval' && <><label>{uiText("节点提示词")}<textarea rows={3} value={selected.prompt || ''} onChange={e => patch(selected.id, {prompt: e.target.value})}/></label>
    <div className="wf-form-grid"><label>{uiText("生成模型")}<select value={(selected.model_id || '') + ':' + (selected.model_version || '')} onChange={e => {
      const next = b.models.find(m => m.id + ':' + m.version === e.target.value);
      if (next) patch(selected.id, { model_id: next.id, model_version: next.version, ...((selected.kind === 'image' || selected.kind === 'video') && { parameters: workflowParameters(b, next) }) });
    }}>{!workflowModels(b, selected.kind).some(m => m.id === selected.model_id && m.version === selected.model_version) && <option value={(selected.model_id || '') + ':' + (selected.model_version || '')}>{uiText("所选规格不可用")}</option>}{workflowModels(b, selected.kind).map(m => <option value={m.id + ':' + m.version} key={m.id}>{m.name}</option>)}</select></label>
    {(selected.kind === 'image' || selected.kind === 'video') && model && <><label>{selected.kind === 'video' ? uiText('视频规格与时长') : uiText('画面规格')}<select value={preset?.id || ''} onChange={e => patch(selected.id, { parameters: workflowParameters(b, model, presets.find(p => p.id === e.target.value), selected.parameters?.count) })}>{!preset && <option value="">{uiText("选择规格")}</option>}{presets.map(p => <option value={p.id} key={p.id}>{p.name}{p.seconds ? uiText(" · {0} 秒 · {1}", p.seconds, p.aspect_ratio) : ''}</option>)}</select></label>
    {selected.kind === 'image' && <label>{uiText("候选图片数量")}<select value={selected.parameters?.count || 1} onChange={e => patch(selected.id, { parameters: workflowParameters(b, model, preset, Number(e.target.value)) })}>{Array.from({length: caps?.max_count || 1}, (_, i) => <option key={i}>{i + 1}</option>)}</select></label>}</>}</div></>}
    {(selected.kind === 'video' ? ['prompt','first_frame'] : selected.kind === 'image' ? ['prompt'] : selected.kind === 'approval' ? ['images'] : []).map(port =>
      <label key={port}>{selected.kind === 'video' ? port === 'first_frame' ? uiText('首帧连接') : uiText('分镜文本连接') : uiText('输入连接')}<select value={graph.edges.find(e => e.to === selected.id && e.to_port === port)?.from || ''} onChange={e => {
        const edge = inputEdge(graph.nodes, selected, e.target.value, port);
        update({ ...graph, edges: [...graph.edges.filter(link => link.to !== selected.id || link.to_port !== port), ...(edge ? [edge] : [])] });
      }}><option value="">{uiText("断开连接")}</option>{graph.nodes.filter(n => inputEdge(graph.nodes, selected, n.id, port)).map(n => <option key={n.id} value={n.id}>{n.title} · {n.kind === 'text' ? uiText('输出文字') : n.kind === 'approval' ? uiText('已选图片') : uiText('候选图片')}</option>)}</select></label>)}</div>
    <DraftMapping project={b.project.id} nodes={graph.nodes} map={(id, prompt) => patch(id, {prompt})} report={report}/>
  </fieldset>;
}

