import { uiText, currentLocale } from './locale';
import { colors, deckApi, imagePath, sameVersion } from './presentations';
import type { DeckVersion, Slide } from './presentations';
export type ImageData = { data: string; width: number; height: number };
export type SlideVersion = Pick<DeckVersion, 'project_id' | 'version' | 'created_at' | 'document'>;
export type Delivery<V extends SlideVersion = DeckVersion> = {
  version: V;
  images: Record<string, ImageData>;
};
const MB = 1024 * 1024;
function failAbort(signal: AbortSignal) {
  signal.throwIfAborted();
}
function toData(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(Error(uiText('图片无法读取。')));
    reader.readAsDataURL(blob);
  });
}
async function loadImage(path: string, project: string, hash: string, limit: number, signal: AbortSignal) {
  const res = await fetch(path, {
    headers: { 'X-Project-ID': project },
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    signal,
  });
  const type = res.headers.get('content-type')?.split(';')[0].toLowerCase();
  if (
    !res.ok ||
    !res.body ||
    !type ||
    !['image/png', 'image/jpeg'].includes(type) ||
    Number(res.headers.get('content-length') || 0) > limit
  ) {
    await res.body?.cancel();
    throw Error(uiText('图片已不可读取或超过大小限制，请重新核对该版本。'));
  }
  const width = Number(res.headers.get('X-Image-Width')),
    height = Number(res.headers.get('X-Image-Height'));
  if (
    !Number.isSafeInteger(width) ||
    !Number.isSafeInteger(height) ||
    width < 1 ||
    height < 1 ||
    width * height > 16000000
  ) {
    await res.body.cancel();
    throw Error(uiText('图片像素尺寸超限或未获核验。'));
  }
  const reader = res.body.getReader(),
    chunks: ArrayBuffer[] = [];
  let size = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > limit) {
        await reader.cancel();
        throw Error(uiText('图片超过本稿导出大小限制。'));
      }
      chunks.push(new Uint8Array(value).buffer);
    }
  } finally {
    reader.releaseLock();
  }
  const blob = new Blob(chunks, { type }),
    buf = await blob.arrayBuffer(),
    signature = new Uint8Array(buf);
  if (
    type === 'image/png'
      ? ![137, 80, 78, 71, 13, 10, 26, 10].every((b, i) => signature[i] === b)
      : signature[0] !== 255 || signature[1] !== 216
  )
    throw Error(uiText('图片编码与声明不符。'));
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', buf)), (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('');
  if (digest !== hash) throw Error(uiText('图片字节与该版本冻结的内容不符。'));
  const bitmap = await createImageBitmap(blob);
  try {
    if (bitmap.width !== width || bitmap.height !== height) throw Error(uiText('图片尺寸与核验记录不符。'));
    failAbort(signal);
    return { size, image: { data: await toData(blob), width: bitmap.width, height: bitmap.height } };
  } finally {
    bitmap.close();
  }
}
export async function loadDelivery(
  project: string,
  id: string,
  version: number,
  signal: AbortSignal,
): Promise<Delivery> {
  signal = AbortSignal.any([signal, AbortSignal.timeout(120_000)]);
  const doc = await deckApi.export(project, id, version, signal);
  if (!sameVersion(doc, project, id, version) || !doc.image_urls || typeof doc.image_urls !== 'object')
    throw Error(uiText('导出清单与所选版本不符。'));
  return loadVersionSlides(doc, doc.image_urls, (s) => imagePath(project, id, version, s), signal);
}
export async function loadVersionSlides<V extends SlideVersion>(
  doc: V,
  urls: Record<string, string>,
  imageURL: (id: string) => string,
  signal: AbortSignal,
): Promise<Delivery<V>> {
  verifyLayout(doc);
  const images: Record<string, ImageData> = Object.create(null);
  let total = 0;
  for (const s of doc.document.slides) {
    if (s.layout !== 'image') continue;
    const path = imageURL(s.id);
    if (
      !s.asset_available ||
      !s.content_version ||
      !/^[a-f0-9]{64}$/.test(s.content_version) ||
      urls[s.id] !== path
    )
      throw Error(uiText('有图片不可用于本次导出，请核对项目素材。'));
    const { image, size } = await loadImage(
      path,
      doc.project_id,
      s.content_version,
      Math.min(8 * MB, 32 * MB - total),
      signal,
    );
    total += size;
    images[s.id] = image;
  }
  failAbort(signal);
  return { version: doc, images };
}
const escape = (str: string) =>
  str.replace(
    /[&<>"']/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!,
  );
export function slideSizing(s: Slide) {
  const weight = (text: string) =>
    [...text].reduce(
      (n, c) => n + (c === '\t' ? 4 : c.charCodeAt(0) > 255 ? 1.04 : /[MW@%]/u.test(c) ? 1 : 0.64),
      0,
    );
  const width = s.layout === 'image' ? 5.05 : 11.2,
    titleHeight = s.layout === 'title' ? 1.94 : 1.52,
    bodyHeight = s.layout === 'title' ? 1.92 : 3.35;
  const rows = (text: string, pt: number) =>
    text
      .split(/\r\n?|\n/u)
      .reduce((n, line) => n + Math.max(1, Math.ceil((weight(line) * pt) / (width * 72))), 0);
  let titleSize = s.layout === 'title' ? 48 : s.layout === 'image' ? 34 : 37;
  while (titleSize > 23 && rows(s.title, titleSize) * titleSize * 1.18 > titleHeight * 72) titleSize--;
  let bodySize = s.layout === 'title' ? 24 : 23;
  while (bodySize > 16 && rows(s.body, bodySize) * bodySize * 1.43 > bodyHeight * 72) bodySize--;
  const fits =
    rows(s.title, titleSize) * titleSize * 1.18 <= titleHeight * 72 &&
    rows(s.body, bodySize) * bodySize * 1.43 <= bodyHeight * 72;
  return { titleSize, bodySize, fits };
}
export const pageStyle = `
.pd-sheet{container-type:inline-size;width:100%;aspect-ratio:16/9;overflow:hidden;background:var(--sheet);color:var(--ink);position:relative;box-sizing:border-box;font-family:"Microsoft YaHei",Arial,sans-serif;line-height:1.32;text-align:left}
.pd-sheet *{box-sizing:border-box}.pd-sheet .pd-accent{position:absolute;top:8.5%;left:6%;width:6%;height:1%;background:var(--accent)}
.pd-sheet .pd-title,.pd-sheet .pd-body{position:absolute;left:6%;width:88%;margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.pd-sheet .pd-title{top:18%;font-size:var(--heading);line-height:1.12;letter-spacing:-.025em;font-weight:700}
.pd-sheet .pd-body{top:43%;height:46%;font-size:var(--copy);line-height:1.34;color:var(--sub)}
.pd-sheet.cover .pd-title{top:29%}.pd-sheet.cover .pd-body{top:60%;height:27%}
.pd-sheet.pic .pd-title,.pd-sheet.pic .pd-body{width:40%}
.pd-sheet.pic img,.pd-sheet .pd-placeholder{position:absolute;left:52%;top:10%;width:42%;height:78%;object-fit:contain}
.pd-sheet .pd-placeholder{display:grid;place-items:center;border:1px dashed var(--accent);font-size:2.4cqw}
.pd-sheet .pd-mark{position:absolute;bottom:4%;left:6%;width:88%;border-top:1px solid var(--sub);padding-top:1%;font-size:1.2cqw;color:var(--sub);text-align:right}`;
export function slideMarkup(v: SlideVersion, s: Slide, index: number, image?: ImageData) {
  const p = colors[v.document.theme],
    { titleSize, bodySize } = slideSizing(s);
  const style =
    '--sheet:#' +
    p.paper +
    ';--ink:#' +
    p.ink +
    ';--accent:#' +
    p.accent +
    ';--sub:#' +
    p.sub +
    ';--heading:' +
    titleSize / 9.6 +
    'cqw;--copy:' +
    bodySize / 9.6 +
    'cqw';
  return (
    '<section class="pd-sheet ' +
    (s.layout === 'title' ? 'cover' : s.layout === 'image' ? 'pic' : '') +
    '" style="' +
    style +
    '"><i class="pd-accent"></i><h2 class="pd-title">' +
    escape(s.title) +
    '</h2><p class="pd-body">' +
    escape(s.body) +
    '</p>' +
    (image ? '<img src="' + image.data + '" alt="">' : '') +
    '<div class="pd-mark">' +
    (index + 1) +
    ' / ' +
    v.document.slides.length +
    '</div></section>'
  );
}
export function layoutProblem(v: SlideVersion) {
  const i = v.document.slides.findIndex((s) => !slideSizing(s).fits);
  return i < 0 ? '' : densityMessage(i);
}
function densityMessage(i: number) {
  return (
    uiText('第 ') +
    (i + 1) +
    uiText(' 页内容超过当前版式在可读字号下的空间。请拆分页面或将详细说明移入演讲备注，再保存新版本。')
  );
}
export function withLayoutProbe<T>(
  v: SlideVersion,
  check: (fits: (s: Slide, index: number) => boolean) => T,
): T {
  const probe = document.createElement('div');
  probe.style.cssText =
    'width:1280px;position:fixed;left:-20000px;top:0;visibility:hidden;pointer-events:none';
  document.body.append(probe);
  function fits(s: Slide, index: number) {
    if (!slideSizing(s).fits) return false;
    probe.innerHTML = slideMarkup(v, s, index);
    const heading = probe.querySelector<HTMLElement>('.pd-title')!,
      body = probe.querySelector<HTMLElement>('.pd-body')!;
    return (
      heading.getBoundingClientRect().bottom <= body.getBoundingClientRect().top - 3 &&
      body.scrollHeight <= body.clientHeight + 1 &&
      heading.scrollWidth <= heading.clientWidth + 1 &&
      body.scrollWidth <= body.clientWidth + 1
    );
  }
  try {
    return check(fits);
  } finally {
    probe.remove();
  }
}
function verifyLayout(v: SlideVersion) {
  withLayoutProbe(v, (fits) => {
    for (const [i, s] of v.document.slides.entries()) if (!fits(s, i)) throw Error(densityMessage(i));
  });
}
export function offlineHTML(delivery: Delivery) {
  const v = delivery.version;
  return (
    '<!doctype html><html lang="' + currentLocale() + '"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data:; style-src \'unsafe-inline\'; base-uri \'none\'; form-action \'none\'"><title>' +
    escape(v.document.title) +
    '</title><style>' +
    pageStyle +
    'body{background:#202020;margin:0}main{max-width:1280px;margin:auto}section{margin:2vh 0}@page{size:13.33333in 7.5in;margin:0}@media print{body{background:white}main{max-width:none}section{break-after:page;margin:0;print-color-adjust:exact}section:last-child{break-after:auto}}</style></head><body><main>' +
    v.document.slides.map((s, i) => slideMarkup(v, s, i, delivery.images[s.id])).join('') +
    '</main></body></html>'
  );
}
export function downloadBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob),
    a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 30000);
}
export function filename(v: SlideVersion, ext: 'pptx' | 'html') {
  const name =
    v.document.title
      .replace(/[<>:"/\\|?*\u0000-\u001f]/g, '_')
      .replace(/[. ]+$/, '')
      .slice(0, 80) || uiText('演示文稿');
  return name + '-v' + v.version + '.' + ext;
}
export async function nativePptx(data: Delivery<SlideVersion>, signal: AbortSignal) {
  const { default: PowerPoint } = await import('pptxgenjs');
  failAbort(signal);
  const ppt = new PowerPoint(),
    v = data.version,
    p = colors[v.document.theme];
  ppt.layout = 'LAYOUT_WIDE';
  ppt.title = v.document.title;
  ppt.author = uiText('星图 AI Studio');
  ppt.subject = uiText('固定版本 v') + v.version;
  ppt.theme = { headFontFace: 'Microsoft YaHei', bodyFontFace: 'Microsoft YaHei' };
  for (const [idx, s] of v.document.slides.entries()) {
    const slide = ppt.addSlide(),
      { titleSize, bodySize } = slideSizing(s),
      cover = s.layout === 'title',
      picture = s.layout === 'image',
      width = picture ? 5.3 : 11.7;
    slide.background = { color: p.paper };
    slide.addShape(ppt.ShapeType.rect, {
      x: 0.8,
      y: 0.64,
      w: 0.8,
      h: 0.07,
      line: { transparency: 100 },
      fill: { color: p.accent },
    });
    slide.addText(s.title, {
      x: 0.8,
      y: cover ? 2.13 : 1.32,
      w: width,
      h: cover ? 2.0 : 1.65,
      fontFace: 'Microsoft YaHei',
      fontSize: titleSize,
      bold: true,
      color: p.ink,
      margin: 0,
      fit: 'shrink',
      valign: 'top',
      objectName: uiText('页标题'),
    });
    if (s.body)
      slide.addText(s.body, {
        x: 0.8,
        y: cover ? 4.5 : 3.22,
        w: width,
        h: cover ? 2.04 : 3.45,
        fontFace: 'Microsoft YaHei',
        fontSize: bodySize,
        color: p.sub,
        margin: 0,
        fit: 'shrink',
        valign: 'top',
        objectName: uiText('页正文'),
      });
    if (picture) {
      const img = data.images[s.id];
      if (!img) throw Error(uiText('该版本的图片未完整读取。'));
      const scale = Math.min(5.6 / img.width, 5.85 / img.height),
        w = img.width * scale,
        h = img.height * scale;
      slide.addImage({
        data: img.data,
        x: 6.93 + (5.6 - w) / 2,
        y: 0.75 + (5.85 - h) / 2,
        w,
        h,
        altText: s.title,
      });
    }
    slide.addShape(ppt.ShapeType.line, {
      x: 0.8,
      y: 6.94,
      w: 11.73,
      h: 0,
      line: { color: p.sub, width: 0.5 },
    });
    slide.addText(idx + 1 + ' / ' + v.document.slides.length, {
      x: 10.9,
      y: 7.01,
      w: 1.6,
      h: 0.25,
      fontSize: 10,
      color: p.sub,
      align: 'right',
      margin: 0,
    });
    if (s.notes) slide.addNotes(s.notes);
  }
  const blob = await ppt.write({ outputType: 'blob', compression: true });
  failAbort(signal);
  if (!(blob instanceof Blob)) throw Error(uiText('无法生成 PowerPoint 文件。'));
  return blob;
}
