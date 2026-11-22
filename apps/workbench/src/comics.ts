import { uiText } from './locale';
import { request } from './api';
import type { AssetEntry } from './libraryState';
import { mediaContentAllowed } from './mediaModeration';
import type { Deck } from './presentations';
export type Card = { id: string; name: string; description: string };
export type Panel = {
  id: string;
  title: string;
  scene_id: string;
  character_ids: string[];
  action: string;
  dialogue: string;
  asset_id?: string;
  asset_revision?: number;
  content_version?: string;
  asset_available?: boolean;
};
export type Comic = { title: string; story: string; characters: Card[]; scenes: Card[]; panels: Panel[] };
export type ComicVersion = {
  project_id: string;
  version: number;
  created_at: string;
  document: Comic;
  confirmed: boolean;
};
export type ComicSummary = Pick<ComicVersion, 'project_id' | 'version' | 'created_at' | 'confirmed'> & {
  title: string;
  panel_count: number;
};
export type ComicManifest = {
  source: 'comic';
  project_id: string;
  version: number;
  created_at: string;
  document: Deck;
  image_urls: Record<string, string>;
};
export type ComicAction = 'save' | 'confirm';
export type ComicTicket = { operation: ComicAction; expected: number; key: string };
const enc = encodeURIComponent;
const base = (p: string) => '/projects/' + enc(p) + '/comic';
const fixed = (p: string, n: number) => base(p) + '/versions/' + n;
const headers = (p: string, key?: string) => ({
  'X-Project-ID': p,
  ...(key ? { 'Idempotency-Key': key } : {}),
});
export const comicImagePath = (p: string, n: number, id: string) =>
  '/api/v1' + fixed(p, n) + '/panels/' + enc(id) + '/image';
export const comicApi = {
  current: (p: string) => request<ComicVersion>(base(p), { headers: headers(p) }),
  history: (p: string, before?: number) =>
    request<{ items: ComicSummary[]; next_before_version?: number }>(
      base(p) + '/versions?limit=24' + (before ? '&before_version=' + before : ''),
      { headers: headers(p) },
    ),
  version: (p: string, n: number) => request<ComicVersion>(fixed(p, n), { headers: headers(p) }),
  save: (p: string, n: number, d: Comic, k: string) =>
    request<ComicVersion>(base(p), {
      method: 'POST',
      headers: headers(p, k),
      body: JSON.stringify({ expected_version: n, document: writableComic(d) }),
    }),
  confirm: (p: string, n: number, k: string) =>
    request<ComicVersion>(fixed(p, n) + '/confirm', { method: 'POST', headers: headers(p, k) }),
  receipt: (p: string, a: ComicAction, k: string) =>
    request<ComicVersion & { operation: ComicAction }>(base(p) + '/receipt?operation=' + a, {
      headers: headers(p, k),
    }),
  manifest: (p: string, n: number, signal: AbortSignal) =>
    request<ComicManifest>(fixed(p, n) + '/export', { headers: headers(p), signal }),
};
export const newCard = (): Card => ({ id: crypto.randomUUID(), name: '', description: '' });
export const newPanel = (scene: string): Panel => ({
  id: crypto.randomUUID(),
  scene_id: scene,
  character_ids: [],
  title: '',
  action: '',
  dialogue: '',
});
export function blankComic(): Comic {
  const sc = newCard();
  return { title: '', story: '', characters: [newCard()], scenes: [sc], panels: [newPanel(sc.id)] };
}
export function writableComic(d: Comic): Comic {
  const card = ({ id, name, description }: Card) => ({ id, name, description });
  return {
    title: d.title,
    story: d.story,
    characters: d.characters.map(card),
    scenes: d.scenes.map(card),
    panels: d.panels.map(
      ({ id, title, scene_id, character_ids, action, dialogue, asset_id, asset_revision }) => ({
        id,
        title,
        scene_id,
        character_ids: [...character_ids],
        action,
        dialogue,
        ...(asset_id ? { asset_id, asset_revision } : {}),
      }),
    ),
  };
}
const validText = (v: unknown, max: number, required: boolean, title = false) =>
  typeof v === 'string' &&
  Array.from(v).length <= max &&
  (!required || !!v.trim()) &&
  !/[\p{Cf}\p{Cs}\uFFFE\uFFFF\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(v) &&
  (!title || !/[\r\n\t]/.test(v));
const validID = (s: unknown) => typeof s === 'string' && /^[-\w]{1,48}$/.test(s);
export function comicProblem(d: Comic) {
  if (!d || !validText(d.title, 120, true, true) || !validText(d.story, 4000, true))
    return uiText('请填写分镜本名称（1–120 字）及故事梗概（1–4000 字）。');
  for (const [label, cards] of [
    [uiText('角色'), d.characters],
    [uiText('场景'), d.scenes],
  ] as const) {
    if (
      !Array.isArray(cards) ||
      cards.length < 1 ||
      cards.length > 16 ||
      new Set(cards.map((c) => c?.id)).size !== cards.length ||
      cards.some(
        (c) =>
          !c ||
          !validID(c.id) ||
          !validText(c.name, 80, true, true) ||
          !validText(c.description, 1000, false),
      )
    )
      return label + uiText('卡需有独立编号和 1–80 字的名称，描述最多 1000 字，每本可用 1–16 张。');
  }
  if (
    !Array.isArray(d.panels) ||
    d.panels.length < 1 ||
    d.panels.length > 12 ||
    new Set(d.panels.map((p) => p?.id)).size !== d.panels.length
  )
    return uiText('分镜本需有 1–12 个不重复的镜头。');
  if (d.panels.filter((p) => p?.asset_id).length > 4) return uiText('一本最多有 4 个配图镜头。');
  for (const [i, p] of d.panels.entries()) {
    const pre = uiText('第 ') + (i + 1) + uiText(' 镜');
    if (
      !p ||
      !validID(p.id) ||
      !validText(p.title, 120, true, true) ||
      !validText(p.action, 1000, true) ||
      !validText(p.dialogue, 1000, false)
    )
      return pre + uiText('需有标题（1–120 字）和动作（1–1000 字），对白最多 1000 字。');
    if (
      !d.scenes.some((c) => c.id === p.scene_id) ||
      !Array.isArray(p.character_ids) ||
      p.character_ids.length > 8 ||
      new Set(p.character_ids).size !== p.character_ids.length ||
      p.character_ids.some((id) => !d.characters.some((c) => c.id === id))
    )
      return pre + uiText('需要一个已登记场景，最多 8 位不同角色。');
    if (p.asset_id || p.asset_revision) {
      if (!p.asset_id || !Number.isSafeInteger(p.asset_revision) || p.asset_revision! < 1)
        return pre + uiText('的项目图片修订缺失。');
    }
  }
  return '';
}
export const matchesComic = (v: ComicVersion, p: string, n?: number) =>
  !!v &&
  v.project_id === p &&
  typeof v.confirmed === 'boolean' &&
  Number.isSafeInteger(v.version) &&
  v.version >= 1 &&
  (!n || n === v.version) &&
  !comicProblem(v.document);
export const usableComicImage = (a: AssetEntry, p: string) =>
  !!a &&
  a.project_id === p &&
  a.state === 'active' &&
  a.kind === 'image' &&
  !!a.content_url &&
  ['image/png', 'image/jpeg'].includes(a.content_type || '') &&
  a.revision > 0 &&
  mediaContentAllowed(a.moderation);
export const comicSlot = (u: string, p: string) => 'xingtu-comic-operation:' + JSON.stringify([u, p]);
export function readComicTicket(slot: string): ComicTicket | null {
  const raw = sessionStorage.getItem(slot);
  if (!raw) return null;
  const t = JSON.parse(raw);
  if (
    !t ||
    !['save', 'confirm'].includes(t.operation) ||
    !Number.isSafeInteger(t.expected) ||
    t.expected < 0 ||
    (t.operation === 'confirm' && t.expected === 0) ||
    typeof t.key !== 'string' ||
    !/^[-\w]{12,100}$/.test(t.key)
  )
    throw Error(uiText('原操作编号无法核对。'));
  return t;
}
