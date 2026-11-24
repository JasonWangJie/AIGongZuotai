import { uiText } from './locale';
import { isSourceOperation, sourcePresets, validSourceCapabilities, validSourceSelection, type SourceSelection } from './sourceVideo';
import { capability } from './capabilities';
import type { Bootstrap, Kind, Model, ModelCatalog, ParameterPreset, TaskInput, CreationOperation } from './types';

export type ModelSelection = { modelId: string; modelVersion?: string; presetId?: string; count: number };
export type CatalogEntry = { scope: string; catalog: ModelCatalog; etag?: string };
export class CatalogCache {
  private entry: CatalogEntry | null = null;
  seed(scope: string, catalog: ModelCatalog): void { if (this.entry?.scope !== scope || this.entry.catalog.version !== catalog.version) this.entry = { scope, catalog }; }
  get(scope: string): CatalogEntry | undefined { return this.entry?.scope === scope ? this.entry : undefined; }
  set(entry: CatalogEntry): void { this.entry = entry; }
  clear(): void { this.entry = null; }
}
export function catalogScope(ownerId: string, workspaceId: string): string { return JSON.stringify([ownerId, workspaceId]); }
export function validCatalog(value: unknown): value is ModelCatalog {
  const catalog = value as ModelCatalog | null;
  if (!catalog || typeof catalog.version !== 'string' || !catalog.version || !Array.isArray(catalog.models) || catalog.models.length < 1 || catalog.models.length > 32) return false;
  const ids = new Set<string>();
  return catalog.models.every(model => {
    if (!model || typeof model.id !== 'string' || !model.id || ids.has(model.id) || !['chat', 'image', 'video'].includes(model.kind) || typeof model.name !== 'string' || typeof model.description !== 'string' || typeof model.enabled !== 'boolean' || typeof model.version !== 'string' || !model.version) return false;
    ids.add(model.id); const cap = model.capabilities;
    if (!cap || typeof cap.cancel_supported !== 'boolean' || !Number.isInteger(cap.max_count) || cap.max_count < 1 || cap.max_count > (model.kind === 'image' ? 4 : 1) || !Number.isInteger(cap.max_prompt_bytes) || cap.max_prompt_bytes < 1 || cap.max_prompt_bytes > 16000 || !Array.isArray(cap.parameter_presets) || cap.parameter_presets.length > 32) return false;
    if (cap.source_video !== undefined && !validSourceCapabilities(model, cap.source_video)) return false;
    if (cap.video_first_frame_supported !== undefined && (typeof cap.video_first_frame_supported !== 'boolean' || (cap.video_first_frame_supported && model.kind !== 'video'))) return false;
    if (cap.image_edit_supported !== undefined && typeof cap.image_edit_supported !== 'boolean') return false;
    if (cap.max_reference_images !== undefined && (!Number.isInteger(cap.max_reference_images) || cap.max_reference_images < 0 || cap.max_reference_images > 4)) return false;
    if (cap.image_edit_supported && (model.kind !== 'image' || !cap.max_reference_images)) return false;
    if (model.kind === 'chat') return cap.parameter_presets.length === 0;
    if (!cap.parameter_presets.length) return false;
    const presets = new Set<string>();
    return cap.parameter_presets.every(preset => { if (!preset || typeof preset.id !== 'string' || !preset.id || presets.has(preset.id) || typeof preset.name !== 'string' || typeof preset.aspect_ratio !== 'string' || !preset.aspect_ratio || (preset.size !== undefined && typeof preset.size !== 'string') || (preset.resolution !== undefined && typeof preset.resolution !== 'string') || (model.kind === 'image' && preset.seconds !== undefined) || (model.kind === 'video' && (typeof preset.seconds !== 'number' || !Number.isInteger(preset.seconds) || preset.seconds < 1 || preset.seconds > 600))) return false; presets.add(preset.id); return true; });
  });
}
export function modelReason(reason?: string): string | undefined {
  if (!reason) return undefined;
  const known: Record<string, string> = { disabled_by_operator: uiText('该模型已由工作区管理员停用。'), provider_not_configured: uiText('该模型的服务尚未配置，请选择其他可用模型。') };
  return known[reason] || (/^[a-z][a-z0-9_]*$/.test(reason) ? uiText('该模型暂不可用，请选择其他可用模型。') : reason);
}
export function modelCapability(bootstrap: Bootstrap | null, model?: Model) {
  if (!model) return { enabled: false, cancel_supported: false, max_count: 1, max_prompt_bytes: 16000, parameter_presets: [] as ParameterPreset[], image_edit_supported: false, max_reference_images: 0, reason: uiText('当前模式没有可用模型。') };
  if (model.capabilities) return { ...model.capabilities, enabled: model.enabled === true, reason: modelReason(model.reason), max_count: Math.max(1, model.capabilities.max_count), image_edit_supported: model.capabilities.image_edit_supported === true, max_reference_images: model.capabilities.image_edit_supported ? model.capabilities.max_reference_images || 0 : 0 };
  const old = capability(bootstrap, model.kind);
  return { ...old, enabled: model.enabled ?? old.enabled, max_count: old.max_count || 1, max_prompt_bytes: 16000, image_edit_supported: false, max_reference_images: 0, parameter_presets: model.kind === 'chat' ? [] : (old.aspect_ratios || ['1:1']).map(value => ({ id: value, name: value, aspect_ratio: value })), reason: modelReason(model.reason || old.reason) };
}
export function resolveSelection(bootstrap: Bootstrap | null, kind: Kind, preference?: ModelSelection, operation: CreationOperation = 'generate') {
  const available = bootstrap?.models.filter(model => model.kind === kind && (!isSourceOperation(operation) || sourcePresets(model, operation).length > 0) && (operation !== 'edit' || kind === 'image' && modelCapability(bootstrap, model).image_edit_supported)) || [];
  const preferred = available.find(model => model.id === preference?.modelId);
  const model = preferred && modelCapability(bootstrap, preferred).enabled ? preferred : available.find(item => modelCapability(bootstrap, item).enabled) || preferred || available[0];
  const capabilities = modelCapability(bootstrap, model);
  const presets = isSourceOperation(operation) ? sourcePresets(model, operation) : capabilities.parameter_presets;
  const preset = (model?.id === preference?.modelId ? presets.find(item => item.id === preference?.presetId) : undefined) || presets[0];
  const count = kind === 'video' ? 1 : Math.max(1, Math.min(capabilities.max_count, Number.isInteger(preference?.count) ? preference!.count : 1));
  const selection: ModelSelection = { modelId: model?.id || '', modelVersion: model?.version, presetId: preset?.id, count };
  const adjusted = !!preference && (preference.modelId !== selection.modelId || preference.modelVersion !== selection.modelVersion || preference.presetId !== selection.presetId || preference.count !== selection.count);
  return { model, capabilities, presets, preset, count, selection, adjusted };
}
export function presetSummary(preset?: ParameterPreset): string { if (!preset) return uiText('文本生成'); return [preset.aspect_ratio, preset.size, preset.resolution, preset.seconds === undefined ? undefined : uiText("{0} 秒", preset.seconds)].filter(Boolean).join(' · '); }
export function promptBytes(prompt: string): number { return new TextEncoder().encode(prompt).byteLength; }
export function buildModelTaskInput(projectId: string, model: Model, prompt: string, preset: ParameterPreset, count: number, operation: CreationOperation = 'generate', referenceIds: string[] = [], source?: SourceSelection): TaskInput {
  if (model.kind === 'chat') throw new Error(uiText('文本模型不能创建媒体任务。'));
  if (operation === 'edit' && (model.kind !== 'image' || model.capabilities?.image_edit_supported !== true || referenceIds.length < 1 || referenceIds.length > (model.capabilities.max_reference_images || 0) || new Set(referenceIds).size !== referenceIds.length)) throw new Error(uiText('当前模型或参考图不符合图片编辑要求。'));
  if (isSourceOperation(operation) && !validSourceSelection(model, preset.id, operation, source)) throw new Error(uiText('请核对原视频修订、模型规格和延长方向。'));
  const parameters: TaskInput['parameters'] = { aspect_ratio: preset.aspect_ratio, count: model.kind === 'video' ? 1 : count };
  if (preset.size !== undefined) parameters.size = preset.size;
  if (preset.resolution !== undefined) parameters.resolution = preset.resolution;
  if (preset.seconds !== undefined) parameters.seconds = preset.seconds;
  return { project_id: projectId, kind: model.kind, model_id: model.id, ...(model.version ? { model_version: model.version } : {}), operation, inputs: { prompt, ...(isSourceOperation(operation) && source ? { source_video: { asset_id: source.ref.asset_id, asset_revision: source.ref.asset_revision }, ...(source.direction ? { extension_direction: source.direction } : {}) } : {}), ...(operation === 'edit' ? { reference_asset_ids: [...referenceIds] } : {}) }, parameters };
}
export function historicalModelName(resource: { model_id: string; model_name?: string }): string { return resource.model_name || resource.model_id; }
export function canCancelResource(resource: { kind?: Kind; provider_mode?: 'demo' | 'live' }, fallback: 'demo' | 'live'): boolean { return resource.kind === undefined || resource.kind === 'chat' || (resource.provider_mode || fallback) === 'demo'; }
