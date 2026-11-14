export const MAX_RETRY_SECONDS = 300;
export const ACCOUNT_RETRY_SECONDS = 900;
export function retryDeadline(value: string | null, status: number, now = Date.now(), maximumSeconds = MAX_RETRY_SECONDS): number {
  if (status !== 429 && status !== 503) return 0;
  const header = value?.trim() || '';
  const seconds = /^\d+$/.test(header) ? Number(header) : header ? (Date.parse(header) - now) / 1000 : 3;
  const bounded = Number.isFinite(seconds) ? Math.max(0, Math.min(maximumSeconds, seconds)) : 3;
  return now + Math.ceil(bounded * 1000);
}
export function remainingSeconds(deadline: number, now = Date.now(), maximumSeconds = MAX_RETRY_SECONDS): number { return Math.max(0, Math.min(maximumSeconds, Math.ceil((deadline - now) / 1000))); }
export function safeRequestId(value: unknown): string | undefined { return typeof value === 'string' && /^[A-Za-z0-9._:-]{1,128}$/.test(value) ? value : undefined; }
export function keepUnknown(priorUnknown: boolean, error: { uncertain?: boolean }): boolean { return priorUnknown || error.uncertain === true; }
export function requestAdvice(error: { status: number; code?: string }, unknown: boolean): string {
  if (error.code?.startsWith('MODERATION_')) return unknown ? '此前请求是否受理仍待核对。请保留原输入、模型、报价与原请求键，先刷新历史；不要另开一次生成。' : error.code === 'MODERATION_MEDIA_UNSUPPORTED' ? '当前只接入文本审核。媒体新生成未受理；历史任务与已有未知请求的核对仍然保留，不会自动换键重发。' : error.code === 'MODERATION_UNAVAILABLE' ? '本次请求已确认未进入生成或冻结额度。输入仍保留；稍后可手动提交，系统不会自动重试。' : ['MODERATION_INPUT_BLOCKED', 'MODERATION_INPUT_REVIEW', 'MODERATION_INPUT_INVALID', 'MODERATION_BUSY', 'MODERATION_CONTEXT_CHANGED'].includes(error.code) ? '这次新请求未受理，输入仍保留。调整内容或等待服务恢复后由你重新确认提交，系统不会自动重试。' : '请刷新原运行核对文本检查状态，必要时提供请求编号联系维护者；不会自动重新生成。';
  if (error.code === 'LEDGER_CAPACITY') return unknown ? '原请求仍待确认，请保留原报价与操作键，核对历史后再用原请求重试。' : '本次新生成未受理，工作区累计结算与冻结已达账务容量上限。请联系工作区维护者核对；释放可用余额不一定解除此限制。';
  if (['INSUFFICIENT_CREDITS', 'QUOTE_REQUIRED', 'QUOTE_INVALID', 'QUOTE_MISMATCH', 'QUOTE_EXPIRED', 'QUOTE_USED'].includes(error.code || '')) return unknown ? '此前请求的受理仍未确认。请保留原报价、原正文与原请求键，核对历史或使用原请求重试；不要另开一次生成。' : error.code === 'INSUFFICIENT_CREDITS' ? '本次新生成未受理。请到工作区权益核对可用与冻结额度，减少交付数量或选择其他模型后重新报价；不会自动重试。' : '请核对历史记录与当前输入，再手动重新获取报价并确认。不会自动发起新的生成。';
  if (['MODEL_VERSION_CHANGED', 'MODEL_DISABLED', 'INVALID_MODEL_PARAMETERS'].includes(error.code || '')) return unknown ? '当前目录已变化，但原请求的受理仍需核对。请保留原请求键和参数；刷新目录不会自动重新提交。' : '请手动刷新模型目录，重新确认可用模型与创作规格后提交。输入内容已保留，不会自动重新生成。';
  if (error.status === 429) return unknown ? '本次重试受到限制；此前提交的受理状态仍未确认。等待冷却后使用原请求重试，不要重新创建任务。' : error.code?.startsWith('ADMISSION_') ? '本次新请求未受理。等待现有任务释放容量，或调整模型后手动提交；输入内容仍保留。' : '请求暂时受到限制。等待冷却后再操作；平台不会自动重新提交。';
  if (error.status === 503) return unknown ? '服务暂不可用，提交可能已经受理。恢复后请使用原请求重试，平台不会自动再次提交。' : '服务暂不可用。等待冷却后手动刷新；持续失败时可复制请求编号协助定位。';
  return '';
}
