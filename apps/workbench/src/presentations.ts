import { uiText } from './locale';
import { request } from './api';

export const themes = { get paper() { return uiText('暖白纸页'); }, get midnight() { return uiText('午夜蓝'); }, get mint() { return uiText('薄荷绿'); } };
export const layouts = { get title() { return uiText('封面'); }, get content() { return uiText('正文'); }, get image() { return uiText('图文'); } };
export type Slide = {
  id: string;
  layout: keyof typeof layouts;
  title: string;
  body: string;
  notes: string;
  asset_id?: string;
  asset_revision?: number;
  content_version?: string;
  asset_available?: boolean;
};
export type Deck = { title: string; theme: keyof typeof themes; slides: Slide[] };
export type DeckSummary = {
  id: string;
  project_id: string;
  version: number;
  title: string;
  slide_count: number;
  created_at: string;
};
export type DeckVersion = {
  id: string;
  project_id: string;
  version: number;
  document: Deck;
  created_at: string;
};
export type DeckManifest = DeckVersion & { image_urls: Record<string, string> };
export const colors = {
  paper: { paper: 'F5F0E5', ink: '202D33', accent: 'A55332', sub: '4B5A5B' },
  midnight: { paper: '1A283A', ink: 'FCF6EC', accent: 'F2AE78', sub: 'CDD8E1' },
  mint: { paper: 'DFF1E8', ink: '183B35', accent: '226A51', sub: '395A51' },
};
const part = encodeURIComponent;
export const base = (p: string) => '/projects/' + part(p) + '/presentations';
export const versionPath = (p: string, id: string, v: number) => base(p) + '/' + part(id) + '/versions/' + v;
export const imagePath = (p: string, id: string, v: number, slide: string) =>
  '/api/v1' + versionPath(p, id, v) + '/slides/' + part(slide) + '/image';
const headers = (p: string, key?: string) => ({
  'X-Project-ID': p,
  ...(key ? { 'Idempotency-Key': key } : {}),
});
export const deckApi = {
  list: (p: string, cursor?: string) =>
    request<{ items: DeckSummary[]; next_cursor?: string }>(
      base(p) + '?limit=24' + (cursor ? '&cursor=' + part(cursor) : ''),
      { headers: headers(p) },
    ),
  versions: (p: string, id: string, before?: number) =>
    request<{ items: DeckSummary[]; next_before_version?: number }>(
      base(p) + '/' + part(id) + '/versions?limit=20' + (before ? '&before_version=' + before : ''),
      { headers: headers(p) },
    ),
  version: (p: string, id: string, v: number) =>
    request<DeckVersion>(versionPath(p, id, v), { headers: headers(p) }),
  export: (p: string, id: string, v: number, signal: AbortSignal) =>
    request<DeckManifest>(versionPath(p, id, v) + '/export', { headers: headers(p), signal }),
  save: (p: string, id: string | undefined, v: number, doc: Deck, key: string) =>
    request<DeckVersion>(base(p), {
      method: 'POST',
      headers: headers(p, key),
      body: JSON.stringify({ presentation_id: id, expected_version: v, document: writable(doc) }),
    }),
  receipt: (p: string, key: string) =>
    request<DeckVersion>(base(p) + '/receipt', { headers: headers(p, key) }),
};
export const freshSlide = (layout: Slide['layout'] = 'content'): Slide => ({
  id: crypto.randomUUID(),
  title: '',
  layout,
  body: '',
  notes: '',
});
export const blank = (): Deck => ({ title: '', theme: 'paper', slides: [freshSlide('title')] });
export function writable(doc: Deck): Deck {
  return {
    title: doc.title,
    theme: doc.theme,
    slides: doc.slides.map(({ id, title, layout, body, notes, asset_id, asset_revision }) => ({
      id,
      title,
      layout,
      body,
      notes,
      ...(layout === 'image' ? { asset_id, asset_revision } : {}),
    })),
  };
}
export function outlineDocument(raw: string, theme: Deck['theme'], title: string): Deck {
  const input = raw.trim().replace(/\r\n?/g, '\n');
  if (!input) throw Error(uiText('请输入材料或大纲。'));
  const sections = input
    .split(/\n\s*\n|\n(?=#{1,6} )/u)
    .map((v) => v.trim())
    .filter(Boolean);
  const slides = sections.map((section, index) => {
    const [heading, ...lines] = section.split('\n');
    const full = heading.replace(/^#{1,6}\s+/u, '').trim();
    const long = [...full].length > 120;
    return {
      ...freshSlide(index ? 'content' : 'title'),
      title: long ? uiText('第 ') + (index + 1) + uiText(' 页') : full,
      body: (long ? [full, ...lines] : lines).join('\n').trim(),
    };
  });
  const next = { title: title.trim() || slides[0].title, theme, slides };
  const issue = deckProblem(next);
  if (issue) throw Error(issue);
  return next;
}
export function deckProblem(doc: Deck): string {
  const length = (s: string) => Array.from(s).length,
    invalid = (s: string) =>
      /[\p{Cf}\p{Cs}\uFFFE\uFFFF\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(s);
  if (
    !doc ||
    !Object.hasOwn(themes, doc.theme) ||
    typeof doc.title !== 'string' ||
    !doc.title.trim() ||
    length(doc.title) > 120 ||
    invalid(doc.title) ||
    /[\r\n\t]/.test(doc.title)
  )
    return uiText('文稿名称需为 1–120 个字符。');
  if (!Array.isArray(doc.slides) || doc.slides.length < 1 || doc.slides.length > 30)
    return uiText('每份文稿可保存 1–30 页。');
  if (doc.slides.filter((s) => s?.layout === 'image').length > 4) return uiText('每份文稿最多使用 4 个图文页。');
  const ids = new Set<string>();
  for (const [index, s] of doc.slides.entries()) {
    const prefix = uiText('第 ') + (index + 1) + uiText(' 页');
    if (
      !s ||
      typeof s.id !== 'string' ||
      !/^[-\w]{1,48}$/.test(s.id) ||
      ids.has(s.id) ||
      !Object.hasOwn(layouts, s.layout)
    )
      return prefix + uiText('的页面编号或版式无效。');
    ids.add(s.id);
    if (
      [s.title, s.body, s.notes].some((v) => typeof v !== 'string' || invalid(v)) ||
      /[\r\n\t]/.test(s.title) ||
      !s.title.trim() ||
      length(s.title) > 120 ||
      length(s.body) > 4000 ||
      length(s.notes) > 2000
    )
      return prefix + uiText('需填写标题（最多 120 字），正文限 4000 字、备注限 2000 字。');
    if (
      s.layout === 'image' &&
      (!s.asset_id || !Number.isSafeInteger(s.asset_revision) || Number(s.asset_revision) < 1)
    )
      return prefix + uiText('请从当前项目选择图片。');
  }
  return new TextEncoder().encode(JSON.stringify(writable(doc))).length > 262144
    ? uiText('文稿内容超过 256 KiB，请分成两份文稿。')
    : '';
}
export function sameVersion(v: DeckVersion, p: string, id?: string, n?: number) {
  return (
    !!v &&
    typeof v.id === 'string' &&
    !!v.id &&
    v.project_id === p &&
    (!id || v.id === id) &&
    Number.isSafeInteger(v.version) &&
    v.version >= 1 &&
    (!n || v.version === n) &&
    !deckProblem(v.document)
  );
}
export function summaryMatches(v: DeckSummary, p: string) {
  return (
    !!v &&
    typeof v.id === 'string' &&
    !!v.id &&
    v.project_id === p &&
    Number.isSafeInteger(v.version) &&
    v.version > 0 &&
    typeof v.title === 'string' &&
    Number.isSafeInteger(v.slide_count) &&
    v.slide_count >= 1 &&
    v.slide_count <= 30
  );
}
export type SaveTicket = { key: string; id?: string; expected: number };
export const saveSlot = (owner: string, p: string) =>
  'xingtu-presentation-receipt:' + JSON.stringify([owner, p]);
export function readTicket(slot: string): SaveTicket | null {
  const raw = sessionStorage.getItem(slot);
  if (!raw) return null;
  const ticket = JSON.parse(raw);
  if (
    !ticket ||
    !/^[-\w]{12,100}$/.test(ticket.key) ||
    !Number.isSafeInteger(ticket.expected) ||
    ticket.expected < 0 ||
    (ticket.expected > 0 && typeof ticket.id !== 'string')
  )
    throw Error(uiText('原保存收据编号无法核对。'));
  return ticket;
}
