import { uiText } from './locale';
import { ApiError } from './api';

export const exportSections = {
  profile: { get name() { return uiText('账户资料'); }, get scope() { return uiText('本人账户编号、名称、邮箱、创建和邮箱验证时间'); } },
  security_events: { get name() { return uiText('安全事件'); }, get scope() { return uiText('本人安全事件编号、操作与发生时间'); } },
  account_appeals: { get name() { return uiText('账户申诉'); }, get scope() { return uiText('本人账户限制轮次、申诉说明、处理状态和公开答复'); } },
};
export type ExportSection = keyof typeof exportSections;
export type ExportRange = { page: number; cursor?: string; cutoff?: string };
type Request = {
  owner: string;
  section: ExportSection;
  range: ExportRange;
  password: string;
  signal: AbortSignal;
};
type Result = {
  blob: Blob;
  digest: string;
  count: number;
  cutoff: string;
  readAt: string;
  next: string | null;
};
const limit = 512 * 1024;
const wrongFile = () => Error(uiText('文件的格式、范围或校验值不符，已停止下载。'));
function object(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}
function keys(x: unknown, allowed: string[]): x is Record<string, unknown> {
  return object(x) && Object.keys(x).length === allowed.length && allowed.every((k) => Object.hasOwn(x, k));
}
const timestamp = (v: unknown): v is string =>
  typeof v === 'string' &&
  /^\d{4}-\d{2}-\d{2}T[\d:.]+(?:Z|[+-]\d\d:\d\d)$/.test(v) &&
  Number.isFinite(Date.parse(v));
const nonempty = (v: unknown): v is string => typeof v === 'string' && !!v.length;
function validRecord(s: ExportSection, x: unknown, owner: string) {
  if (s === 'profile')
    return (
      keys(x, ['id', 'name', 'email', 'created_at', 'email_verified_at']) &&
      x.id === owner &&
      nonempty(x.name) &&
      nonempty(x.email) &&
      timestamp(x.created_at) &&
      (x.email_verified_at === null || timestamp(x.email_verified_at))
    );
  if (s === 'security_events')
    return (
      keys(x, ['id', 'action', 'created_at']) &&
      nonempty(x.id) &&
      nonempty(x.action) &&
      timestamp(x.created_at)
    );
  return (
    keys(x, [
      'id',
      'restriction_id',
      'state',
      'outcome',
      'version',
      'reason_code',
      'statement',
      'public_reply',
      'created_at',
      'updated_at',
    ]) &&
    nonempty(x.id) &&
    nonempty(x.restriction_id) &&
    ['OPEN', 'RESOLVED'].includes(String(x.state)) &&
    ['', 'UPHELD', 'LIFTED'].includes(String(x.outcome)) &&
    Number.isSafeInteger(x.version) &&
    Number(x.version) > 0 &&
    nonempty(x.reason_code) &&
    typeof x.statement === 'string' &&
    typeof x.public_reply === 'string' &&
    timestamp(x.created_at) &&
    timestamp(x.updated_at)
  );
}
export function checkDataPage(x: unknown, owner: string, section: ExportSection, range: ExportRange) {
  if (
    !keys(x, ['format', 'account_id', 'section', 'read_at', 'created_through', 'items', 'next_cursor']) ||
    x.format !== 'studio.account-export.v1' ||
    x.account_id !== owner ||
    x.section !== section ||
    !timestamp(x.read_at) ||
    !timestamp(x.created_through) ||
    (range.cutoff && x.created_through !== range.cutoff) ||
    !Array.isArray(x.items) ||
    x.items.length > 100 ||
    !x.items.every((row) => validRecord(section, row, owner)) ||
    new Set(x.items.map((row) => row.id)).size !== x.items.length
  )
    throw wrongFile();
  const next = x.next_cursor;
  if (
    (next !== null && (!nonempty(next) || next.length > 1024 || next === range.cursor || !x.items.length)) ||
    (section === 'profile' && (next !== null || x.items.length !== 1 || range.page !== 1))
  )
    throw wrongFile();
  return { count: x.items.length, cutoff: x.created_through, readAt: x.read_at, next: next as string | null };
}
async function bytesOf(response: Response, max: number) {
  const reader = response.body?.getReader();
  if (!reader) throw wrongFile();
  let n = 0;
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      n += part.value.byteLength;
      if (n > max) {
        await reader.cancel();
        throw wrongFile();
      }
      chunks.push(part.value);
    }
  } finally {
    reader.releaseLock();
  }
  const data = new Uint8Array(n);
  let pos = 0;
  for (const c of chunks) {
    data.set(c, pos);
    pos += c.length;
  }
  return data;
}
const errors: Record<string, string> = {
  get ACCOUNT_EXPORT_PASSWORD() { return uiText('当前密码不正确，请重新输入。'); },
  get INVALID_EXPORT_CURSOR() { return uiText('当前分页范围已失效，请从第一页重新开始。'); },
  get INVALID_EXPORT_SECTION() { return uiText('该资料范围不可用，请重新选择。'); },
  get AUTH_BUSY() { return uiText('身份核验繁忙，请稍后手动下载。'); },
  get ACCOUNT_EXPORT_RATE_LIMIT() { return uiText('下载请求过于频繁，请稍后手动下载。'); },
  get AUTH_REQUIRED() { return uiText('登录状态已失效，请重新登录。'); },
};
async function rejectResponse(r: Response): Promise<never> {
  let code = '';
  try {
    const v = JSON.parse(new TextDecoder().decode(await bytesOf(r, 4096)));
    if (typeof v?.error?.code === 'string' && Object.hasOwn(errors, v.error.code)) code = v.error.code;
  } catch {
    /* Never echo raw error bodies from a sensitive request. */
  }
  throw new ApiError(errors[code] || uiText('本页资料未取得，请核对账户状态后手动下载。'), r.status, false, code);
}
export async function requestDataPage({ owner, section, range, password, signal }: Request): Promise<Result> {
  if (
    !nonempty(owner) ||
    !Object.hasOwn(exportSections, section) ||
    !nonempty(password) ||
    password.includes('\0') ||
    new TextEncoder().encode(password).length > 72
  )
    throw Error(uiText('请输入有效的当前密码，最多 72 个 UTF-8 字节。'));
  let r: Response;
  try {
    r = await fetch('/api/v1/auth/data-export', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      signal,
      body: JSON.stringify({ section, password, ...(range.cursor ? { cursor: range.cursor } : {}) }),
    });
  } catch {
    throw Error(uiText('资料连接已中断。如需下载，请重新输入密码并手动提交。'));
  }
  if (!r.ok) return rejectResponse(r);
  const header = r.headers.get('Content-Disposition') || '',
    digest = r.headers.get('X-Content-SHA256') || '';
  const declared = r.headers.get('Content-Length'),
    cache = (r.headers.get('Cache-Control') || '')
      .toLowerCase()
      .split(',')
      .map((s) => s.trim());
  if (
    r.status !== 200 ||
    r.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/json' ||
    !cache.includes('private') ||
    !cache.includes('no-store') ||
    ![
      'attachment; filename="account-' + section + '.json"',
      'attachment; filename=account-' + section + '.json',
    ].includes(header) ||
    !/^[a-f0-9]{64}$/.test(digest) ||
    (declared !== null && (!/^[0-9]+$/.test(declared) || Number(declared) > limit))
  ) {
    await r.body?.cancel();
    throw wrongFile();
  }
  const data = await bytesOf(r, limit);
  const calculated = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
  if (Array.from(calculated, (b) => b.toString(16).padStart(2, '0')).join('') !== digest) throw wrongFile();
  let decoded: unknown;
  try {
    decoded = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data));
  } catch {
    throw wrongFile();
  }
  const meta = checkDataPage(decoded, owner, section, range);
  return { ...meta, blob: new Blob([data], { type: 'application/json' }), digest };
}
export function downloadDataPage(blob: Blob, section: ExportSection, page: number) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  try {
    anchor.download = 'account-' + section + '-p' + page + '.json';
    anchor.href = url;
    anchor.click();
  } finally {
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}
