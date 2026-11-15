export type ModerationSummary = { policy_id: string; policy_version: string; phase: 'input' | 'output'; state: 'PENDING' | 'ALLOWED' | 'BLOCKED' | 'REVIEW'; version: number; reason_code?: string };
export type ModerationCapability = { enabled: boolean; mode: 'disabled' | 'rules' | 'http'; policy_id?: string; policy_version?: string; text_delivery: 'stream' | 'after_review'; media_supported: boolean; media_mode?: 'disabled' | 'fixtures' | 'http'; media_policy_id?: string; media_policy_version?: string };
type TextSnapshot = { moderation?: ModerationSummary | null; status: string; text: string };

export function validModeration(value: unknown): value is ModerationSummary {
  const summary = value as ModerationSummary | null;
  return !!summary && typeof summary.policy_id === 'string' && summary.policy_id.length > 0 && summary.policy_id.length <= 128 && typeof summary.policy_version === 'string' && summary.policy_version.length > 0 && summary.policy_version.length <= 128 && ['input', 'output'].includes(summary.phase) && ['PENDING', 'ALLOWED', 'BLOCKED', 'REVIEW'].includes(summary.state) && Number.isSafeInteger(summary.version) && summary.version > 0;
}
export function moderationState(value?: ModerationSummary | null): ModerationSummary['state'] | undefined { return value == null ? undefined : validModeration(value) ? value.state : 'REVIEW'; }
export function moderationAllowsText(value?: ModerationSummary | null, status?: string): boolean { return value == null || validModeration(value) && value.phase === 'output' && value.state === 'ALLOWED' && status === 'COMPLETED'; }
export function visibleModeratedText(value: TextSnapshot): string { return moderationAllowsText(value.moderation, value.status) ? value.text : ''; }
export function moderationMediaBlocked(value?: ModerationCapability): boolean { return value?.enabled === true && value.media_supported !== true; }
export const moderationNames = { PENDING: '生成与检查中', ALLOWED: '文本检查已通过', BLOCKED: '回复已限制', REVIEW: '文本检查待核实' };
export function moderationDisplayState(value?: ModerationSummary | null, status?: string): ModerationSummary['state'] | undefined {
  const state = value != null && value.phase !== 'output' ? 'REVIEW' : moderationState(value);
  if (state !== 'ALLOWED' || status === undefined || status === 'COMPLETED') return state;
  return ['FAILED', 'INTERRUPTED', 'CANCELLED'].includes(status) ? 'REVIEW' : 'PENDING';
}
export function moderationText(value?: ModerationSummary | null, status?: string): string { const state = moderationDisplayState(value, status); return state ? moderationNames[state] : ''; }

export function sameModerationDecision(left?: ModerationSummary | null, right?: ModerationSummary | null): boolean {
  if (left == null || right == null) return left == null && right == null;
  return validModeration(left) && validModeration(right) && left.policy_id === right.policy_id && left.policy_version === right.policy_version && left.phase === right.phase && left.version === right.version && left.state === right.state;
}
/** The frozen policy is part of a run's identity. Never clear a newer restriction with an old/missing projection. */
export function mergeModeration(previous?: ModerationSummary | null, incoming?: ModerationSummary | null): ModerationSummary | undefined {
  if (previous == null) return incoming ?? undefined;
  if (incoming == null) return previous;
  if (!validModeration(incoming)) return incoming;
  if (!validModeration(previous)) return previous;
  if (previous.policy_id !== incoming.policy_id || previous.policy_version !== incoming.policy_version || previous.phase !== incoming.phase) return { ...previous, state: 'REVIEW', reason_code: 'projection_conflict' };
  if (previous.version !== incoming.version) return previous.version > incoming.version ? previous : incoming;
  const restriction = { ALLOWED: 0, PENDING: 1, REVIEW: 2, BLOCKED: 3 };
  return restriction[previous.state] > restriction[incoming.state] ? previous : incoming;
}
/** Resource sequence and moderation decision versions are independent. A stale content snapshot cannot undo a restriction. */
export function mergeModeratedText(previous: TextSnapshot | undefined, incoming: TextSnapshot, acceptIncoming = true): { moderation?: ModerationSummary; text: string } {
  const moderation = mergeModeration(previous?.moderation, incoming.moderation);
  const source = acceptIncoming && sameModerationDecision(moderation, incoming.moderation) ? incoming : previous && sameModerationDecision(moderation, previous.moderation) ? previous : undefined;
  return { ...(moderation ? { moderation } : {}), text: source ? visibleModeratedText({ ...source, moderation }) : '' };
}

export function moderationErrorMessage(code?: string): string | undefined {
  if (!code?.startsWith('MODERATION_')) return undefined;
  return ({ MODERATION_INPUT_REVIEW: '这次输入需要进一步核实，新生成未受理。请调整输入或联系维护者。', MODERATION_INPUT_INVALID: '输入不符合当前文本检查限制，新生成未受理。请缩短或调整输入。', MODERATION_BUSY: '文本检查繁忙，新生成未受理。请等待冷却后再手动操作。', MODERATION_CONTEXT_CHANGED: '对话上下文已变化，新生成未受理。请刷新会话并核对当前分支，输入仍保留。', MODERATION_INPUT_BLOCKED: '这次输入未通过文本检查，新生成未受理。请调整输入后再手动提交。', MODERATION_UNAVAILABLE: '文本检查暂不可用，请稍后核对请求状态。输入已保留，不会自动重新提交。', MODERATION_MEDIA_UNSUPPORTED: '当前文本审核模式尚未支持图片与视频的新生成。已有任务仍可查看或核对。', MODERATION_OUTPUT_BLOCKED: '此回复未通过文本检查，内容未开放。', MODERATION_REVIEW_REQUIRED: '此回复的文本检查尚未完成，内容暂不开放。' } as Record<string, string>)[code] || '文本检查需要处理。请刷新状态或联系维护者，并提供请求编号；不会自动重新生成。';
}
