import { uiText } from './locale';
import { comicApi, comicImagePath, matchesComic } from './comics';
import type { ComicManifest, ComicVersion } from './comics';
import type { Slide } from './presentations';
import { filename, loadVersionSlides, nativePptx, withLayoutProbe } from './presentationExport';
type Fits = (slide: Slide, index: number) => boolean;
export function paginateComic(slides: Slide[], fits: Fits): Slide[] {
  const ids = new Set(slides.map((s) => s.id)),
    out: Slide[] = [];
  const split = new Intl.Segmenter('zh', { granularity: 'grapheme' });
  for (const [index, original] of slides.entries()) {
    if (fits(original, out.length)) {
      out.push(original);
      continue;
    }
    let first = { ...original },
      text = original.body;
    if (!fits({ ...first, body: '' }, out.length)) {
      first.title = uiText('第 ') + (index + 1) + uiText(' 镜');
      text = original.title + '\n' + text;
    }
    const parts = Array.from(split.segment(text), (v) => v.segment);
    let offset = 0,
      page = 1;
    while (offset < parts.length) {
      let next: Slide;
      if (page === 1) next = first;
      else {
        let id = 'continuation_' + index + '_' + page;
        while (ids.has(id)) id += '_';
        ids.add(id);
        next = {
          id,
          layout: 'content',
          title: uiText('第 ') + (index + 1) + uiText(' 镜 · 续页 ') + page,
          body: '',
          notes: '',
        };
      }
      let lo = 1,
        hi = parts.length - offset,
        best = 0;
      while (lo <= hi) {
        const n = Math.floor((lo + hi) / 2);
        const body = parts.slice(offset, offset + n).join('');
        if (fits({ ...next, body }, out.length)) {
          best = n;
          lo = n + 1;
        } else hi = n - 1;
      }
      if (!best) {
        if (page === 1 && fits({ ...next, body: '' }, out.length)) {
          out.push({ ...next, body: '' });
          page++;
          continue;
        }
        throw Error(uiText('当前页面无法测量可读版式，请重新打开分镜再导出。'));
      }
      if (offset + best < parts.length) {
        for (let n = best; n >= Math.ceil(best * 0.8); n--) {
          if (/[\s。！？；，、]/u.test(parts[offset + n - 1])) {
            best = n;
            break;
          }
        }
      }
      out.push({ ...next, body: parts.slice(offset, offset + best).join('') });
      offset += best;
      page++;
    }
  }
  return out;
}
function checkManifest(m: ComicManifest, v: ComicVersion) {
  if (
    !matchesComic(v, v.project_id, v.version) ||
    !v.confirmed ||
    !m ||
    m.source !== 'comic' ||
    m.project_id !== v.project_id ||
    m.version !== v.version ||
    m.created_at !== v.created_at ||
    m.document?.title !== v.document.title ||
    m.document.theme !== 'paper' ||
    !Array.isArray(m.document.slides) ||
    m.document.slides.length !== v.document.panels.length ||
    !m.image_urls ||
    typeof m.image_urls !== 'object'
  )
    throw Error(uiText('导出清单与已确认的分镜版本不符。'));
  for (const [i, s] of m.document.slides.entries()) {
    const p = v.document.panels[i];
    if (
      !s ||
      s.id !== p.id ||
      s.title !== p.title ||
      typeof s.body !== 'string' ||
      typeof s.notes !== 'string' ||
      s.body.length > 12000 ||
      s.notes.length > 32000 ||
      s.layout !== (p.asset_id ? 'image' : 'content')
    )
      throw Error(uiText('导出镜头与原版本不符。'));
    if (
      p.asset_id &&
      (s.asset_id !== p.asset_id ||
        s.asset_revision !== p.asset_revision ||
        s.content_version !== p.content_version ||
        !s.asset_available ||
        !p.asset_available ||
        m.image_urls[p.id] !== comicImagePath(v.project_id, v.version, p.id))
    )
      throw Error(uiText('有项目图片无法从已确认的分镜版本交付。'));
  }
  if (Object.keys(m.image_urls).length !== v.document.panels.filter((p) => p.asset_id).length)
    throw Error(uiText('分镜图片清单有多余来源。'));
}
export async function comicPowerPoint(v: ComicVersion, signal: AbortSignal) {
  if (!v.confirmed) throw Error(uiText('请先确认已保存的分镜版本。'));
  signal = AbortSignal.any([signal, AbortSignal.timeout(170000)]);
  const read = async () => {
    const m = await comicApi.manifest(v.project_id, v.version, signal);
    checkManifest(m, v);
    return m;
  };
  const first = await read();
  const pages = withLayoutProbe(first, (fits) => paginateComic(first.document.slides, fits));
  const projected = { ...first, document: { ...first.document, slides: pages } };
  const delivery = await loadVersionSlides(
    projected,
    first.image_urls,
    (id) => comicImagePath(v.project_id, v.version, id),
    signal,
  );
  const blob = await nativePptx(delivery, signal);
  const last = await read();
  if (JSON.stringify(first.document) !== JSON.stringify(last.document))
    throw Error(uiText('分镜导出期间原清单发生变化，请重新核对。'));
  signal.throwIfAborted();
  return { blob, name: filename(first, 'pptx') };
}
