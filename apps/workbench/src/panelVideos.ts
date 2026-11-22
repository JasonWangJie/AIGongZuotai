import { ApiError, request } from './api';
import type { BillingQuote } from './billing';
import { buildModelTaskInput } from './modelCatalog';
import { uiText } from './locale';
import type { Bootstrap, Model, ParameterPreset } from './types';
import { versionMatches, type TimelineVersion } from './videoTimeline';

export type PanelScope = { project: string; version: number; panel: string };
export type VideoSpec = {
  model_id: string; model_version: string;
  parameters: { count: number; seconds: number; aspect_ratio: string; resolution?: string; size?: string };
};
export type Attempt = VideoSpec & {
  project_id: string; comic_version: number; panel_id: string; attempt: number;
  task_id: string; status: string; quote_id?: string; created_at: string;
};
export type Preview = VideoSpec & { attempt: number; prompt: string; billing_enabled: boolean; quote?: BillingQuote };
export type GenerateTicket = { kind: 'generate'; key: string; expected_attempt: number; spec: VideoSpec; quote_id?: string };
export type ReplaceTicket = {
  kind: 'replace'; key: string; timeline: string; version: number; clip: string;
  task_id: string; asset_id: string; asset_revision: number; in_ms: number; out_ms: number;
};
export type Ticket = GenerateTicket | ReplaceTicket;
const enc = encodeURIComponent;
const path = (s: PanelScope) => '/projects/' + enc(s.project) + '/comic/versions/' + s.version + '/panels/' + enc(s.panel) + '/videos';
const headers = (s: PanelScope, key?: string) => ({ 'X-Project-ID': s.project, ...(key ? { 'Idempotency-Key': key } : {}) });
function post<T>(s: PanelScope, url: string, key: string, body: unknown) {
  return request<T>(url, { method: 'POST', headers: headers(s, key), body: JSON.stringify(body) });
}
export const panelVideoApi = {
  list: (s: PanelScope, before?: number, signal?: AbortSignal) => request<{ items: Attempt[]; next_before_attempt?: number }>(
    path(s) + '?limit=16' + (before ? '&before_attempt=' + before : ''), { headers: headers(s), signal }),
  quote: (s: PanelScope, key: string, spec: VideoSpec) => post<Preview>(s, path(s) + '/quote', key, spec),
  generate: (s: PanelScope, t: GenerateTicket) => post<Attempt>(s, path(s), t.key,
    { ...t.spec, expected_attempt: t.expected_attempt, ...(t.quote_id ? { quote_id: t.quote_id } : {}) }),
  receipt: (s: PanelScope, key: string) => request<Attempt>(path(s) + '/receipt', { headers: headers(s, key) }),
  replace: (s: PanelScope, t: ReplaceTicket) => post<TimelineVersion>(s,
    '/projects/' + enc(s.project) + '/video-timelines/' + enc(t.timeline) + '/versions/' + t.version +
      '/clips/' + enc(t.clip) + '/replace-from-panel', t.key, {
        comic_version: s.version, panel_id: s.panel, task_id: t.task_id, asset_id: t.asset_id,
        asset_revision: t.asset_revision, in_ms: t.in_ms, out_ms: t.out_ms,
      }),
};
const positive = (n: unknown): n is number => typeof n === 'number' && Number.isSafeInteger(n) && n > 0;
const id = (s: unknown): s is string => typeof s === 'string' && s.length > 0 && s.length <= 160;
export function selectableVideoModels(models: Model[]) {
  return models.filter(m => m.kind === 'video' && m.enabled && id(m.version) && videoPresets(m).length);
}
export function videoPresets(model?: Model) {
  return model?.capabilities?.parameter_presets.filter(p => id(p.id) && id(p.aspect_ratio) && positive(p.seconds)) || [];
}
export function videoSpec(s: PanelScope, model: Model, preset: ParameterPreset): VideoSpec {
  const { parameters } = buildModelTaskInput(s.project, model, '', preset, 1);
  return { model_id: model.id, model_version: model.version!, parameters: { ...parameters, seconds: preset.seconds! } };
}
function validSpec(s: VideoSpec) {
  return !!s && id(s.model_id) && id(s.model_version) && !!s.parameters && s.parameters.count === 1 &&
    positive(s.parameters.seconds) && id(s.parameters.aspect_ratio) &&
    (s.parameters.size === undefined || id(s.parameters.size)) && (s.parameters.resolution === undefined || id(s.parameters.resolution));
}
export function sameSpec(a: VideoSpec, b: VideoSpec) {
  return validSpec(a) && validSpec(b) && a.model_id === b.model_id && a.model_version === b.model_version &&
    a.parameters.seconds === b.parameters.seconds && a.parameters.aspect_ratio === b.parameters.aspect_ratio &&
    a.parameters.size === b.parameters.size && a.parameters.resolution === b.parameters.resolution;
}
export function validPreview(p: Preview, spec: VideoSpec, b: Bootstrap) {
  if (!sameSpec(p, spec) || !positive(p.attempt) || p.attempt > 32 || !p.prompt?.trim() ||
      p.billing_enabled !== (b.billing?.enabled === true)) return false;
  const q = p.quote;
  if (!p.billing_enabled) return !q;
  return !!q && id(q.id) && q.project_id === b.project.id && q.workspace_id === b.workspace.id &&
    q.model_id === p.model_id && q.model_version === p.model_version && q.provider_mode === b.mode &&
    q.resource_type === 'task' && q.unit === 'CREDIT' && q.scale === 6 &&
    /^\d{1,30}$/.test(q.reserve_micros) && Date.parse(q.expires_at) > Date.now();
}
export function matchesAttempt(a: Attempt, s: PanelScope, t?: GenerateTicket) {
  return validSpec(a) && a.project_id === s.project && a.comic_version === s.version && a.panel_id === s.panel &&
    id(a.task_id) && id(a.status) && positive(a.attempt) && a.attempt <= 32 &&
    (!t || (a.attempt === t.expected_attempt && sameSpec(a, t.spec) && (a.quote_id || '') === (t.quote_id || '')));
}
export function matchesReplacement(v: TimelineVersion, s: PanelScope, t: ReplaceTicket) {
  if (!versionMatches(v, s.project, t.timeline, t.version + 1)) return false;
  const c = v.document.clips.find(clip => clip.id === t.clip);
  return !!c && c.asset_id === t.asset_id && c.asset_revision === t.asset_revision && c.in_ms === t.in_ms && c.out_ms === t.out_ms;
}
export function ticketSlot(owner: string, s: PanelScope) {
  return 'xingtu-panel-video:' + JSON.stringify([owner, s.project, s.version, s.panel]);
}
export function readTicket(slot: string): Ticket | null {
  const raw = sessionStorage.getItem(slot);
  if (raw === null) return null;
  if (raw.length <= 4096) {
    const t = JSON.parse(raw) as Ticket;
    if (t && /^[\w-]{12,100}$/.test(t.key) && (
      (t.kind === 'generate' && validSpec(t.spec) && positive(t.expected_attempt) && t.expected_attempt <= 32) ||
      (t.kind === 'replace' && [t.asset_id,t.task_id,t.timeline,t.clip].every(id) && positive(t.asset_revision) &&
       positive(t.version) && Number.isSafeInteger(t.in_ms) && t.in_ms >= 0 && positive(t.out_ms) && t.out_ms > t.in_ms)
    )) return t;
  }
  throw new Error(uiText('本页原操作记录无法读取，已暂停新的提交。'));
}
export function saveTicket(slot: string, t: Ticket) {
  if (readTicket(slot)) throw new Error(uiText('请先核对原操作。'));
  sessionStorage.setItem(slot, JSON.stringify(t));
}
export function forgetTicket(slot: string, key: string) {
  if (readTicket(slot)?.key === key) sessionStorage.removeItem(slot);
}
export const definitelyRejected = (e: unknown) => e instanceof ApiError && !e.uncertain && e.status >= 400 && e.status < 500;
