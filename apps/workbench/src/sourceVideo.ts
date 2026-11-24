import { uiText } from './locale';
import type { AssetEntry } from './libraryState';
import type { Model, SourceVideoCapabilities, SourceVideoOperation, SourceVideoRef, Task, TaskInput } from './types';
import type { PendingOperation } from './pendingRequests';
import { usableVideo } from './videoTimeline';

export const isSourceOperation = (op: unknown): op is SourceVideoOperation => op === 'video_edit' || op === 'video_extend';
export const sourceOperationName = (op: SourceVideoOperation) => op === 'video_edit' ? uiText('编辑原视频') : uiText('延长原视频');
export const sourceDirectionName = (value: string) => value === 'forward' ? uiText('向后延长') :
  value === 'backward' ? uiText('向前延长') : value;
export type SourceSelection = { ref: SourceVideoRef; direction?: string };
export function sourcePresets(model: Model | undefined, op: SourceVideoOperation) {
  const cap = model?.capabilities?.source_video;
  const ids = op === 'video_edit' ? cap?.data_uri_edit_presets : cap?.data_uri_extend_presets;
  if (model?.kind !== 'video' || !model.version || !model.enabled || !cap || !validSourceCapabilities(model, cap) || !Array.isArray(ids)) return [];
  return model.capabilities!.parameter_presets.filter(p => ids.includes(p.id));
}
export function validSourceCapabilities(model: Model, cap: SourceVideoCapabilities) {
  if (!cap || typeof cap !== 'object' || model.kind !== 'video') return false;
  const { data_uri_edit_presets: edit = [], data_uri_extend_presets: extend = [], extension_directions: directions = [] } = cap;
  const validIDs = (values: unknown): values is string[] => Array.isArray(values) &&
    values.every(v => typeof v === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,63}$/.test(v)) && new Set(values).size === values.length;
  return validIDs(edit) && validIDs(extend) && validIDs(directions) && edit.length + extend.length > 0 &&
    directions.length <= 16 && directions.every(v => /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(v)) && (!directions.length || extend.length > 0) &&
    [...edit, ...extend].every(id => model.capabilities?.parameter_presets.some(p => p?.id === id));
}
// The library assigns these IDs and reserves a bounded revision history.
function validSourceRef(ref?: SourceVideoRef) {
  return !!ref && /^lib_[a-f0-9]{64}$/.test(ref.asset_id) && Number.isSafeInteger(ref.asset_revision) &&
    ref.asset_revision >= 1 && ref.asset_revision <= 1000;
}
export function eligibleSource(asset: AssetEntry, project: string) {
  return usableVideo(asset, project) && !!asset.task_id && !!asset.artifact_id && validSourceRef({ asset_id: asset.id, asset_revision: asset.revision });
}
export function validSourceSelection(model: Model, presetID: string, op: SourceVideoOperation, selected?: SourceSelection) {
  return !!selected && validSourceRef(selected.ref) &&
    sourcePresets(model, op).some(p => p.id === presetID) &&
    (!selected.direction || (op === 'video_extend' && model.capabilities?.source_video?.extension_directions?.includes(selected.direction)));
}
export const sourcePending = (pending?: PendingOperation | null) => pending?.kind === 'task' && isSourceOperation(pending.body.operation);
export const sourceUnknownText = '原视频任务的受理状态待确认。请查询原任务收据。';
export function matchesSourceTask(task: Task | null, body: TaskInput, workspace: string) {
  const ref = body.inputs.source_video, params = body.parameters;
  return !!task?.id && typeof task.status === 'string' && task.status.length > 0 && Number.isSafeInteger(task.version) && task.version > 0 && task.workspace_id === workspace && task.project_id === body.project_id && task.kind === 'video' &&
    task.model_id === body.model_id && task.model_version === body.model_version && task.operation === body.operation &&
    task.prompt === body.inputs.prompt && (task.billing?.quote_id || '') === (body.quote_id || '') &&
    !!ref && task.source_video?.asset_id === ref.asset_id && task.source_video.asset_revision === ref.asset_revision &&
    (task.extension_direction || '') === (body.inputs.extension_direction || '') && !!task.parameters &&
    (['aspect_ratio', 'count', 'seconds', 'size', 'resolution'] as const).every(key => task.parameters![key] === params[key]);
}
